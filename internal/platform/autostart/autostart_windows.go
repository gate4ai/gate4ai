// SPDX-License-Identifier: AGPL-3.0-only

package autostart

import (
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Значения ключа Run (HKCU, без прав администратора).
const (
	runDaemon = "gate4ai"
	runTray   = "gate4ai-tray"
)

// runPath — значение ключа Run для вывода (files, removed).
func runPath(value string) string { return `HKCU\` + runKey + `\` + value }

func Install(s Spec) (Result, error) {
	var r Result
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return r, err
	}
	defer k.Close()
	// `gate4ai daemon start` запускает daemon без консоли и выходит: при входе
	// окно консоли мелькает на мгновение.
	cmd := winQuote(s.Bin) + " daemon start"
	for _, a := range s.Args {
		cmd += " " + winQuote(a)
	}
	if err := k.SetStringValue(runDaemon, cmd); err != nil {
		return r, err
	}
	r.Files = append(r.Files, runPath(runDaemon))
	if s.Tray != "" {
		if err := k.SetStringValue(runTray, winQuote(s.Tray)); err != nil {
			return r, err
		}
		r.Files = append(r.Files, runPath(runTray))
	}
	return r, nil
}

func Uninstall(string, bool) (removed, notes []string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil, nil
	}
	defer k.Close()
	for _, v := range []string{runDaemon, runTray} {
		if k.DeleteValue(v) == nil {
			removed = append(removed, runPath(v))
		}
	}
	return removed, []string{"a running daemon keeps running until gate4ai daemon stop or logout"}
}

func State(string) map[string]any {
	var files []string
	enabled := false
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE); err == nil {
		for _, v := range []string{runDaemon, runTray} {
			if _, _, err := k.GetStringValue(v); err == nil {
				files = append(files, runPath(v))
				enabled = enabled || v == runDaemon
			}
		}
		k.Close()
	}
	// running неизвестен: ключ Run только запускает daemon при входе.
	return map[string]any{"installed": len(files) > 0, "files": files, "enabled": enabled}
}

// Restart — служба автозапуска здесь daemon не перезапускает: это делает вызывающий.
func Restart() (bool, error) { return false, nil }
