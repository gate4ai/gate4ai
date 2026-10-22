// SPDX-License-Identifier: AGPL-3.0-only

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceLinux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	var calls []string
	old := runCmd
	t.Cleanup(func() { runCmd = old })
	systemd, enabled := true, "enabled"
	runCmd = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if !systemd {
			return nil, errors.New("no systemd")
		}
		if len(args) > 1 && args[1] == "is-enabled" {
			return []byte(enabled + "\n"), nil
		}
		return []byte("active\n"), nil
	}
	spec := Spec{Bin: "/opt/my apps/gate4ai", Tray: "/opt/my apps/gate4ai-tray", Args: []string{"--keystore", "file"},
		Log: filepath.Join(home, "state", "daemon.log"), Home: home}
	r, err := Install(spec)
	if err != nil || !r.Started {
		t.Fatal(r, err)
	}
	unit, _ := os.ReadFile(filepath.Join(home, ".config/systemd/user/gate4ai.service"))
	for _, want := range []string{`ExecStart="/opt/my apps/gate4ai" daemon --keystore file`, "Restart=on-failure", "StartLimitBurst=5", "StandardError=append:" + spec.Log, "WantedBy=default.target"} {
		if !strings.Contains(string(unit), want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
	tray, _ := os.ReadFile(filepath.Join(home, ".config/autostart/gate4ai-tray.desktop"))
	if !strings.Contains(string(tray), `Exec="/opt/my apps/gate4ai-tray"`) {
		t.Fatal(string(tray))
	}
	// restart, а не enable --now: уже работающая служба (обновление) получает новый бинарник.
	if j := strings.Join(calls, "\n"); !strings.Contains(j, "systemctl --user enable gate4ai.service") || !strings.Contains(j, "systemctl --user restart gate4ai.service") {
		t.Fatal(calls)
	}
	if st := State(home); st["installed"] != true || st["systemd"] != "active" || st["enabled"] != true || st["running"] != true {
		t.Fatal(st)
	}
	// Файл службы есть, но служба выключена (systemctl --user disable).
	enabled = "disabled"
	if st := State(home); st["installed"] != true || st["enabled"] != false || st["systemd_enabled"] != "disabled" {
		t.Fatal(st)
	}
	enabled = "enabled"
	files, _ := Uninstall(home, false)
	if st := State(home); len(files) != 2 || st["installed"] != false || st["enabled"] != false || st["running"] != nil {
		t.Fatal(files)
	}

	// Из Web UI: daemon уже работает — включить без запуска и выключить без остановки.
	calls = nil
	spec.NoStart = true
	if r, err := Install(spec); err != nil || r.Started {
		t.Fatal(r, err)
	}
	if j := strings.Join(calls, "\n"); strings.Contains(j, "--now") || !strings.Contains(j, "systemctl --user enable gate4ai.service") {
		t.Fatal(calls)
	}
	calls = nil
	_, _ = Uninstall(home, true)
	if j := strings.Join(calls, "\n"); strings.Contains(j, "--now") {
		t.Fatal(calls)
	}
	spec.NoStart = false

	// Без systemd --user: XDG autostart запускает `gate4ai daemon start`.
	systemd = false
	r, err = Install(spec)
	if err != nil || r.Started {
		t.Fatal(r, err)
	}
	d, _ := os.ReadFile(filepath.Join(home, ".config/autostart/gate4ai.desktop"))
	if !strings.Contains(string(d), `Exec="/opt/my apps/gate4ai" daemon start --keystore file`) {
		t.Fatal(string(d))
	}
	// XDG autostart: установлен — значит включён; работает ли — неизвестно.
	if st := State(home); st["enabled"] != true || st["running"] != nil {
		t.Fatal(st)
	}
}

func TestRestartLinux(t *testing.T) {
	var calls []string
	old := runCmd
	t.Cleanup(func() { runCmd = old })
	state := "active\n"
	runCmd = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(args) > 1 && args[1] == "is-active" {
			return []byte(state), nil
		}
		return nil, nil
	}
	if ok, err := Restart(); !ok || err != nil || !strings.Contains(strings.Join(calls, "\n"), "systemctl --user restart gate4ai.service") {
		t.Fatal(ok, err, calls)
	}
	// Служба не активна (daemon запущен руками) — перезапускает вызывающий.
	state, calls = "inactive\n", nil
	if ok, err := Restart(); ok || err != nil || strings.Contains(strings.Join(calls, "\n"), "restart") {
		t.Fatal(ok, err, calls)
	}
}
