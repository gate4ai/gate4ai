// SPDX-License-Identifier: AGPL-3.0-only

// Package browserenv — что нужно браузеру gate4ai на этом компьютере: Chrome
// (обязателен), виртуальный экран Xvfb (Linux без графики: рекомендуется,
// без него Chrome работает headless) и шрифты (иначе на страницах
// «квадраты»), и команды установки недостающего под ОС и дистрибутив.
// Пользуются gate4ai browser setup, gate4ai doctor и Web UI («Браузер»).
// Без root: Chrome for Testing (userchrome.go), его библиотеки и шрифты на
// Debian / Ubuntu и состояние песочницы (userlibs.go).
package browserenv

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gate4ai/gate4ai/internal/platform/paths"
)

// Sys — окружение проверки (подменяется в тестах).
type Sys struct {
	GOOS, GOARCH string
	Getenv       func(string) string
	LookPath     func(string) (string, error)
	ReadFile     func(string) ([]byte, error)
	Exists       func(string) bool
	Glob         func(pattern string) []string                                   // Chrome из кешей Playwright и Puppeteer
	Output       func(name string, args ...string) (string, error)               // версия Chrome, fc-list, ldd
	OutputEnv    func(env []string, name string, args ...string) (string, error) // то же с переменными окружения (ldd с LD_LIBRARY_PATH; nil — Output)
	Run          func(ctx context.Context, cmd string, out io.Writer) error
	Root         bool
	Home         string // домашний каталог (кеши Playwright, Puppeteer, flatpak)
	DataDir      string // каталог данных gate4ai: Chrome for Testing в <DataDir>/chrome (gate4ai browser setup --user)
}

func System() Sys {
	return Sys{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Getenv: os.Getenv, LookPath: exec.LookPath, ReadFile: os.ReadFile,
		Exists:  func(p string) bool { _, err := os.Stat(p); return err == nil },
		Glob:    func(pattern string) []string { m, _ := filepath.Glob(pattern); return m },
		Home:    home(),
		DataDir: paths.Home(),
		Output: func(name string, args ...string) (string, error) {
			b, err := exec.Command(name, args...).Output()
			return string(b), err
		},
		OutputEnv: func(env []string, name string, args ...string) (string, error) {
			c := exec.Command(name, args...)
			c.Env = append(os.Environ(), env...)
			b, err := c.Output()
			return string(b), err
		},
		Run: func(ctx context.Context, cmd string, out io.Writer) error {
			c := exec.CommandContext(ctx, "sh", "-c", cmd)
			if runtime.GOOS == "windows" {
				c = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", cmd)
			}
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, out, out
			return c.Run()
		},
		Root: os.Geteuid() == 0}
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

// BrowserCheck — результат проверки.
type BrowserCheck struct {
	OS        string   `json:"os"`
	Distro    string   `json:"distro,omitempty"`
	Chrome    string   `json:"chrome,omitempty"` // путь ("" — не найден)
	Version   string   `json:"chrome_version,omitempty"`
	Screen    bool     `json:"screen"`         // графический сеанс
	Xvfb      string   `json:"xvfb,omitempty"` // путь к Xvfb
	Display   string   `json:"display"`        // режим, в котором запустится браузер: host | virtual | headless | none
	Fonts     int      `json:"fonts,omitempty"`
	Libraries []string `json:"missing_libraries,omitempty"` // разделяемые библиотеки, которых Chrome не находит (ldd: not found)
	LibDir    string   `json:"lib_dir,omitempty"`           // библиотеки Chrome из каталога данных (gate4ai browser setup --user без root)
	Sandbox   string   `json:"sandbox,omitempty"`           // песочница Chrome: ok | blocked (ядро запрещает user namespaces этому Chrome: он запустится с --no-sandbox)
	Missing   []string `json:"missing"`                     // chrome, libraries, xvfb, fonts
	Needs     []Need   `json:"needs"`                       // то же подробно: зачем, обязательно ли, пакеты и команда
	Commands  []string `json:"commands"`                    // как поставить недостающее (все команды, по порядку)
	NeedsRoot bool     `json:"needs_root"`                  // команды требуют root (sudo)
	Summary   string   `json:"summary,omitempty"`           // одной фразой: что поставить, чтобы Chrome работал
	Notes     []string `json:"notes,omitempty"`
	OK        bool     `json:"ok"` // браузер работает (Chrome есть и все его библиотеки на месте)
}

// Need — чего не хватает браузеру: id (chrome, libraries, xvfb, fonts), зачем,
// обязательно ли для запуска Chrome, пакеты под этот дистрибутив и команда.
type Need struct {
	ID       string   `json:"id"`
	Required bool     `json:"required"`
	Why      string   `json:"why"`
	Packages []string `json:"packages,omitempty"`
	Command  string   `json:"command,omitempty"`
}

// ChromeCandidates — где искать Chrome (те же места, что у пакета browser):
// GATE4AI_CHROME, имена в PATH, snap и flatpak, кеши Playwright и Puppeteer
// (на сервере с OpenClaw или Playwright Chromium нередко уже стоит — без root).
// Записи с «*» — шаблоны для Glob.
func ChromeCandidates(s Sys) []string {
	var out []string
	if p := s.Getenv("GATE4AI_CHROME"); p != "" {
		out = append(out, p)
	}
	if s.DataDir != "" {
		out = append(out, userChromeGlob(s.GOOS, s.DataDir)) // gate4ai browser setup --user
	}
	switch s.GOOS {
	case "darwin":
		return append(out, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium",
			filepath.Join(s.Home, ".cache/ms-playwright/chromium-*/chrome-mac*/Chromium.app/Contents/MacOS/Chromium"),
			filepath.Join(s.Home, ".cache/puppeteer/chrome/mac*/chrome-mac*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"))
	case "windows":
		for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
			if d := s.Getenv(env); d != "" {
				out = append(out, filepath.Join(d, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
		if d := s.Getenv("LOCALAPPDATA"); d != "" {
			out = append(out, filepath.Join(d, "ms-playwright", "chromium-*", "chrome-win*", "chrome.exe"))
		}
		return out
	}
	out = append(out, "google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		"/snap/bin/chromium", "/var/lib/flatpak/exports/bin/com.google.Chrome", "/var/lib/flatpak/exports/bin/org.chromium.Chromium")
	if s.Home != "" {
		out = append(out, filepath.Join(s.Home, ".local/share/flatpak/exports/bin/com.google.Chrome"), filepath.Join(s.Home, ".local/share/flatpak/exports/bin/org.chromium.Chromium"),
			filepath.Join(s.Home, ".cache/ms-playwright/chromium-*/chrome-linux*/chrome"),
			filepath.Join(s.Home, ".cache/puppeteer/chrome/linux-*/chrome-linux*/chrome"))
	}
	return out
}

// FindChrome — путь к Chrome на этом компьютере ("" — не найден).
func FindChrome(s Sys) string {
	for _, p := range ChromeCandidates(s) {
		if strings.Contains(p, "*") {
			if s.Glob == nil {
				continue
			}
			if m := s.Glob(p); len(m) > 0 {
				return m[len(m)-1] // самая новая версия — последняя по имени
			}
			continue
		}
		if !strings.ContainsAny(p, `/\`) {
			if path, err := s.LookPath(p); err == nil {
				return path
			}
			continue
		}
		if s.Exists(p) {
			return p
		}
	}
	return ""
}

// osRelease — ID и ID_LIKE из /etc/os-release.
func osRelease(s Sys) (id string, like []string) {
	b, err := s.ReadFile("/etc/os-release")
	if err != nil {
		return "", nil
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = v
		case "ID_LIKE":
			like = strings.Fields(v)
		}
	}
	return id, like
}

// family — семейство дистрибутива: debian, fedora, suse, arch, alpine ("" — неизвестно).
func family(id string, like []string) string {
	for _, x := range append([]string{id}, like...) {
		switch x {
		case "debian", "ubuntu":
			return "debian"
		case "fedora", "rhel", "centos", "rocky", "almalinux":
			return "fedora"
		case "suse", "opensuse", "opensuse-leap", "opensuse-tumbleweed", "sles":
			return "suse"
		case "arch", "manjaro":
			return "arch"
		case "alpine":
			return "alpine"
		}
	}
	return ""
}

// CheckBrowser — что есть и чего не хватает браузеру.
func CheckBrowser(s Sys) BrowserCheck {
	c := BrowserCheck{OS: s.GOOS, Missing: []string{}, Commands: []string{}}
	c.Chrome = FindChrome(s)
	if c.Chrome != "" && s.GOOS != "windows" {
		if v, err := s.Output(c.Chrome, "--version"); err == nil {
			c.Version = strings.TrimSpace(v)
		}
	}
	c.Screen = s.GOOS == "darwin" || s.GOOS == "windows" || s.Getenv("DISPLAY") != "" || s.Getenv("WAYLAND_DISPLAY") != ""
	if p, err := s.LookPath("Xvfb"); err == nil {
		c.Xvfb = p
	}
	switch {
	case c.Chrome == "":
		c.Display = "none"
	case c.Screen:
		c.Display = "host"
	case c.Xvfb != "":
		c.Display = "virtual"
	default:
		c.Display = "headless"
	}
	linux := s.GOOS != "darwin" && s.GOOS != "windows"
	if c.Chrome == "" {
		c.Missing = append(c.Missing, "chrome")
	} else if linux {
		// Chromium из кеша Playwright или скачанный пакет не тянет зависимости:
		// без системных библиотек он не запустится — ldd называет их точно
		// (библиотеки из каталога данных — через LD_LIBRARY_PATH, как при запуске).
		if env := userLibEnv(s); len(env) > 0 {
			c.LibDir = UserLibDir(s.DataDir)
		}
		if c.Libraries = missingLibraries(s, c.Chrome); len(c.Libraries) > 0 {
			c.Missing = append(c.Missing, "libraries")
		}
		c.Sandbox = "ok"
		if SandboxBlocked(s, c.Chrome) {
			c.Sandbox = "blocked"
		}
	}
	if linux && !c.Screen && c.Xvfb == "" {
		c.Missing = append(c.Missing, "xvfb")
	}
	if linux && !c.Screen {
		if out, err := s.Output("fc-list"); err == nil {
			c.Fonts = len(strings.Split(strings.TrimSpace(out), "\n"))
			if strings.TrimSpace(out) == "" {
				c.Fonts = 0
			}
		}
		if n := userFontCount(s); n > c.Fonts {
			c.Fonts = n // шрифты в каталоге данных: fc-list их не видит без FONTCONFIG_FILE
		}
		if c.Fonts < 5 {
			c.Missing = append(c.Missing, "fonts")
		}
	}
	c.OK = c.Chrome != "" && len(c.Libraries) == 0
	c.Needs, c.Commands, c.Notes, c.Distro, c.NeedsRoot = planInstall(s, c.Missing, c.Libraries)
	if c.Commands == nil {
		c.Commands = []string{}
	}
	if c.Needs == nil {
		c.Needs = []Need{}
	}
	c.Summary = summary(c.Needs)
	return c
}

// summary — одной фразой, что поставить: сначала обязательное для запуска
// Chrome, затем рекомендуемое. Её печатает установщик и повторяет агент.
func summary(needs []Need) string {
	var req, rec []string
	for _, n := range needs {
		what := strings.Join(n.Packages, " ")
		if what == "" {
			what = n.ID
		}
		if n.Required {
			req = append(req, what)
		} else {
			rec = append(rec, what+" ("+n.Why+")")
		}
	}
	var parts []string
	if len(req) > 0 {
		parts = append(parts, "To run Chrome, install: "+strings.Join(req, ", ")+".")
	}
	if len(rec) > 0 {
		parts = append(parts, "Recommended: "+strings.Join(rec, "; ")+".")
	}
	return strings.Join(parts, " ")
}

// missingLibraries — разделяемые библиотеки, которых не находит динамический
// загрузчик (`ldd <chrome>`: «=> not found»). Обёртки (google-chrome — скрипт)
// разворачиваются до настоящего бинарника.
func missingLibraries(s Sys, chrome string) []string {
	bin := chrome
	if b, err := s.ReadFile(chrome); err == nil && strings.HasPrefix(string(b), "#!") {
		bin = ""
		for _, real := range []string{"/opt/google/chrome/chrome", "/usr/lib/chromium/chromium", "/usr/lib/chromium-browser/chromium-browser", "/usr/lib64/chromium-browser/chromium-browser"} {
			if s.Exists(real) {
				bin = real
				break
			}
		}
		if bin == "" {
			return nil
		}
	}
	var out string
	var err error
	if env := userLibEnv(s); len(env) > 0 && s.OutputEnv != nil {
		out, err = s.OutputEnv(env, "ldd", bin)
	} else {
		out, err = s.Output("ldd", bin)
	}
	if err != nil && out == "" {
		return nil
	}
	var libs []string
	for _, l := range strings.Split(out, "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(l), " => not found"); ok && name != "" && !strings.ContainsAny(name, " \t") {
			libs = append(libs, name)
		}
	}
	return libs
}

// libPackages — пакет дистрибутива по имени библиотеки (soname без версии)
// для семейств debian, fedora, suse, arch, alpine: зависимости Chrome, которых
// обычно нет на сервере без графики.
var libPackages = map[string]map[string]string{
	"libnss3.so":           {"debian": "libnss3", "fedora": "nss", "suse": "mozilla-nss", "arch": "nss", "alpine": "nss"},
	"libnssutil3.so":       {"debian": "libnss3", "fedora": "nss", "suse": "mozilla-nss", "arch": "nss", "alpine": "nss"},
	"libsmime3.so":         {"debian": "libnss3", "fedora": "nss", "suse": "mozilla-nss", "arch": "nss", "alpine": "nss"},
	"libnspr4.so":          {"debian": "libnspr4", "fedora": "nspr", "suse": "mozilla-nspr", "arch": "nspr", "alpine": "nspr"},
	"libatk-1.0.so":        {"debian": "libatk1.0-0", "fedora": "atk", "suse": "libatk-1_0-0", "arch": "at-spi2-core", "alpine": "at-spi2-core"},
	"libatk-bridge-2.0.so": {"debian": "libatk-bridge2.0-0", "fedora": "at-spi2-atk", "suse": "libatk-bridge-2_0-0", "arch": "at-spi2-core", "alpine": "at-spi2-core"},
	"libatspi.so":          {"debian": "libatspi2.0-0", "fedora": "at-spi2-core", "suse": "libatspi0", "arch": "at-spi2-core", "alpine": "at-spi2-core"},
	"libcups.so":           {"debian": "libcups2", "fedora": "cups-libs", "suse": "libcups2", "arch": "libcups", "alpine": "cups-libs"},
	"libdrm.so":            {"debian": "libdrm2", "fedora": "libdrm", "suse": "libdrm2", "arch": "libdrm", "alpine": "libdrm"},
	"libxkbcommon.so":      {"debian": "libxkbcommon0", "fedora": "libxkbcommon", "suse": "libxkbcommon0", "arch": "libxkbcommon", "alpine": "libxkbcommon"},
	"libgbm.so":            {"debian": "libgbm1", "fedora": "mesa-libgbm", "suse": "libgbm1", "arch": "mesa", "alpine": "mesa-gbm"},
	"libasound.so":         {"debian": "libasound2", "fedora": "alsa-lib", "suse": "libasound2", "arch": "alsa-lib", "alpine": "alsa-lib"},
	"libpango-1.0.so":      {"debian": "libpango-1.0-0", "fedora": "pango", "suse": "libpango-1_0-0", "arch": "pango", "alpine": "pango"},
	"libpangocairo-1.0.so": {"debian": "libpangocairo-1.0-0", "fedora": "pango", "suse": "libpango-1_0-0", "arch": "pango", "alpine": "pango"},
	"libcairo.so":          {"debian": "libcairo2", "fedora": "cairo", "suse": "libcairo2", "arch": "cairo", "alpine": "cairo"},
	"libgtk-3.so":          {"debian": "libgtk-3-0", "fedora": "gtk3", "suse": "libgtk-3-0", "arch": "gtk3", "alpine": "gtk+3.0"},
	"libgdk_pixbuf-2.0.so": {"debian": "libgdk-pixbuf-2.0-0", "fedora": "gdk-pixbuf2", "suse": "libgdk_pixbuf-2_0-0", "arch": "gdk-pixbuf2", "alpine": "gdk-pixbuf"},
	"libglib-2.0.so":       {"debian": "libglib2.0-0", "fedora": "glib2", "suse": "libglib-2_0-0", "arch": "glib2", "alpine": "glib"},
	"libgobject-2.0.so":    {"debian": "libglib2.0-0", "fedora": "glib2", "suse": "libgobject-2_0-0", "arch": "glib2", "alpine": "glib"},
	"libgio-2.0.so":        {"debian": "libglib2.0-0", "fedora": "glib2", "suse": "libgio-2_0-0", "arch": "glib2", "alpine": "glib"},
	"libdbus-1.so":         {"debian": "libdbus-1-3", "fedora": "dbus-libs", "suse": "libdbus-1-3", "arch": "dbus", "alpine": "dbus-libs"},
	"libexpat.so":          {"debian": "libexpat1", "fedora": "expat", "suse": "libexpat1", "arch": "expat", "alpine": "expat"},
	"libudev.so":           {"debian": "libudev1", "fedora": "systemd-libs", "suse": "libudev1", "arch": "systemd-libs", "alpine": "eudev-libs"},
	"libX11.so":            {"debian": "libx11-6", "fedora": "libX11", "suse": "libX11-6", "arch": "libx11", "alpine": "libx11"},
	"libX11-xcb.so":        {"debian": "libx11-xcb1", "fedora": "libX11-xcb", "suse": "libX11-xcb1", "arch": "libx11", "alpine": "libx11"},
	"libxcb.so":            {"debian": "libxcb1", "fedora": "libxcb", "suse": "libxcb1", "arch": "libxcb", "alpine": "libxcb"},
	"libXcomposite.so":     {"debian": "libxcomposite1", "fedora": "libXcomposite", "suse": "libXcomposite1", "arch": "libxcomposite", "alpine": "libxcomposite"},
	"libXdamage.so":        {"debian": "libxdamage1", "fedora": "libXdamage", "suse": "libXdamage1", "arch": "libxdamage", "alpine": "libxdamage"},
	"libXext.so":           {"debian": "libxext6", "fedora": "libXext", "suse": "libXext6", "arch": "libxext", "alpine": "libxext"},
	"libXfixes.so":         {"debian": "libxfixes3", "fedora": "libXfixes", "suse": "libXfixes3", "arch": "libxfixes", "alpine": "libxfixes"},
	"libXrandr.so":         {"debian": "libxrandr2", "fedora": "libXrandr", "suse": "libXrandr2", "arch": "libxrandr", "alpine": "libxrandr"},
	"libXrender.so":        {"debian": "libxrender1", "fedora": "libXrender", "suse": "libXrender1", "arch": "libxrender", "alpine": "libxrender"},
	"libXi.so":             {"debian": "libxi6", "fedora": "libXi", "suse": "libXi6", "arch": "libxi", "alpine": "libxi"},
	"libXtst.so":           {"debian": "libxtst6", "fedora": "libXtst", "suse": "libXtst6", "arch": "libxtst", "alpine": "libxtst"},
	"libXcursor.so":        {"debian": "libxcursor1", "fedora": "libXcursor", "suse": "libXcursor1", "arch": "libxcursor", "alpine": "libxcursor"},
	"libXss.so":            {"debian": "libxss1", "fedora": "libXScrnSaver", "suse": "libXss1", "arch": "libxss", "alpine": "libxscrnsaver"},
	"libfontconfig.so":     {"debian": "libfontconfig1", "fedora": "fontconfig", "suse": "fontconfig", "arch": "fontconfig", "alpine": "fontconfig"},
	"libfreetype.so":       {"debian": "libfreetype6", "fedora": "freetype", "suse": "libfreetype6", "arch": "freetype2", "alpine": "freetype"},
	"libharfbuzz.so":       {"debian": "libharfbuzz0b", "fedora": "harfbuzz", "suse": "libharfbuzz0", "arch": "harfbuzz", "alpine": "harfbuzz"},
	"libstdc++.so":         {"debian": "libstdc++6", "fedora": "libstdc++", "suse": "libstdc++6", "arch": "gcc-libs", "alpine": "libstdc++"},
	"libgcc_s.so":          {"debian": "libgcc-s1", "fedora": "libgcc", "suse": "libgcc_s1", "arch": "gcc-libs", "alpine": "libgcc"},
	"libuuid.so":           {"debian": "libuuid1", "fedora": "libuuid", "suse": "libuuid1", "arch": "util-linux-libs", "alpine": "libuuid"},
	"libxshmfence.so":      {"debian": "libxshmfence1", "fedora": "libxshmfence", "suse": "libxshmfence1", "arch": "libxshmfence", "alpine": "libxshmfence"},
	"libwayland-client.so": {"debian": "libwayland-client0", "fedora": "libwayland-client", "suse": "libwayland-client0", "arch": "wayland", "alpine": "wayland-libs-client"},
	"libgraphene-1.0.so":   {"debian": "libgraphene-1.0-0", "fedora": "graphene", "suse": "libgraphene-1_0-0", "arch": "graphene", "alpine": "graphene"},
	"libepoxy.so":          {"debian": "libepoxy0", "fedora": "libepoxy", "suse": "libepoxy0", "arch": "libepoxy", "alpine": "libepoxy"},
	"libgstreamer-1.0.so":  {"debian": "libgstreamer1.0-0", "fedora": "gstreamer1", "suse": "libgstreamer-1_0-0", "arch": "gstreamer", "alpine": "gstreamer"},
	// Зависимости библиотек выше — всплывают, когда библиотеки кладутся в каталог
	// данных без root (InstallUserLibraries), на сервере без графики их тоже нет.
	"libavahi-client.so":   {"debian": "libavahi-client3", "fedora": "avahi-libs", "suse": "libavahi-client3", "arch": "avahi", "alpine": "avahi-libs"},
	"libavahi-common.so":   {"debian": "libavahi-common3", "fedora": "avahi-libs", "suse": "libavahi-common3", "arch": "avahi", "alpine": "avahi-libs"},
	"libxcb-render.so":     {"debian": "libxcb-render0", "fedora": "libxcb", "suse": "libxcb-render0", "arch": "libxcb", "alpine": "libxcb"},
	"libxcb-shm.so":        {"debian": "libxcb-shm0", "fedora": "libxcb", "suse": "libxcb-shm0", "arch": "libxcb", "alpine": "libxcb"},
	"libpixman-1.so":       {"debian": "libpixman-1-0", "fedora": "pixman", "suse": "libpixman-1-0", "arch": "pixman", "alpine": "pixman"},
	"libthai.so":           {"debian": "libthai0", "fedora": "libthai", "suse": "libthai0", "arch": "libthai", "alpine": "libthai"},
	"libdatrie.so":         {"debian": "libdatrie1", "fedora": "libdatrie", "suse": "libdatrie1", "arch": "libdatrie", "alpine": "libdatrie"},
	"libgraphite2.so":      {"debian": "libgraphite2-3", "fedora": "graphite2", "suse": "libgraphite2-3", "arch": "graphite", "alpine": "graphite2"},
	"libXRes.so":           {"debian": "libxres1", "fedora": "libXres", "suse": "libXres1", "arch": "libxres", "alpine": "libxres"},
	"libpng16.so":          {"debian": "libpng16-16", "fedora": "libpng", "suse": "libpng16-16", "arch": "libpng", "alpine": "libpng"},
	"libfribidi.so":        {"debian": "libfribidi0", "fedora": "fribidi", "suse": "libfribidi0", "arch": "fribidi", "alpine": "fribidi"},
	"libbrotlidec.so":      {"debian": "libbrotli1", "fedora": "brotli", "suse": "libbrotlidec1", "arch": "brotli", "alpine": "brotli-libs"},
	"libbrotlicommon.so":   {"debian": "libbrotli1", "fedora": "brotli", "suse": "libbrotlicommon1", "arch": "brotli", "alpine": "brotli-libs"},
	"libbz2.so":            {"debian": "libbz2-1.0", "fedora": "bzip2-libs", "suse": "libbz2-1", "arch": "bzip2", "alpine": "libbz2"},
	"libxcb-dri3.so":       {"debian": "libxcb-dri3-0", "fedora": "libxcb", "suse": "libxcb-dri3-0", "arch": "libxcb", "alpine": "libxcb"},
	"libxcb-randr.so":      {"debian": "libxcb-randr0", "fedora": "libxcb", "suse": "libxcb-randr0", "arch": "libxcb", "alpine": "libxcb"},
	"libxcb-sync.so":       {"debian": "libxcb-sync1", "fedora": "libxcb", "suse": "libxcb-sync1", "arch": "libxcb", "alpine": "libxcb"},
	"libxcb-present.so":    {"debian": "libxcb-present0", "fedora": "libxcb", "suse": "libxcb-present0", "arch": "libxcb", "alpine": "libxcb"},
	"libxcb-xfixes.so":     {"debian": "libxcb-xfixes0", "fedora": "libxcb", "suse": "libxcb-xfixes0", "arch": "libxcb", "alpine": "libxcb"},
	"libwayland-server.so": {"debian": "libwayland-server0", "fedora": "libwayland-server", "suse": "libwayland-server0", "arch": "wayland", "alpine": "wayland-libs-server"},
}

// soname — имя библиотеки без версии: libnss3.so, libatk-1.0.so.0 → libatk-1.0.so.
func soname(lib string) string {
	if i := strings.Index(lib, ".so"); i >= 0 {
		return lib[:i+3]
	}
	return lib
}

// libraryPackages — пакеты семейства дистрибутива для недостающих библиотек
// (без повторов) и библиотеки, пакет которых неизвестен.
func libraryPackages(fam string, libs []string) (pkgs, unknown []string) {
	seen := map[string]bool{}
	for _, l := range libs {
		p := libPackages[soname(l)][fam]
		if p == "" {
			unknown = append(unknown, l)
			continue
		}
		if !seen[p] {
			seen[p] = true
			pkgs = append(pkgs, p)
		}
	}
	return pkgs, unknown
}

// planInstall — чего не хватает (Need: зачем, обязательно ли, пакеты и команда
// под ОС и дистрибутив) и команды установки по порядку. Обязательное для
// запуска — Chrome и его библиотеки; Xvfb и шрифты — рекомендации.
func planInstall(s Sys, missing, libs []string) (needs []Need, cmds, notes []string, distro string, root bool) {
	need := map[string]bool{}
	for _, m := range missing {
		need[m] = true
	}
	if len(need) == 0 {
		return nil, nil, nil, "", false
	}
	sudo := "sudo "
	if s.Root || s.GOOS == "windows" || s.GOOS == "darwin" {
		sudo = ""
	}
	terms := "Google Chrome comes from Google under the Google Chrome Terms of Service: https://www.google.com/chrome/terms/"
	chromeWhy := "the browser of agents does not work without it"
	switch s.GOOS {
	case "darwin":
		if need["chrome"] {
			n := Need{ID: "chrome", Required: true, Why: chromeWhy}
			if _, err := s.LookPath("brew"); err == nil {
				n.Command = "brew install --cask google-chrome"
				cmds = append(cmds, n.Command)
			} else {
				notes = append(notes, "install Google Chrome from https://www.google.com/chrome/")
			}
			needs = append(needs, n)
			notes = append(notes, terms)
		}
		return needs, cmds, notes, "", false
	case "windows":
		if need["chrome"] {
			n := Need{ID: "chrome", Required: true, Why: chromeWhy, Command: "winget install --id Google.Chrome --exact --scope user"}
			needs = append(needs, n)
			cmds = append(cmds, n.Command)
			notes = append(notes, terms)
		}
		return needs, cmds, notes, "", false
	}
	id, like := osRelease(s)
	fam := family(id, like)
	deb, rpm := map[string]string{"amd64": "amd64", "arm64": "arm64"}[s.GOARCH], map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[s.GOARCH]
	// install — команда менеджера пакетов семейства для списка пакетов.
	install := func(pkgs []string) string {
		if len(pkgs) == 0 {
			return ""
		}
		switch fam {
		case "debian":
			return sudo + "apt-get install -y --no-install-recommends " + strings.Join(pkgs, " ")
		case "fedora":
			return sudo + "dnf install -y " + strings.Join(pkgs, " ")
		case "suse":
			return sudo + "zypper install -y " + strings.Join(pkgs, " ")
		case "arch":
			return sudo + "pacman -S --needed --noconfirm " + strings.Join(pkgs, " ")
		case "alpine":
			return sudo + "apk add " + strings.Join(pkgs, " ")
		}
		return ""
	}
	if fam == "" {
		notes = append(notes, "unknown Linux distribution ("+id+"): install Google Chrome or Chromium, its libraries, Xvfb and Noto fonts with its package manager")
	}
	var pkgs []string // всё в одну команду менеджера пакетов
	if need["chrome"] {
		n := Need{ID: "chrome", Required: true, Why: chromeWhy}
		switch {
		case fam == "debian" && deb != "":
			n.Command = "curl -fsSLo /tmp/google-chrome.deb https://dl.google.com/linux/direct/google-chrome-stable_current_" + deb + ".deb && " +
				sudo + "apt-get install -y /tmp/google-chrome.deb && rm -f /tmp/google-chrome.deb"
			n.Packages = []string{"google-chrome-stable"}
			cmds = append(cmds, n.Command)
			notes = append(notes, terms)
		case (fam == "fedora" || fam == "suse") && rpm != "":
			n.Packages = []string{"https://dl.google.com/linux/direct/google-chrome-stable_current_" + rpm + ".rpm"}
			n.Command = install(n.Packages)
			pkgs = append(pkgs, n.Packages...)
			notes = append(notes, terms)
		case fam != "":
			n.Packages = []string{"chromium"}
			n.Command = install(n.Packages)
			pkgs = append(pkgs, n.Packages...)
		}
		needs = append(needs, n)
	}
	if need["libraries"] {
		n := Need{ID: "libraries", Required: true, Why: "shared libraries Chrome loads at start; without them it does not start"}
		var unknown []string
		n.Packages, unknown = libraryPackages(fam, libs)
		if len(unknown) > 0 {
			notes = append(notes, "libraries without a known package here: "+strings.Join(unknown, ", ")+" — find their packages with the package manager")
		}
		n.Command = install(n.Packages)
		pkgs = append(pkgs, n.Packages...)
		needs = append(needs, n)
	}
	if need["xvfb"] {
		n := Need{ID: "xvfb", Required: false, Why: "a virtual screen: sites see an ordinary Chrome, not headless, so bot protection asks for a captcha or blocks less often"}
		if p := map[string]string{"debian": "xvfb", "fedora": "xorg-x11-server-Xvfb", "suse": "xorg-x11-server-Xvfb", "arch": "xorg-server-xvfb", "alpine": "xvfb"}[fam]; p != "" {
			n.Packages = []string{p}
			n.Command = install(n.Packages)
			pkgs = append(pkgs, p)
		}
		needs = append(needs, n)
		if !need["chrome"] {
			notes = append(notes, "without Xvfb the browser works without a screen (headless): sites with bot protection ask for a captcha or block more often")
		}
	}
	if need["fonts"] {
		n := Need{ID: "fonts", Required: false, Why: "fonts: without them pages show boxes instead of letters"}
		if p := map[string][]string{"debian": {"fonts-noto-core", "fonts-noto-color-emoji"}, "fedora": {"google-noto-sans-fonts"}, "suse": {"noto-sans-fonts"}, "arch": {"noto-fonts"}, "alpine": {"font-noto"}}[fam]; len(p) > 0 {
			n.Packages = p
			n.Command = install(p)
			pkgs = append(pkgs, p...)
		}
		needs = append(needs, n)
	}
	if c := install(pkgs); c != "" {
		cmds = append(cmds, c)
	}
	if need["chrome"] && UserInstallAvailable(s) {
		n := "without root: gate4ai browser setup --user downloads Chrome for Testing into " + UserChromeDir(s.DataDir)
		if UserLibrariesAvailable(s) {
			n += " and puts its libraries and fonts next to it (apt-get download, no root)"
		} else {
			n += " (its system libraries still come from packages)"
		}
		notes = append(notes, n)
	} else if (need["libraries"] || need["fonts"]) && UserLibrariesAvailable(s) {
		notes = append(notes, "without root: gate4ai browser setup --user puts the libraries and fonts into "+UserChromeDir(s.DataDir)+" (apt-get download, no root)")
	}
	if sudo != "" && len(cmds) > 0 {
		root = true
		if _, err := s.LookPath("sudo"); err != nil {
			notes = append(notes, "sudo is not installed: run the commands as root")
		}
	}
	return needs, cmds, notes, fam, root
}
