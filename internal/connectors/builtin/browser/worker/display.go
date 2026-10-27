// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
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

// Режимы дисплея Chrome (настройка display).
const (
	DisplayAuto     = "auto"     // есть графический сеанс — он; Linux без него — Xvfb, без Xvfb — headless
	DisplayVirtual  = "virtual"  // Linux: виртуальный дисплей Xvfb, который поднимает пакет
	DisplayHeadless = "headless" // --headless=new: без дисплея, запасной режим (сайты видят признаки headless)
	DisplayHost     = "host"     // дисплей пользователя (DISPLAY / WAYLAND_DISPLAY, macOS, Windows)
)

// DisplayModes — допустимые значения display.
var DisplayModes = []string{DisplayAuto, DisplayVirtual, DisplayHeadless, DisplayHost}

// HeadlessWarning — что сказать владельцу о запасном режиме.
const HeadlessWarning = "the browser runs without a screen (--headless=new): sites with bot protection ask for a captcha or block more often; " +
	"install Xvfb for a virtual screen (Debian / Ubuntu: sudo apt install xvfb; Fedora / RHEL: sudo dnf install xorg-x11-server-Xvfb), see gate4ai browser setup"

// DisplayEnv — окружение, по которому решается режим auto (подменяется в тестах).
type DisplayEnv struct {
	GOOS   string
	Getenv func(string) string
	Look   func(string) (string, error) // exec.LookPath
}

// SystemDisplayEnv — окружение этого процесса.
func SystemDisplayEnv() DisplayEnv {
	return DisplayEnv{GOOS: runtime.GOOS, Getenv: os.Getenv, Look: exec.LookPath}
}

// hasScreen — у процесса есть графический сеанс: macOS и Windows — всегда
// (daemon работает в сеансе пользователя), Linux и BSD — DISPLAY или WAYLAND_DISPLAY.
func (e DisplayEnv) hasScreen() bool {
	if e.GOOS == "darwin" || e.GOOS == "windows" {
		return true
	}
	return e.Getenv("DISPLAY") != "" || e.Getenv("WAYLAND_DISPLAY") != ""
}

// ResolveDisplay — режим, в котором запускать Chrome: auto раскрывается в
// host, virtual или headless; virtual — только там, где есть X (не macOS и
// не Windows), и только с установленным Xvfb.
func ResolveDisplay(mode string, e DisplayEnv) (string, error) {
	switch mode {
	case "", DisplayAuto:
		if e.hasScreen() {
			return DisplayHost, nil
		}
		if _, err := e.Look("Xvfb"); err == nil {
			return DisplayVirtual, nil
		}
		return DisplayHeadless, nil
	case DisplayVirtual:
		if e.GOOS == "darwin" || e.GOOS == "windows" {
			return "", fmt.Errorf("display: virtual is for Linux (Xvfb); on %s Chrome uses your screen (display: host) or runs headless", e.GOOS)
		}
		if _, err := e.Look("Xvfb"); err != nil {
			return "", errors.New("display: virtual needs Xvfb, it is not installed (Debian / Ubuntu: sudo apt install xvfb; Fedora / RHEL: sudo dnf install xorg-x11-server-Xvfb); see gate4ai browser setup")
		}
		return DisplayVirtual, nil
	case DisplayHeadless:
		return DisplayHeadless, nil
	case DisplayHost:
		if !e.hasScreen() {
			return "", errors.New("display: host needs a graphical session (DISPLAY or WAYLAND_DISPLAY is not set); use display: auto")
		}
		return DisplayHost, nil
	}
	return "", fmt.Errorf("display %q must be one of %s", mode, strings.Join(DisplayModes, ", "))
}

// WindowSize — размер окна Chrome на виртуальном экране и в headless:
// самое распространённое разрешение десктопа у настоящих пользователей
// (StatCounter, desktop worldwide, 2026: 1920×1080 — около 20 %, следующие —
// по 6–7 %). Без флага --headless=new открывал бы окна 800×600, какие у людей
// почти не встречаются: сайты верстаются иначе, а антифрод замечает редкий размер.
const WindowSize = "1920,1080"

// displayFlags — флаги Chrome под режим.
func displayFlags(mode string) []string {
	switch mode {
	case DisplayHeadless:
		return []string{"--headless=new", "--window-size=" + WindowSize}
	case DisplayVirtual:
		// Chrome на Wayland-сессии иначе выбрал бы Wayland, а не дисплей Xvfb.
		return []string{"--ozone-platform=x11", "--window-size=" + WindowSize, "--window-position=0,0"}
	}
	return nil
}

// XvfbScreen — размер виртуального экрана (под окно WindowSize).
const XvfbScreen = "1920x1080x24"

// Xvfb — виртуальный дисплей, который поднял пакет: X-сервер в память, без
// TCP (-nolisten tcp), доступ — по cookie из файла в каталоге пакета (0600),
// поэтому другие пользователи ОС не снимут экран и не пошлют ввод.
type Xvfb struct {
	Display string // ":N"
	Auth    string // файл XAUTHORITY
	proc    *os.Process
	done    chan struct{}
}

// Env — переменные окружения для Chrome на этом дисплее.
func (x *Xvfb) Env() []string {
	return []string{"DISPLAY=" + x.Display, "XAUTHORITY=" + x.Auth}
}

// Alive — X-сервер работает.
func (x *Xvfb) Alive() bool {
	if x == nil {
		return false
	}
	select {
	case <-x.done:
		return false
	default:
		return true
	}
}

// Stop останавливает X-сервер.
func (x *Xvfb) Stop() {
	if x == nil || x.proc == nil {
		return
	}
	_ = x.proc.Kill()
	<-x.done
	_ = os.Remove(x.Auth)
}

// StartXvfb запускает Xvfb со свободным номером дисплея (-displayfd) и
// cookie доступа в dir.
func StartXvfb(ctx context.Context, dir string) (*Xvfb, error) {
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		return nil, errors.New("Xvfb is not installed")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	auth := filepath.Join(dir, "Xauthority")
	cookie := make([]byte, 16)
	_, _ = rand.Read(cookie)
	if err := os.WriteFile(auth, XauthEntry(cookie), 0o600); err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "-displayfd", "3", "-screen", "0", XvfbScreen, "-nolisten", "tcp", "-auth", auth, "-nocursor")
	cmd.ExtraFiles = []*os.File{w}
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("start Xvfb: %w", err)
	}
	w.Close()
	x := &Xvfb{Auth: auth, proc: cmd.Process, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(x.done) }()
	num := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		r.Close()
		num <- strings.TrimSpace(line)
	}()
	select {
	case n := <-num:
		if n == "" {
			x.Stop()
			return nil, errors.New("Xvfb did not start (no display number)")
		}
		x.Display = ":" + n
		return x, nil
	case <-x.done:
		return nil, errors.New("Xvfb exited at start")
	case <-time.After(10 * time.Second):
		x.Stop()
		return nil, errors.New("Xvfb did not report its display in 10s")
	case <-ctx.Done():
		x.Stop()
		return nil, ctx.Err()
	}
}

// XauthEntry — запись файла Xauthority: семейство FamilyWild без адреса и
// номера дисплея (подходит к любому дисплею), MIT-MAGIC-COOKIE-1 с cookie.
func XauthEntry(cookie []byte) []byte {
	var b []byte
	field := func(v []byte) {
		b = binary.BigEndian.AppendUint16(b, uint16(len(v)))
		b = append(b, v...)
	}
	b = binary.BigEndian.AppendUint16(b, 0xffff) // FamilyWild
	field(nil)                                   // адрес
	field(nil)                                   // номер дисплея
	field([]byte("MIT-MAGIC-COOKIE-1"))
	field(cookie)
	return b
}

// display — дисплей Chrome воркера: режим и Xvfb, если он нужен.
type display struct {
	mu   sync.Mutex
	mode string // решённый режим последнего запуска
	x    *Xvfb
}

// prepare решает режим и, для virtual, поднимает Xvfb (или берёт живой);
// возвращает режим, окружение и флаги Chrome.
func (d *display) prepare(ctx context.Context, setting, dir string, e DisplayEnv) (string, []string, []string, error) {
	mode, err := ResolveDisplay(setting, e)
	if err != nil {
		return "", nil, nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mode = mode
	if mode != DisplayVirtual {
		return mode, nil, displayFlags(mode), nil
	}
	if !d.x.Alive() {
		x, err := StartXvfb(ctx, dir)
		if err != nil {
			return "", nil, nil, err
		}
		d.x = x
	}
	// WAYLAND_DISPLAY пустой: Chrome на Wayland-сессии ушёл бы на неё.
	return mode, append(d.x.Env(), "WAYLAND_DISPLAY="), displayFlags(mode), nil
}

// current — режим последнего запуска Chrome ("" — ещё не запускался).
func (d *display) current() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mode
}

func (d *display) stop() {
	d.mu.Lock()
	x := d.x
	d.x = nil
	d.mu.Unlock()
	x.Stop()
}
