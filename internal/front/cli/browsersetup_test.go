// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/platform/browserenv"
)

// fakeSys — компьютер для проверки: что установлено и какой дистрибутив.
func fakeSys(goos, arch, osRelease string, have map[string]string, vars map[string]string) browserenv.Sys {
	return browserenv.Sys{GOOS: goos, GOARCH: arch, Getenv: func(k string) string { return vars[k] },
		LookPath: func(n string) (string, error) {
			if p, ok := have[n]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
		ReadFile: func(p string) ([]byte, error) {
			if p == "/etc/os-release" && osRelease != "" {
				return []byte(osRelease), nil
			}
			return nil, errors.New("no")
		},
		Exists: func(p string) bool { _, ok := have[p]; return ok },
		Glob: func(pattern string) []string {
			var out []string
			for k := range have {
				if ok, _ := filepath.Match(pattern, k); ok {
					out = append(out, k)
				}
			}
			sort.Strings(out)
			return out
		},
		Home: vars["HOME"],
		Output: func(name string, args ...string) (string, error) {
			switch name {
			case "fc-list":
				return have["fc-list-out"], nil
			case "ldd":
				return have["ldd-out"], nil
			}
			return "Google Chrome 156.0.8078.47", nil
		},
		Run: func(context.Context, string, io.Writer) error { return nil }}
}

const fonts = "a\nb\nc\nd\ne\nf\n"

func TestCheckBrowser(t *testing.T) {
	ubuntu := "ID=ubuntu\nID_LIKE=debian\n"
	for _, c := range []struct {
		name, goos, arch, rel string
		have, vars            map[string]string
		display               string
		missing, cmds         []string
		ok                    bool
	}{
		{name: "desktop with Chrome", goos: "linux", arch: "amd64", rel: ubuntu, have: map[string]string{"google-chrome": "/usr/bin/google-chrome"},
			vars: map[string]string{"DISPLAY": ":0"}, display: "host", ok: true},
		{name: "server, nothing", goos: "linux", arch: "amd64", rel: ubuntu, have: map[string]string{"sudo": "/usr/bin/sudo"}, display: "none",
			missing: []string{"chrome", "xvfb", "fonts"},
			cmds:    []string{"google-chrome-stable_current_amd64.deb && sudo apt-get install -y /tmp/google-chrome.deb", "sudo apt-get install -y --no-install-recommends xvfb fonts-noto-core"}},
		{name: "arm64 server with Chrome", goos: "linux", arch: "arm64", rel: "ID=debian\n", have: map[string]string{"google-chrome": "/usr/bin/google-chrome", "fc-list-out": fonts},
			display: "headless", missing: []string{"xvfb"}, cmds: []string{"apt-get install -y --no-install-recommends xvfb"}, ok: true},
		{name: "fedora", goos: "linux", arch: "amd64", rel: "ID=fedora\n", have: map[string]string{"Xvfb": "/usr/bin/Xvfb", "fc-list-out": fonts},
			display: "none", missing: []string{"chrome"}, cmds: []string{"dnf install -y https://dl.google.com/linux/direct/google-chrome-stable_current_x86_64.rpm"}},
		{name: "rocky", goos: "linux", arch: "arm64", rel: "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", have: map[string]string{"chromium": "/usr/bin/chromium"},
			display: "headless", missing: []string{"xvfb", "fonts"}, cmds: []string{"dnf install -y xorg-x11-server-Xvfb google-noto-sans-fonts"}, ok: true},
		{name: "arch", goos: "linux", arch: "amd64", rel: "ID=arch\n", display: "none", missing: []string{"chrome", "xvfb", "fonts"},
			cmds: []string{"pacman -S --needed --noconfirm chromium xorg-server-xvfb noto-fonts"}},
		{name: "alpine", goos: "linux", arch: "amd64", rel: "ID=alpine\n", display: "none", missing: []string{"chrome", "xvfb", "fonts"},
			cmds: []string{"apk add chromium xvfb font-noto"}},
		{name: "virtual screen ready", goos: "linux", arch: "amd64", rel: ubuntu, have: map[string]string{"chromium": "/usr/bin/chromium", "Xvfb": "/usr/bin/Xvfb", "fc-list-out": fonts},
			display: "virtual", ok: true},
		// Chromium из кеша Playwright (без root) находится сам; его библиотеки проверяет ldd —
		// недостающие названы пакетами дистрибутива, Chrome без них не «ok».
		{name: "playwright chromium, missing libs", goos: "linux", arch: "amd64", rel: ubuntu, vars: map[string]string{"HOME": "/home/u"},
			have: map[string]string{"/home/u/.cache/ms-playwright/chromium-1140/chrome-linux/chrome": "", "sudo": "/usr/bin/sudo", "Xvfb": "/usr/bin/Xvfb", "fc-list-out": fonts,
				"ldd-out": "\tlinux-vdso.so.1 (0x00007ffd)\n\tlibnss3.so => not found\n\tlibatk-1.0.so.0 => not found\n\tlibgbm.so.1 => not found\n\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x0000)\n"},
			display: "virtual", missing: []string{"libraries"}, cmds: []string{"sudo apt-get install -y --no-install-recommends libnss3 libatk1.0-0 libgbm1"}},
		{name: "puppeteer chrome ok", goos: "linux", arch: "amd64", rel: ubuntu, vars: map[string]string{"HOME": "/home/u", "DISPLAY": ":0"},
			have: map[string]string{"/home/u/.cache/puppeteer/chrome/linux-131.0.6778.85/chrome-linux64/chrome": "", "ldd-out": "\tlibc.so.6 => /lib/libc.so.6 (0x0)\n"}, display: "host", ok: true},
		{name: "GATE4AI_CHROME", goos: "linux", arch: "amd64", rel: ubuntu, vars: map[string]string{"GATE4AI_CHROME": "/opt/chrome/chrome", "DISPLAY": ":0"},
			have: map[string]string{"/opt/chrome/chrome": ""}, display: "host", ok: true},
		{name: "snap chromium", goos: "linux", arch: "amd64", rel: ubuntu, vars: map[string]string{"DISPLAY": ":0"}, have: map[string]string{"/snap/bin/chromium": ""}, display: "host", ok: true},
		{name: "macOS", goos: "darwin", arch: "arm64", have: map[string]string{"brew": "/opt/homebrew/bin/brew"}, display: "none", missing: []string{"chrome"},
			cmds: []string{"brew install --cask google-chrome"}},
		{name: "windows", goos: "windows", arch: "amd64", vars: map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`},
			have: map[string]string{`C:\Users\u\AppData\Local/Google/Chrome/Application/chrome.exe`: ""}, display: "host", ok: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := browserenv.CheckBrowser(fakeSys(c.goos, c.arch, c.rel, c.have, c.vars))
			if got.Display != c.display || got.OK != c.ok || strings.Join(got.Missing, ",") != strings.Join(c.missing, ",") {
				t.Fatalf("%+v", got)
			}
			all := strings.Join(got.Commands, "\n")
			for _, want := range c.cmds {
				if !strings.Contains(all, want) {
					t.Errorf("no %q in\n%s", want, all)
				}
			}
			if len(c.cmds) == 0 && len(got.Commands) != 0 {
				t.Errorf("nothing to install, got %v", got.Commands)
			}
			if strings.Contains(strings.Join(c.missing, ","), "chrome") && strings.Contains(all, "dl.google.com") && !strings.Contains(strings.Join(got.Notes, " "), "Terms of Service") {
				t.Error("Google Chrome comes with its terms")
			}
			// Needs повторяет Missing по порядку; Chrome и библиотеки обязательны, остальное — рекомендации.
			var ids []string
			for _, n := range got.Needs {
				ids = append(ids, n.ID)
				if n.Required != (n.ID == "chrome" || n.ID == "libraries") {
					t.Errorf("%s required=%v", n.ID, n.Required)
				}
			}
			if strings.Join(ids, ",") != strings.Join(c.missing, ",") {
				t.Errorf("needs %v, missing %v", ids, c.missing)
			}
			if (len(c.missing) > 0) != (got.Summary != "") {
				t.Errorf("summary %q for missing %v", got.Summary, c.missing)
			}
		})
	}
	// Фраза для агента: сначала обязательное пакетами дистрибутива, затем рекомендации.
	got := browserenv.CheckBrowser(fakeSys("linux", "amd64", ubuntu, map[string]string{"/home/u/.cache/ms-playwright/chromium-1140/chrome-linux/chrome": "", "sudo": "/usr/bin/sudo",
		"ldd-out": "\tlibnss3.so => not found\n\tlibnssutil3.so => not found\n\tlibfoo.so.1 => not found\n"}, map[string]string{"HOME": "/home/u"}))
	if got.Summary != "To run Chrome, install: libnss3. Recommended: xvfb (a virtual screen: sites see an ordinary Chrome, not headless, so bot protection asks for a captcha or blocks less often); fonts-noto-core fonts-noto-color-emoji (fonts: without them pages show boxes instead of letters)." {
		t.Errorf("summary: %s", got.Summary)
	}
	if !got.NeedsRoot || got.OK || strings.Join(got.Libraries, ",") != "libnss3.so,libnssutil3.so,libfoo.so.1" || !strings.Contains(strings.Join(got.Notes, " "), "libfoo.so.1") {
		t.Errorf("%+v", got)
	}
	// Обёртка google-chrome — скрипт: ldd смотрит настоящий бинарник.
	s2 := fakeSys("linux", "amd64", ubuntu, map[string]string{"google-chrome": "/usr/bin/google-chrome", "/opt/google/chrome/chrome": "", "ldd-out": "\tlibgbm.so.1 => not found\n"}, map[string]string{"DISPLAY": ":0"})
	rf := s2.ReadFile
	s2.ReadFile = func(p string) ([]byte, error) {
		if p == "/usr/bin/google-chrome" {
			return []byte("#!/bin/sh\nexec /opt/google/chrome/chrome\n"), nil
		}
		return rf(p)
	}
	if got := browserenv.CheckBrowser(s2); got.OK || strings.Join(got.Libraries, ",") != "libgbm.so.1" {
		t.Errorf("%+v", got)
	}
	// root: без sudo.
	s := fakeSys("linux", "amd64", ubuntu, nil, nil)
	s.Root = true
	if c := browserenv.CheckBrowser(s); strings.Contains(strings.Join(c.Commands, " "), "sudo") {
		t.Fatal(c.Commands)
	}
}

func TestBrowserSetupCommand(t *testing.T) {
	var ran []string
	sys := fakeSys("linux", "amd64", "ID=debian\n", map[string]string{"google-chrome": "/usr/bin/google-chrome", "sudo": "/usr/bin/sudo"}, nil)
	sys.Run = func(_ context.Context, cmd string, _ io.Writer) error { ran = append(ran, cmd); return nil }
	old := setupSysFn
	setupSysFn = func() browserenv.Sys { return sys }
	defer func() { setupSysFn = old }()
	run := func(stdin string, interactive bool, args ...string) (int, string) {
		var out, errb bytes.Buffer
		e := &Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }, Interactive: interactive, StdoutTTY: true}
		code := Run(context.Background(), e, append([]string{"browser", "setup"}, args...))
		return code, out.String() + errb.String()
	}
	// Без согласия ничего не ставится; Chrome есть — выход 0.
	if code, out := run("", false); code != 0 || len(ran) != 0 || !strings.Contains(out, "apt-get install -y --no-install-recommends xvfb") || !strings.Contains(out, "headless") || !strings.Contains(out, "Recommended: xvfb (a virtual screen") {
		t.Fatal(code, ran, out)
	}
	if code, _ := run("n\n", true); code != 0 || len(ran) != 0 {
		t.Fatal("answered no:", ran)
	}
	if code, _ := run("y\n", true); code != 0 || len(ran) != 1 {
		t.Fatal("answered yes:", ran)
	}
	ran = nil
	// Без терминала sudo вызывается с -n: пароль спросить некому.
	if code, _ := run("", false, "--yes"); code != 0 || len(ran) != 1 || !strings.HasPrefix(ran[0], "sudo -n apt-get") {
		t.Fatal("--yes:", ran)
	}
	ran = nil
	if code, _ := run("y\n", true, "--yes"); code != 0 || len(ran) != 1 || !strings.HasPrefix(ran[0], "sudo apt-get") {
		t.Fatal("--yes at a terminal:", ran)
	}
	// Команда не прошла (нет прав): точные команды для root.
	sys.Run = func(_ context.Context, cmd string, _ io.Writer) error {
		return errors.New("sudo: a password is required")
	}
	if code, out := run("", false, "--yes"); code != ExitError || !strings.Contains(out, "Run as root (or with sudo):\n  sudo apt-get install -y --no-install-recommends xvfb") {
		t.Fatal(code, out)
	}
	sys.Run = func(_ context.Context, cmd string, _ io.Writer) error { ran = append(ran, cmd); return nil }
	ran = nil
	code, out := run("", false, "--json")
	var c browserenv.BrowserCheck
	if err := json.Unmarshal([]byte(out[:strings.LastIndex(out, "}")+1]), &c); err != nil || code != 0 || len(ran) != 0 || c.Display != "headless" {
		t.Fatal(code, err, out)
	}
	// Chrome нет — выход 1.
	sys = fakeSys("linux", "amd64", "ID=debian\n", nil, map[string]string{"DISPLAY": ":0"})
	if code, _ := run("", false, "--dry-run"); code != ExitError {
		t.Fatal("no Chrome:", code)
	}
}
