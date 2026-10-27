// SPDX-License-Identifier: AGPL-3.0-only

package browserenv

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// debianSys — сервер Debian без root: apt-get и dpkg-deb есть, библиотек
// Chrome нет; ldd отвечает по содержимому каталога lib (что положили — найдено).
func debianSys(t *testing.T, dataDir string, deps map[string][]string) (Sys, *[]string) {
	t.Helper()
	var cmds []string
	lib := UserLibDir(dataDir)
	ldd := func() string {
		var out []string
		for name := range deps {
			if _, err := os.Lstat(filepath.Join(lib, name)); err != nil {
				out = append(out, "\t"+name+" => not found")
				continue
			}
			for _, d := range deps[name] { // зависимости положенной библиотеки
				if _, err := os.Lstat(filepath.Join(lib, d)); err != nil {
					out = append(out, "\t"+d+" => not found")
				}
			}
		}
		return "\tlibc.so.6 => /lib/libc.so.6 (0x0)\n" + strings.Join(out, "\n") + "\n"
	}
	download := regexp.MustCompile(`cd '([^']+)' && apt-get download '([^']+)'`)
	extract := regexp.MustCompile(`dpkg-deb -x '([^']+)' '([^']+)'`)
	s := Sys{GOOS: "linux", GOARCH: "amd64", DataDir: dataDir, Home: "/home/u", Getenv: func(string) string { return "" },
		LookPath: func(n string) (string, error) {
			if n == "apt-get" || n == "dpkg-deb" {
				return "/usr/bin/" + n, nil
			}
			return "", exec.ErrNotFound
		},
		ReadFile: func(p string) ([]byte, error) {
			if p == "/etc/os-release" {
				return []byte("ID=ubuntu\nID_LIKE=debian\n"), nil
			}
			return nil, errors.New("no")
		},
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		Glob:   func(pattern string) []string { m, _ := filepath.Glob(pattern); return m },
		Output: func(name string, args ...string) (string, error) {
			if name == "ldd" {
				return ldd(), nil
			}
			return "Google Chrome for Testing 156.0.1", nil
		},
		Run: func(_ context.Context, cmd string, _ io.Writer) error {
			cmds = append(cmds, cmd)
			if m := download.FindStringSubmatch(cmd); m != nil {
				// Пакет под именем Debian существует только с суффиксом t64 (Ubuntu 24.04+),
				// шрифты и остальные — под своим именем.
				if m[2] == "libatk1.0-0" {
					return errors.New("E: Can't select candidate version")
				}
				return os.WriteFile(filepath.Join(m[1], m[2]+".deb"), []byte(m[2]), 0o644)
			}
			if m := extract.FindStringSubmatch(cmd); m != nil {
				pkg := strings.TrimSuffix(filepath.Base(m[1]), ".deb")
				dir := filepath.Join(m[2], "usr/lib/x86_64-linux-gnu")
				if strings.HasPrefix(pkg, "fonts-") {
					dir = filepath.Join(m[2], "usr/share/fonts/truetype/noto")
					os.MkdirAll(dir, 0o755)
					return os.WriteFile(filepath.Join(dir, pkg+".ttf"), []byte("ttf"), 0o644)
				}
				os.MkdirAll(dir, 0o755)
				for name, lib := range map[string]string{"libatk1.0-0t64": "libatk-1.0.so.0", "libcups2": "libcups.so.2", "libavahi-client3": "libavahi-client.so.3"} {
					if name == pkg {
						// Как в настоящем пакете: файл с полной версией и ссылка на него.
						if err := os.WriteFile(filepath.Join(dir, lib+".0.1"), []byte(lib), 0o644); err != nil {
							return err
						}
						return os.Symlink(lib+".0.1", filepath.Join(dir, lib))
					}
				}
				return nil
			}
			return errors.New("unexpected command " + cmd)
		}}
	return s, &cmds
}

func TestInstallUserLibraries(t *testing.T) {
	data := t.TempDir()
	chrome := filepath.Join(data, "chrome", "156.0.1", "chrome-linux64", "chrome")
	os.MkdirAll(filepath.Dir(chrome), 0o755)
	os.WriteFile(chrome, []byte("ELF"), 0o755)
	// libcups тянет avahi — второй круг.
	s, cmds := debianSys(t, data, map[string][]string{"libatk-1.0.so.0": nil, "libcups.so.2": {"libavahi-client.so.3"}})
	c := CheckBrowser(s)
	if c.OK || c.Sandbox != "ok" || strings.Join(c.Libraries, ",") != "libatk-1.0.so.0,libcups.so.2" && strings.Join(c.Libraries, ",") != "libcups.so.2,libatk-1.0.so.0" {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(strings.Join(c.Notes, "\n"), "gate4ai browser setup --user puts the libraries and fonts into") {
		t.Fatal(c.Notes)
	}
	var out strings.Builder
	left, err := InstallUserLibraries(context.Background(), s, data, chrome, &out)
	if err != nil || len(left) > 0 {
		t.Fatal(err, left, out.String(), *cmds)
	}
	all := strings.Join(*cmds, "\n")
	for _, want := range []string{"apt-get download 'libatk1.0-0'", "apt-get download 'libatk1.0-0t64'", "apt-get download 'libcups2'", "apt-get download 'libavahi-client3'"} {
		if !strings.Contains(all, want) {
			t.Fatal(want, all)
		}
	}
	if strings.Contains(all, "sudo") {
		t.Fatal("no root", all)
	}
	lib := UserLibDir(data)
	for _, f := range []string{"libatk-1.0.so.0", "libatk-1.0.so.0.0.1", "libcups.so.2", "libavahi-client.so.3"} {
		if _, err := os.Lstat(filepath.Join(lib, f)); err != nil {
			t.Fatal(f, err)
		}
	}
	if target, _ := os.Readlink(filepath.Join(lib, "libatk-1.0.so.0")); target != "libatk-1.0.so.0.0.1" {
		t.Fatal("symlink", target)
	}
	// Временных каталогов загрузки не осталось.
	if m, _ := filepath.Glob(filepath.Join(data, "chrome", "download-*")); len(m) > 0 {
		t.Fatal(m)
	}
	// Повторная проверка: библиотеки найдены через LD_LIBRARY_PATH, lib_dir назван.
	c = CheckBrowser(s)
	if !c.OK || c.LibDir != lib || len(c.Libraries) > 0 {
		t.Fatalf("%+v", c)
	}
	// Окружение запуска Chrome.
	env := UserChromeEnv(data, func(k string) string { return map[string]string{"LD_LIBRARY_PATH": "/opt/x"}[k] })
	if strings.Join(env, " ") != "LD_LIBRARY_PATH="+lib+":/opt/x" {
		t.Fatal(env)
	}
	// Шрифты: в каталог данных, свой fonts.conf, проверка их считает.
	n, err := InstallUserFonts(context.Background(), s, data, &out)
	if err != nil || n != 2 {
		t.Fatal(n, err, out.String())
	}
	conf, _ := os.ReadFile(UserFontsConf(data))
	if !strings.Contains(string(conf), "<dir>"+UserFontsDir(data)+"</dir>") {
		t.Fatal(string(conf))
	}
	// Настоящий XML: fontconfig не читает файл с «--» внутри комментария.
	if err := xml.Unmarshal(conf, new(struct{})); err != nil {
		t.Fatal(err)
	}
	if c = CheckBrowser(s); c.Fonts != 2 {
		t.Fatalf("%+v", c)
	}
	env = UserChromeEnv(data, func(string) string { return "" })
	if len(env) != 2 || !strings.HasPrefix(env[1], "FONTCONFIG_FILE=") {
		t.Fatal(env)
	}
}

func TestInstallUserLibrariesUnknownPackage(t *testing.T) {
	data := t.TempDir()
	s, _ := debianSys(t, data, map[string][]string{"libfoo.so.9": nil})
	left, err := InstallUserLibraries(context.Background(), s, data, "/x/chrome", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "libfoo.so.9") || strings.Join(left, ",") != "libfoo.so.9" {
		t.Fatal(left, err)
	}
	// Не Debian: без root никак.
	s.ReadFile = func(string) ([]byte, error) { return []byte("ID=fedora\n"), nil }
	if UserLibrariesAvailable(s) {
		t.Fatal("fedora")
	}
	if _, err := InstallUserLibraries(context.Background(), s, data, "/x/chrome", io.Discard); err == nil || !strings.Contains(err.Error(), "Debian and Ubuntu") {
		t.Fatal(err)
	}
}

func TestSandboxBlocked(t *testing.T) {
	sysOf := func(files map[string]string) Sys {
		return Sys{GOOS: "linux", ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no")
		}}
	}
	apparmor := map[string]string{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1\n"}
	for _, c := range []struct {
		name   string
		files  map[string]string
		chrome string
		goos   string
		want   bool
	}{
		{"ubuntu 24+, Chrome for Testing in the data folder", apparmor, "/home/u/.local/state/gate4ai/chrome/156/chrome-linux64/chrome", "linux", true},
		{"ubuntu 24+, playwright cache", apparmor, "/home/u/.cache/ms-playwright/chromium-1/chrome-linux/chrome", "linux", true},
		{"ubuntu 24+, Google package has its AppArmor profile", apparmor, "/opt/google/chrome/chrome", "linux", false},
		{"ubuntu 24+, distribution chromium", apparmor, "/usr/lib/chromium/chromium", "linux", false},
		{"ubuntu 24+, snap", apparmor, "/snap/bin/chromium", "linux", false},
		{"ubuntu 24+, restriction off", map[string]string{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "0"}, "/home/u/chrome/chrome", "linux", false},
		{"debian: no sysctl", nil, "/home/u/chrome/chrome", "linux", false},
		{"userns clone disabled", map[string]string{"/proc/sys/kernel/unprivileged_userns_clone": "0"}, "/home/u/chrome/chrome", "linux", true},
		{"macOS", apparmor, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "darwin", false},
		{"no chrome", apparmor, "", "linux", false},
	} {
		s := sysOf(c.files)
		s.GOOS = c.goos
		if got := SandboxBlocked(s, c.chrome); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}
