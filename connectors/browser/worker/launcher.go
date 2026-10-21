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

// Launch запускает настоящий Chrome с выделенным профилем (не headless): с Chrome 136
// удалённая отладка не работает с профилем по умолчанию. Порт выбирает Chrome
// (--remote-debugging-port=0) и пишет в DevToolsActivePort профиля.
func Launch(ctx context.Context, chrome, profileDir string, extra ...string) (cdpURL string, proc *os.Process, err error) {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return "", nil, err
	}
	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	_ = os.Remove(portFile)
	args := []string{
		"--user-data-dir=" + profileDir,
		"--remote-debugging-port=0",
		"--remote-debugging-address=127.0.0.1",
		"--enable-unsafe-extension-debugging", // Stagehand ставит своё расширение через CDP Extensions.loadUnpacked
		// С портом отладки Chrome выставляет navigator.webdriver = true — прямой признак
		// автоматизации для антифрода и reCAPTCHA (login.mos.ru: «подозрительная активность»).
		"--disable-blink-features=AutomationControlled",
		"--no-first-run", "--no-default-browser-check",
	}
	cmd := exec.Command(chrome, append(append(args, extra...), "about:blank")...)
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
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return "", nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return "", nil, errors.New("chrome did not report its DevTools port")
}
