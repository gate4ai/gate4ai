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

func winQuote(s string) string { return `"` + s + `"` }

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
	r.Files = append(r.Files, `HKCU\`+runKey+`\`+runDaemon)
	if s.Tray != "" {
		if err := k.SetStringValue(runTray, winQuote(s.Tray)); err != nil {
			return r, err
		}
		r.Files = append(r.Files, `HKCU\`+runKey+`\`+runTray)
	}
	return r, nil
}

func Uninstall(string, bool) ([]string, []string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return nil, nil, nil
	}
	defer k.Close()
	var removed []string
	for _, v := range []string{runDaemon, runTray} {
		if k.DeleteValue(v) == nil {
			removed = append(removed, `HKCU\`+runKey+`\`+v)
		}
	}
	return removed, []string{"a running daemon keeps running until gate4ai daemon stop or logout"}, nil
}

func State(string) map[string]any {
	var files []string
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE); err == nil {
		for _, v := range []string{runDaemon, runTray} {
			if _, _, err := k.GetStringValue(v); err == nil {
				files = append(files, `HKCU\`+runKey+`\`+v)
			}
		}
		k.Close()
	}
	return map[string]any{"installed": len(files) > 0, "files": files}
}

// Restart — служба автозапуска здесь daemon не перезапускает: это делает вызывающий.
func Restart() (bool, error) { return false, nil }
