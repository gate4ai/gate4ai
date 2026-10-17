// SPDX-License-Identifier: AGPL-3.0-only

// Package autostart — запуск daemon (и трея) при входе пользователя в систему:
// systemd --user или XDG autostart (Linux), LaunchAgent (macOS), ключ Run в
// реестре пользователя (Windows). Используется `gate4ai service` и Web UI.
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Spec — что запускать при входе в систему.
type Spec struct {
	Bin  string   // gate4ai
	Tray string   // gate4ai-tray ("" — без трея)
	Args []string // флаги daemon
	Log  string   // журнал daemon
	Home string   // домашний каталог пользователя
	// NoStart — только включить автозапуск, не запуская daemon сейчас (daemon уже
	// работает, например включение из Web UI).
	NoStart bool
}

// Result — что сделала установка автозапуска.
type Result struct {
	Files   []string // файлы и ключи автозапуска
	Started bool     // daemon уже запущен механизмом автозапуска
	Notes   []string
}

// runCmd выполняет системную команду (systemctl, launchctl); подменяется в тестах.
var runCmd = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// FindTray — gate4ai-tray рядом с gate4ai или в PATH.
func FindTray(bin string) string {
	name := "gate4ai-tray"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if p := filepath.Join(filepath.Dir(bin), name); fileExists(p) {
		return p
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeFile пишет файл автозапуска, создавая каталоги.
func writeFile(p, body string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}

// removeFiles удаляет существующие файлы и возвращает удалённые.
func removeFiles(files ...string) []string {
	var out []string
	for _, f := range files {
		if os.Remove(f) == nil {
			out = append(out, f)
		}
	}
	return out
}

// shellQuote — аргумент для Exec= (desktop entry) и ExecStart= (systemd).
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$`%") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(s) + `"`
}

func quoteAll(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// cmdErr — ошибка системной команды с её выводом.
func cmdErr(cmd string, out []byte, err error) error {
	if s := strings.TrimSpace(string(out)); s != "" {
		return fmt.Errorf("%s: %v: %s", cmd, err, s)
	}
	return fmt.Errorf("%s: %v", cmd, err)
}
