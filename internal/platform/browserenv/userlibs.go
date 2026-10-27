// SPDX-License-Identifier: AGPL-3.0-only

package browserenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Библиотеки и шрифты Chrome без root. Chrome for Testing (и Chromium из кеша
// Playwright) не приносит системных библиотек, а без root их не поставить
// пакетами. На Debian и Ubuntu пакеты скачиваются и без root (apt-get download
// — только загрузка .deb из уже известных apt источников) и распаковываются
// (dpkg-deb -x) в <каталог данных>/chrome/lib; daemon запускает Chrome с
// LD_LIBRARY_PATH на этот каталог. Шрифты так же: fonts-noto в
// <каталог данных>/chrome/fonts со своим fonts.conf (FONTCONFIG_FILE) — на
// сервере без графики fontconfig часто нет вовсе.

// UserLibDir — куда кладутся библиотеки Chrome без root.
func UserLibDir(dataDir string) string { return filepath.Join(UserChromeDir(dataDir), "lib") }

// UserFontsDir — куда кладутся шрифты без root; в нём же fonts.conf.
func UserFontsDir(dataDir string) string { return filepath.Join(UserChromeDir(dataDir), "fonts") }

// UserFontsConf — конфигурация fontconfig для шрифтов из UserFontsDir.
func UserFontsConf(dataDir string) string { return filepath.Join(UserFontsDir(dataDir), "fonts.conf") }

// userFontPackages — шрифты Noto (как в команде для root): буквы и эмодзи.
var userFontPackages = []string{"fonts-noto-core", "fonts-noto-color-emoji"}

// UserChromeEnv — переменные окружения для запуска Chrome с библиотеками и
// шрифтами из каталога данных (пусто, если их там нет). LD_LIBRARY_PATH
// дополняет уже заданный; getenv — окружение процесса.
func UserChromeEnv(dataDir string, getenv func(string) string) []string {
	if dataDir == "" {
		return nil
	}
	var env []string
	if lib := UserLibDir(dataDir); hasFiles(lib, "*.so*") {
		p := lib
		if cur := getenv("LD_LIBRARY_PATH"); cur != "" {
			p += string(os.PathListSeparator) + cur
		}
		env = append(env, "LD_LIBRARY_PATH="+p)
	}
	if conf := UserFontsConf(dataDir); getenv("FONTCONFIG_FILE") == "" {
		if _, err := os.Stat(conf); err == nil {
			env = append(env, "FONTCONFIG_FILE="+conf)
		}
	}
	return env
}

func hasFiles(dir, pattern string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	return len(m) > 0
}

// userLibEnv — LD_LIBRARY_PATH для проверки ldd: библиотеки из каталога данных
// (nil — их нет).
func userLibEnv(s Sys) []string {
	if s.DataDir == "" || s.Glob == nil {
		return nil
	}
	if m := s.Glob(filepath.Join(UserLibDir(s.DataDir), "*.so*")); len(m) > 0 {
		p := UserLibDir(s.DataDir)
		if cur := s.Getenv("LD_LIBRARY_PATH"); cur != "" {
			p += string(os.PathListSeparator) + cur
		}
		return []string{"LD_LIBRARY_PATH=" + p}
	}
	return nil
}

// userFontCount — шрифты в каталоге данных (*.ttf, *.otf).
func userFontCount(s Sys) int {
	if s.DataDir == "" || s.Glob == nil {
		return 0
	}
	return len(s.Glob(filepath.Join(UserFontsDir(s.DataDir), "*.ttf"))) + len(s.Glob(filepath.Join(UserFontsDir(s.DataDir), "*.otf")))
}

// UserLibrariesAvailable — библиотеки и шрифты можно положить в каталог
// данных без root: Linux семейства debian с apt-get и dpkg-deb.
func UserLibrariesAvailable(s Sys) bool {
	if s.GOOS == "darwin" || s.GOOS == "windows" {
		return false
	}
	if id, like := osRelease(s); family(id, like) != "debian" {
		return false
	}
	for _, tool := range []string{"apt-get", "dpkg-deb"} {
		if _, err := s.LookPath(tool); err != nil {
			return false
		}
	}
	return true
}

// InstallUserLibraries скачивает пакеты недостающих библиотек Chrome (ldd)
// и кладёт их .so в UserLibDir(dataDir); зависимости этих библиотек —
// следующими кругами, пока ldd не перестанет находить пропуски. Возвращает
// библиотеки, которые положить не удалось (пакет неизвестен или не скачался).
func InstallUserLibraries(ctx context.Context, s Sys, dataDir, chrome string, out io.Writer) ([]string, error) {
	if !UserLibrariesAvailable(s) {
		return nil, errors.New("libraries without root are for Debian and Ubuntu (apt-get download and dpkg-deb); install them with the package manager")
	}
	lib := UserLibDir(dataDir)
	if err := os.MkdirAll(lib, 0o755); err != nil {
		return nil, err
	}
	tried := map[string]bool{}
	for round := 0; round < 8; round++ {
		missing := missingLibraries(s, chrome)
		if len(missing) == 0 {
			return nil, nil
		}
		pkgs, unknown := libraryPackages("debian", missing)
		var todo []string
		for _, p := range pkgs {
			if !tried[p] {
				tried[p] = true
				todo = append(todo, p)
			}
		}
		if len(todo) == 0 {
			if len(unknown) > 0 {
				return missing, fmt.Errorf("libraries without a known package: %s", strings.Join(unknown, ", "))
			}
			return missing, errors.New("the packages were unpacked, but Chrome still misses " + strings.Join(missing, ", "))
		}
		fmt.Fprintln(out, "Downloading packages into "+lib+": "+strings.Join(todo, " ")+" …")
		if err := unpackPackages(ctx, s, todo, lib, out); err != nil {
			return missing, err
		}
	}
	return missingLibraries(s, chrome), errors.New("too many rounds of dependencies")
}

// InstallUserFonts скачивает шрифты Noto в UserFontsDir(dataDir) и пишет
// рядом fonts.conf; возвращает число шрифтов.
func InstallUserFonts(ctx context.Context, s Sys, dataDir string, out io.Writer) (int, error) {
	if !UserLibrariesAvailable(s) {
		return 0, errors.New("fonts without root are for Debian and Ubuntu (apt-get download and dpkg-deb); install them with the package manager")
	}
	dir := UserFontsDir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	fmt.Fprintln(out, "Downloading fonts into "+dir+": "+strings.Join(userFontPackages, " ")+" …")
	if err := unpackPackages(ctx, s, userFontPackages, dir, out); err != nil {
		return 0, err
	}
	conf := "<?xml version=\"1.0\"?>\n<!DOCTYPE fontconfig SYSTEM \"fonts.dtd\">\n<!-- gate4ai: fonts for the browser of agents without root (gate4ai browser setup, user install); no double hyphen here: XML comments forbid it -->\n<fontconfig>\n" +
		"  <dir>" + dir + "</dir>\n  <dir>/usr/share/fonts</dir>\n  <dir>/usr/local/share/fonts</dir>\n  <dir prefix=\"xdg\">fonts</dir>\n" +
		"  <cachedir>" + filepath.Join(dir, "cache") + "</cachedir>\n  <include ignore_missing=\"yes\">/etc/fonts/conf.d</include>\n</fontconfig>\n"
	if err := os.WriteFile(UserFontsConf(dataDir), []byte(conf), 0o644); err != nil {
		return 0, err
	}
	n, _ := filepath.Glob(filepath.Join(dir, "*.ttf"))
	return len(n), nil
}

// unpackPackages скачивает пакеты (apt-get download, без root) во временный
// каталог, распаковывает каждый (dpkg-deb -x) и переносит из него
// библиотеки (*.so*) и шрифты (*.ttf, *.otf) в dst — плоско, вместе с
// символическими ссылками (libatk-1.0.so.0 → libatk-1.0.so.0.2…). Пакет
// без .deb под своим именем пробуется с суффиксом t64 (Ubuntu 24.04+ так
// переименовала libatk1.0-0, libcups2, libasound2 и другие).
func unpackPackages(ctx context.Context, s Sys, pkgs []string, dst string, out io.Writer) error {
	tmp, err := os.MkdirTemp(filepath.Dir(dst), "download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, p := range pkgs {
		if err := downloadDeb(ctx, s, tmp, p, out); err != nil {
			return err
		}
	}
	debs, _ := filepath.Glob(filepath.Join(tmp, "*.deb"))
	if len(debs) == 0 {
		return errors.New("no packages were downloaded (is the apt index present? run apt-get update as root once)")
	}
	for _, d := range debs {
		x := filepath.Join(tmp, "x")
		os.RemoveAll(x)
		if err := s.Run(ctx, "dpkg-deb -x "+shellQuote(d)+" "+shellQuote(x), out); err != nil {
			return fmt.Errorf("dpkg-deb -x %s: %w", filepath.Base(d), err)
		}
		if err := collectFiles(x, dst); err != nil {
			return err
		}
	}
	return nil
}

// downloadDeb — apt-get download пакета в dir: сначала под своим именем,
// затем с суффиксом t64.
func downloadDeb(ctx context.Context, s Sys, dir, pkg string, out io.Writer) error {
	before, _ := filepath.Glob(filepath.Join(dir, "*.deb"))
	names := []string{pkg}
	if !strings.HasSuffix(pkg, "t64") {
		names = append(names, pkg+"t64")
	}
	for i, name := range names {
		w := out
		if i < len(names)-1 {
			w = io.Discard // «no candidate» первой попытки — не ошибка, если есть запасное имя
		}
		if err := s.Run(ctx, "cd "+shellQuote(dir)+" && apt-get download "+shellQuote(name), w); err == nil {
			if after, _ := filepath.Glob(filepath.Join(dir, "*.deb")); len(after) > len(before) {
				return nil
			}
		}
	}
	return fmt.Errorf("apt-get download %s failed (no root is needed for it; the apt index may be missing — apt-get update as root once)", pkg)
}

// collectFiles переносит библиотеки и шрифты из распакованного пакета в dst.
func collectFiles(root, dst string) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		name := info.Name()
		isLib := strings.Contains(name, ".so") && strings.Contains(p, "/lib/")
		isFont := strings.HasSuffix(name, ".ttf") || strings.HasSuffix(name, ".otf")
		if !isLib && !isFont {
			return nil
		}
		target := filepath.Join(dst, name)
		os.Remove(target)
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(filepath.Base(link), target)
		}
		return copyFile(p, target, info.Mode().Perm()|0o644)
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// SandboxBlocked — песочница Chrome не поднимется: ядро запрещает
// непривилегированные user namespaces (Ubuntu 24.04+: AppArmor разрешает их
// только программам со своим профилем — Chrome из пакета Google его имеет,
// Chrome for Testing в каталоге данных и Chromium из кеша Playwright — нет).
// Без root выход один — --no-sandbox (настройка sandbox: auto делает это сама).
func SandboxBlocked(s Sys, chrome string) bool {
	if s.GOOS == "darwin" || s.GOOS == "windows" || chrome == "" {
		return false
	}
	for _, pre := range []string{"/usr/", "/opt/", "/snap/", "/var/lib/flatpak/"} {
		if strings.HasPrefix(chrome, pre) {
			return false // пакет дистрибутива или Google: свой профиль AppArmor или suid-песочница
		}
	}
	if b, err := s.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); err == nil && strings.TrimSpace(string(b)) == "1" {
		return true
	}
	if b, err := s.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil && strings.TrimSpace(string(b)) == "0" {
		return true
	}
	return false
}
