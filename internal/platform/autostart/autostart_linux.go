// SPDX-License-Identifier: AGPL-3.0-only

package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

const systemdUnit = "gate4ai.service"

func configDir(home string) string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	return filepath.Join(home, ".config")
}

func unitPath(home string) string {
	return filepath.Join(configDir(home), "systemd", "user", systemdUnit)
}

func desktopPath(home, name string) string {
	return filepath.Join(configDir(home), "autostart", name+".desktop")
}

// systemdUser — у пользователя есть systemd --user.
func systemdUser() bool {
	_, err := runCmd("systemctl", "--user", "show-environment")
	return err == nil
}

// unitFile — user unit daemon. Выход 1 при «already running» не крутит
// перезапуски бесконечно: StartLimitBurst.
func unitFile(s Spec) string {
	exec := strings.ReplaceAll(quoteAll(append([]string{s.Bin, "daemon"}, s.Args...)), "%", "%%")
	return `[Unit]
Description=gate4.ai daemon: policies, approvals, secrets and journal for AI agents
Documentation=https://github.com/gate4ai/gate4ai#readme
StartLimitIntervalSec=120
StartLimitBurst=5

[Service]
ExecStart=` + exec + `
Restart=on-failure
RestartSec=5
StandardOutput=append:` + s.Log + `
StandardError=append:` + s.Log + `

[Install]
WantedBy=default.target
`
}

// desktopEntry — XDG autostart (сеанс рабочего стола без systemd, трей).
func desktopEntry(name string, argv []string) string {
	return `[Desktop Entry]
Type=Application
Name=` + name + `
Exec=` + strings.ReplaceAll(quoteAll(argv), "%", "%%") + `
Terminal=false
X-GNOME-Autostart-enabled=true
`
}

func Install(s Spec) (Result, error) {
	var r Result
	if err := os.MkdirAll(filepath.Dir(s.Log), 0o700); err != nil {
		return r, err
	}
	if systemdUser() {
		p := unitPath(s.Home)
		if err := writeFile(p, unitFile(s)); err != nil {
			return r, err
		}
		r.Files = append(r.Files, p)
		if out, err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
			return r, cmdErr("systemctl --user daemon-reload", out, err)
		}
		enable := []string{"--user", "enable", systemdUnit}
		if out, err := runCmd("systemctl", enable...); err != nil {
			return r, cmdErr("systemctl "+strings.Join(enable, " "), out, err)
		}
		if !s.NoStart {
			// restart, а не enable --now: уже работающая служба (обновление)
			// иначе осталась бы со старым бинарником.
			if out, err := runCmd("systemctl", "--user", "restart", systemdUnit); err != nil {
				return r, cmdErr("systemctl --user restart "+systemdUnit, out, err)
			}
		}
		r.Started = !s.NoStart
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			r.Notes = append(r.Notes, "on a server without a login session the user services stop at logout; keep them running: loginctl enable-linger $USER")
		}
	} else {
		p := desktopPath(s.Home, "gate4ai")
		if err := writeFile(p, desktopEntry("gate4.ai daemon", append([]string{s.Bin, "daemon", "start"}, s.Args...))); err != nil {
			return r, err
		}
		r.Files = append(r.Files, p)
		r.Notes = append(r.Notes, "no systemd --user: the daemon starts with your desktop session (XDG autostart)")
	}
	if s.Tray != "" {
		p := desktopPath(s.Home, "gate4ai-tray")
		if err := writeFile(p, desktopEntry("gate4.ai tray", []string{s.Tray})); err != nil {
			return r, err
		}
		r.Files = append(r.Files, p)
	}
	return r, nil
}

func Uninstall(home string, keepRunning bool) ([]string, []string, error) {
	var notes []string
	unit := unitPath(home)
	if fileExists(unit) {
		if keepRunning {
			_, _ = runCmd("systemctl", "--user", "disable", systemdUnit)
		} else {
			_, _ = runCmd("systemctl", "--user", "disable", "--now", systemdUnit)
			notes = append(notes, "the daemon started by systemd was stopped")
		}
	}
	files := removeFiles(unit, desktopPath(home, "gate4ai"), desktopPath(home, "gate4ai-tray"))
	if fileExists(filepath.Dir(unit)) {
		_, _ = runCmd("systemctl", "--user", "daemon-reload")
	}
	return files, notes, nil
}

func State(home string) map[string]any {
	var files []string
	for _, f := range []string{unitPath(home), desktopPath(home, "gate4ai"), desktopPath(home, "gate4ai-tray")} {
		if fileExists(f) {
			files = append(files, f)
		}
	}
	res := map[string]any{"installed": len(files) > 0, "files": files,
		"enabled": fileExists(desktopPath(home, "gate4ai"))}
	if fileExists(unitPath(home)) {
		out, _ := runCmd("systemctl", "--user", "is-active", systemdUnit)
		res["systemd"] = strings.TrimSpace(string(out))
		res["running"] = res["systemd"] == "active"
		out, _ = runCmd("systemctl", "--user", "is-enabled", systemdUnit)
		en := strings.TrimSpace(string(out))
		res["systemd_enabled"] = en
		res["enabled"] = en == "enabled" || res["enabled"] == true
	}
	return res
}

// Restart перезапускает daemon, если он работает службой systemd --user
// (false — не служба: перезапускает вызывающий).
func Restart() (bool, error) {
	if !systemdUser() {
		return false, nil
	}
	if out, _ := runCmd("systemctl", "--user", "is-active", systemdUnit); strings.TrimSpace(string(out)) != "active" {
		return false, nil
	}
	if out, err := runCmd("systemctl", "--user", "restart", systemdUnit); err != nil {
		return false, cmdErr("systemctl --user restart "+systemdUnit, out, err)
	}
	return true, nil
}
