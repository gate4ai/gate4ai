// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ChromeCandidates — где искать системный Chrome.
func ChromeCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	case "windows":
		var out []string
		for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
			if d := os.Getenv(env); d != "" {
				out = append(out, filepath.Join(d, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
		return out
	}
	return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
}

// FindChrome возвращает путь к Chrome.
func FindChrome() (string, error) {
	for _, c := range ChromeCandidates() {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Chrome not found; set connectors.browser.chrome_path")
}

// Launch запускает настоящий Chrome с выделенным профилем: с Chrome 136
// удалённая отладка не работает с профилем по умолчанию. Окружение — как у
// воркера; другое окружение (дисплей Xvfb) — LaunchEnv.
func Launch(ctx context.Context, chrome, profileDir string, extra ...string) (cdpURL string, proc *os.Process, err error) {
	return LaunchEnv(ctx, chrome, profileDir, nil, extra...)
}

// LaunchEnv — Launch с дополнительными переменными окружения Chrome (env
// поверх окружения воркера). Порт отладки Chrome не открывает: CDP идёт по
// pipe (--remote-debugging-pipe), клиентам адрес отдаёт Mux — 127.0.0.1 с
// секретным токеном (cdpURL — http://127.0.0.1:<port>/t/<token>). На Windows
// pipe в Go не передать (exec.Cmd.ExtraFiles) — там прежний порт
// (--remote-debugging-port=0 на 127.0.0.1).
func LaunchEnv(ctx context.Context, chrome, profileDir string, env []string, extra ...string) (cdpURL string, proc *os.Process, err error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return "", nil, err
	}
	args := []string{
		"--user-data-dir=" + profileDir,
		// С отладкой Chrome выставляет navigator.webdriver = true — прямой признак
		// автоматизации для антифрода и reCAPTCHA (login.mos.ru: «подозрительная активность»).
		"--disable-blink-features=AutomationControlled",
		"--no-first-run", "--no-default-browser-check",
	}
	if !pipeSupported {
		return launchPort(ctx, chrome, profileDir, env, append(args, extra...))
	}
	chromeIn, toChrome, err := os.Pipe() // fd 3 Chrome: его команды
	if err != nil {
		return "", nil, err
	}
	fromChrome, chromeOut, err := os.Pipe() // fd 4 Chrome: ответы и события
	if err != nil {
		toChrome.Close()
		chromeIn.Close()
		return "", nil, err
	}
	cmd := exec.Command(chrome, append(append(append(args, "--remote-debugging-pipe"), extra...), "about:blank")...)
	cmd.ExtraFiles = []*os.File{chromeIn, chromeOut}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	tail := &tailBuffer{max: 2048}
	cmd.Stderr = tail
	ownGroup(cmd)
	err = cmd.Start()
	chromeIn.Close()
	chromeOut.Close()
	if err != nil {
		toChrome.Close()
		fromChrome.Close()
		return "", nil, fmt.Errorf("launch chrome: %w", err)
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := StartMux(sctx, fromChrome, toChrome)
	if err != nil {
		_ = cmd.Process.Kill()
		toChrome.Close()
		fromChrome.Close()
		if t := tail.String(); t != "" {
			return "", nil, fmt.Errorf("chrome did not answer on its DevTools pipe: %w; chrome said: %s%s", err, t, sandboxHint(t))
		}
		return "", nil, fmt.Errorf("chrome did not answer on its DevTools pipe: %w", err)
	}
	go func() {
		<-m.Done()
		toChrome.Close()
		fromChrome.Close()
	}()
	return m.URL(), cmd.Process, nil
}

// launchPort — запуск с портом отладки (Windows): Chrome выбирает порт сам
// (--remote-debugging-port=0) и пишет его в DevToolsActivePort профиля.
func launchPort(ctx context.Context, chrome, profileDir string, env, args []string) (string, *os.Process, error) {
	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	_ = os.Remove(portFile)
	// Подключения воркера идут без заголовка Origin: Chrome их пускает и без
	// --remote-allow-origins.
	args = append(args, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1")
	cmd := exec.Command(chrome, append(args, "about:blank")...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("launch chrome: %w", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if f, err := os.Open(portFile); err == nil {
			sc := bufio.NewScanner(f)
			if sc.Scan() {
				port := strings.TrimSpace(sc.Text())
				f.Close()
				if port != "" && port != "0" {
					return "http://127.0.0.1:" + port, cmd.Process, nil
				}
			} else {
				f.Close()
			}
		}
		if err := wait(ctx, 100*time.Millisecond); err != nil {
			_ = cmd.Process.Kill()
			return "", nil, err
		}
	}
	_ = cmd.Process.Kill()
	return "", nil, errors.New("chrome did not report its DevTools port")
}

// sandboxHint — песочнице Chrome не дали пространства имён (Docker с
// seccomp по умолчанию, AppArmor Ubuntu 24.04 для Chrome не из пакета).
func sandboxHint(stderr string) string {
	if !strings.Contains(stderr, "namespace") && !strings.Contains(stderr, "No usable sandbox") {
		return ""
	}
	return " (the Chrome sandbox needs user namespaces: in Docker run the container with --security-opt seccomp=unconfined or Chrome's seccomp profile; " +
		"on Ubuntu 24.04 install the google-chrome-stable package, it brings its AppArmor profile)"
}

// tailBuffer — последние max байт вывода (stderr Chrome для ошибки запуска).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}
