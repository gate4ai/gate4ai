// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// OpenURL открывает ссылку в браузере (подменяется в тестах). Процесс
// открывателя реапится, чтобы долгоживущий трей не копил зомби.
var OpenURL = func(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// handoffDir — каталог страниц-передатчиков ссылки входа (подменяется в тестах).
var handoffDir = func() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "gate4ai", "open"), nil
}

// handoffTTL — сколько хранится страница-передатчик: дольше жизни ссылки входа.
const handoffTTL = 5 * time.Minute

// OpenLoginURL открывает одноразовую ссылку входа так, чтобы токен не попал в
// argv (на Linux и macOS argv любого процесса видят все локальные
// пользователи, а Web UI на loopback доступен и им): браузер
// получает путь к странице-передатчику (файл 0600 в личном каталоге кэша),
// которая сама переходит по ссылке. На Windows argv чужих процессов другим
// пользователям не виден — ссылка открывается как есть. Snap-браузеры не
// читают скрытые каталоги домашней папки — для них тоже ссылка как есть.
func OpenLoginURL(u string) error {
	if runtime.GOOS == "windows" || snapBrowser() {
		return OpenURL(u)
	}
	path, err := writeHandoff(u)
	if err != nil {
		return OpenURL(u)
	}
	return OpenURL((&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String())
}

// writeHandoff пишет страницу-передатчик и удаляет устаревшие.
func writeHandoff(u string) (string, error) {
	dir, err := handoffDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o700)
	old, _ := filepath.Glob(filepath.Join(dir, "login-*.html"))
	for _, f := range old {
		if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > handoffTTL {
			_ = os.Remove(f)
		}
	}
	f, err := os.CreateTemp(dir, "login-*.html")
	if err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	_, err = f.WriteString(handoffPage(u))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// handoffPage — страница, которая сразу переходит по ссылке (без истории).
// Переход один: ссылка одноразовая, и второй (meta refresh вдогонку скрипту)
// мог бы отменить первый, когда сервер уже израсходовал токен.
func handoffPage(u string) string {
	q := html.EscapeString(u)
	js := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "<", `\x3c`, ">", `\x3e`, "\n", "", "\r", "").Replace(u)
	return `<!doctype html><meta charset="utf-8"><meta name="referrer" content="no-referrer">` +
		`<noscript><meta http-equiv="refresh" content="0;url=` + q + `"></noscript><title>gate4.ai</title>` +
		`<script>location.replace("` + js + `")</script><a href="` + q + `">Open gate4.ai</a>`
}

// snapBrowser — браузер по умолчанию на Linux установлен из snap (desktop-файл
// вида firefox_firefox.desktop): ему недоступен личный каталог кэша.
var snapBrowser = func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	out, err := exec.Command("xdg-settings", "get", "default-web-browser").Output()
	if err != nil {
		return false
	}
	name := strings.TrimSuffix(strings.TrimSpace(string(out)), ".desktop")
	if a, b, ok := strings.Cut(name, "_"); ok && a == b {
		_, err := os.Stat("/snap/bin/" + a)
		return err == nil
	}
	return false
}
