// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

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
	systemd := true
	runCmd = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if !systemd {
			return nil, errors.New("no systemd")
		}
		return []byte("active\n"), nil
	}
	spec := serviceSpec{Bin: "/opt/my apps/gate4ai", Tray: "/opt/my apps/gate4ai-tray", Args: []string{"--keystore", "file"},
		Log: filepath.Join(home, "state", "daemon.log"), Home: home}
	r, err := installService(spec)
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
	if !strings.Contains(strings.Join(calls, "\n"), "systemctl --user enable --now gate4ai.service") {
		t.Fatal(calls)
	}
	if st := serviceState(home); st["installed"] != true || st["systemd"] != "active" {
		t.Fatal(st)
	}
	files, _, _ := uninstallService(home)
	if len(files) != 2 || serviceState(home)["installed"] != false {
		t.Fatal(files)
	}

	// Без systemd --user: XDG autostart запускает `gate4ai daemon start`.
	systemd = false
	r, err = installService(spec)
	if err != nil || r.Started {
		t.Fatal(r, err)
	}
	d, _ := os.ReadFile(filepath.Join(home, ".config/autostart/gate4ai.desktop"))
	if !strings.Contains(string(d), `Exec="/opt/my apps/gate4ai" daemon start --keystore file`) {
		t.Fatal(string(d))
	}
}
