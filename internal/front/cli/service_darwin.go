// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

const (
	launchDaemon = "ai.gate4.daemon"
	launchTray   = "ai.gate4.tray"
)

func plistPath(home, label string) string {
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func gui() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// plist — LaunchAgent; keepAlive — перезапуск после сбоя (не после штатной остановки).
func plist(label string, argv []string, log string, keepAlive bool) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, a := range argv {
		b.WriteString("    <string>" + html.EscapeString(a) + "</string>\n")
	}
	b.WriteString("  </array>\n  <key>RunAtLoad</key><true/>\n")
	if keepAlive {
		b.WriteString("  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n  <key>ThrottleInterval</key><integer>10</integer>\n")
	}
	if log != "" {
		b.WriteString("  <key>StandardOutPath</key><string>" + html.EscapeString(log) + "</string>\n")
		b.WriteString("  <key>StandardErrorPath</key><string>" + html.EscapeString(log) + "</string>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func installService(s serviceSpec) (serviceResult, error) {
	var r serviceResult
	if err := os.MkdirAll(filepath.Dir(s.Log), 0o700); err != nil {
		return r, err
	}
	p := plistPath(s.Home, launchDaemon)
	if err := writeFile(p, plist(launchDaemon, append([]string{s.Bin, "daemon"}, s.Args...), s.Log, true)); err != nil {
		return r, err
	}
	r.Files = append(r.Files, p)
	_, _ = runCmd("launchctl", "bootout", gui()+"/"+launchDaemon)
	if out, err := runCmd("launchctl", "bootstrap", gui(), p); err != nil {
		return r, cmdErr("launchctl bootstrap "+gui()+" "+p, out, err)
	}
	r.Started = true
	if s.Tray != "" {
		// Трей загрузится при следующем входе: сейчас он может быть уже запущен руками.
		tp := plistPath(s.Home, launchTray)
		if err := writeFile(tp, plist(launchTray, []string{s.Tray}, "", false)); err != nil {
			return r, err
		}
		r.Files = append(r.Files, tp)
	}
	return r, nil
}

func uninstallService(home string) ([]string, []string, error) {
	var notes []string
	if fileExists(plistPath(home, launchDaemon)) {
		_, _ = runCmd("launchctl", "bootout", gui()+"/"+launchDaemon)
		notes = append(notes, "the daemon started by launchd was stopped")
	}
	return removeFiles(plistPath(home, launchDaemon), plistPath(home, launchTray)), notes, nil
}

func serviceState(home string) map[string]any {
	var files []string
	for _, f := range []string{plistPath(home, launchDaemon), plistPath(home, launchTray)} {
		if fileExists(f) {
			files = append(files, f)
		}
	}
	return map[string]any{"installed": len(files) > 0, "files": files}
}
