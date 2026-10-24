// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gate4ai/gate4ai/internal/sandbox"
)

// gate4ai browser setup — что нужно браузеру gate4ai на этом компьютере:
// Chrome (обязателен), виртуальный экран Xvfb (Linux без графики:
// рекомендуется, без него Chrome работает headless) и шрифты (иначе на
// страницах «квадраты»). Команды установки — под дистрибутив; ставятся только
// с согласия (вопрос в терминале или --yes): установщик gate4ai сам sudo не
// вызывает.

// setupSys — окружение проверки (подменяется в тестах).
type setupSys struct {
	GOOS, GOARCH string
	Getenv       func(string) string
	LookPath     func(string) (string, error)
	ReadFile     func(string) ([]byte, error)
	Exists       func(string) bool
	Output       func(name string, args ...string) (string, error) // версия Chrome, fc-list
	Run          func(ctx context.Context, cmd string, out io.Writer) error
	Root         bool
}

func systemSetup() setupSys {
	return setupSys{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Getenv: os.Getenv, LookPath: exec.LookPath, ReadFile: os.ReadFile,
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		Output: func(name string, args ...string) (string, error) {
			b, err := exec.Command(name, args...).Output()
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

// setupSysFn — окружение для команды (тесты подменяют).
var setupSysFn = systemSetup

// BrowserCheck — результат проверки.
type BrowserCheck struct {
	OS       string   `json:"os"`
	Distro   string   `json:"distro,omitempty"`
	Chrome   string   `json:"chrome,omitempty"` // путь ("" — не найден)
	Version  string   `json:"chrome_version,omitempty"`
	Screen   bool     `json:"screen"`         // графический сеанс
	Xvfb     string   `json:"xvfb,omitempty"` // путь к Xvfb
	Display  string   `json:"display"`        // режим, в котором запустится браузер: host | virtual | headless | none
	Fonts    int      `json:"fonts,omitempty"`
	Missing  []string `json:"missing"`  // chrome, xvfb, fonts
	Commands []string `json:"commands"` // как поставить недостающее
	Notes    []string `json:"notes,omitempty"`
	OK       bool     `json:"ok"` // браузер работает (Chrome есть)
}

// chromeCandidates — где искать Chrome (те же места, что у пакета browser).
func chromeCandidates(s setupSys) []string {
	switch s.GOOS {
	case "darwin":
		return []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
	case "windows":
		var out []string
		for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "LOCALAPPDATA"} {
			if d := s.Getenv(env); d != "" {
				out = append(out, filepath.Join(d, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
		return out
	}
	return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
}

// osRelease — ID и ID_LIKE из /etc/os-release.
func osRelease(s setupSys) (id string, like []string) {
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
func CheckBrowser(s setupSys) BrowserCheck {
	c := BrowserCheck{OS: s.GOOS, Missing: []string{}, Commands: []string{}}
	for _, p := range chromeCandidates(s) {
		if s.GOOS == "darwin" || s.GOOS == "windows" {
			if s.Exists(p) {
				c.Chrome = p
				break
			}
			continue
		}
		if path, err := s.LookPath(p); err == nil {
			c.Chrome = path
			break
		}
	}
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
	if c.Chrome == "" {
		c.Missing = append(c.Missing, "chrome")
	}
	linux := s.GOOS != "darwin" && s.GOOS != "windows"
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
		if c.Fonts < 5 {
			c.Missing = append(c.Missing, "fonts")
		}
	}
	c.OK = c.Chrome != ""
	c.Commands, c.Notes, c.Distro = planInstall(s, c.Missing)
	if c.Commands == nil {
		c.Commands = []string{}
	}
	return c
}

// planInstall — команды установки недостающего под ОС и дистрибутив.
func planInstall(s setupSys, missing []string) (cmds, notes []string, distro string) {
	need := map[string]bool{}
	for _, m := range missing {
		need[m] = true
	}
	if len(need) == 0 {
		return nil, nil, ""
	}
	sudo := "sudo "
	if s.Root || s.GOOS == "windows" || s.GOOS == "darwin" {
		sudo = ""
	}
	terms := "Google Chrome comes from Google under the Google Chrome Terms of Service: https://www.google.com/chrome/terms/"
	switch s.GOOS {
	case "darwin":
		if need["chrome"] {
			if _, err := s.LookPath("brew"); err == nil {
				cmds = append(cmds, "brew install --cask google-chrome")
			} else {
				notes = append(notes, "install Google Chrome from https://www.google.com/chrome/")
			}
			notes = append(notes, terms)
		}
		return cmds, notes, ""
	case "windows":
		if need["chrome"] {
			cmds = append(cmds, "winget install --id Google.Chrome --exact --scope user")
			notes = append(notes, terms)
		}
		return cmds, notes, ""
	}
	id, like := osRelease(s)
	fam := family(id, like)
	deb, rpm := map[string]string{"amd64": "amd64", "arm64": "arm64"}[s.GOARCH], map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[s.GOARCH]
	var pkgs []string
	switch fam {
	case "debian":
		if need["chrome"] && deb != "" {
			cmds = append(cmds, "curl -fsSLo /tmp/google-chrome.deb https://dl.google.com/linux/direct/google-chrome-stable_current_"+deb+".deb && "+
				sudo+"apt-get install -y /tmp/google-chrome.deb && rm -f /tmp/google-chrome.deb")
		} else if need["chrome"] {
			pkgs = append(pkgs, "chromium")
		}
		if need["xvfb"] {
			pkgs = append(pkgs, "xvfb")
		}
		if need["fonts"] {
			pkgs = append(pkgs, "fonts-noto-core", "fonts-noto-color-emoji")
		}
		if len(pkgs) > 0 {
			cmds = append(cmds, sudo+"apt-get install -y --no-install-recommends "+strings.Join(pkgs, " "))
		}
	case "fedora", "suse":
		tool := map[string]string{"fedora": "dnf", "suse": "zypper"}[fam]
		if need["chrome"] && rpm != "" {
			pkgs = append(pkgs, "https://dl.google.com/linux/direct/google-chrome-stable_current_"+rpm+".rpm")
		} else if need["chrome"] {
			pkgs = append(pkgs, "chromium")
		}
		if need["xvfb"] {
			pkgs = append(pkgs, "xorg-x11-server-Xvfb")
		}
		if need["fonts"] {
			pkgs = append(pkgs, map[string]string{"fedora": "google-noto-sans-fonts", "suse": "noto-sans-fonts"}[fam])
		}
		cmds = append(cmds, sudo+tool+" install -y "+strings.Join(pkgs, " "))
	case "arch":
		if need["chrome"] {
			pkgs = append(pkgs, "chromium")
		}
		if need["xvfb"] {
			pkgs = append(pkgs, "xorg-server-xvfb")
		}
		if need["fonts"] {
			pkgs = append(pkgs, "noto-fonts")
		}
		cmds = append(cmds, sudo+"pacman -S --needed --noconfirm "+strings.Join(pkgs, " "))
	case "alpine":
		if need["chrome"] {
			pkgs = append(pkgs, "chromium")
		}
		if need["xvfb"] {
			pkgs = append(pkgs, "xvfb")
		}
		if need["fonts"] {
			pkgs = append(pkgs, "font-noto")
		}
		cmds = append(cmds, sudo+"apk add "+strings.Join(pkgs, " "))
	default:
		notes = append(notes, "unknown Linux distribution ("+id+"): install Google Chrome or Chromium, Xvfb and Noto fonts with its package manager")
	}
	if need["chrome"] && (fam == "debian" && deb != "" || (fam == "fedora" || fam == "suse") && rpm != "") {
		notes = append(notes, terms)
	}
	if need["xvfb"] && !need["chrome"] {
		notes = append(notes, "without Xvfb the browser works without a screen (headless): sites with bot protection ask for a captcha or block more often")
	}
	if sudo != "" {
		if _, err := s.LookPath("sudo"); err != nil {
			notes = append(notes, "sudo is not installed: run the commands as root")
		}
	}
	return cmds, notes, fam
}

func (e *Env) browserSetup(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("gate4ai browser setup", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	yes := fs.Bool("yes", false, "install what is missing without asking (runs the commands, with sudo where needed)")
	dry := fs.Bool("dry-run", false, "only check and print the commands")
	asJSON := fs.Bool("json", false, "JSON output")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return ExitUsage
	}
	if *asJSON {
		e.ForceJSON = true
	}
	s := setupSysFn()
	c := CheckBrowser(s)
	e.printCheck(c)
	if len(c.Commands) == 0 || *dry || e.ForceJSON {
		e.out(c, nil)
		return setupExit(c)
	}
	install := *yes
	if !install && e.Interactive {
		fmt.Fprint(e.Stderr, "Install now? [y/N] ")
		line, _ := bufio.NewReader(e.Stdin).ReadString('\n')
		install = strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
	}
	if !install {
		e.say("Nothing installed. Run the commands above, or gate4ai browser setup --yes.")
		return setupExit(c)
	}
	for _, cmd := range c.Commands {
		fmt.Fprintln(e.Stderr, "+ "+cmd)
		if err := s.Run(ctx, cmd, e.Stderr); err != nil {
			fmt.Fprintln(e.Stderr, "gate4ai: the command failed: "+err.Error())
			return ExitError
		}
	}
	c = CheckBrowser(s)
	e.say("After installing:")
	e.printCheck(c)
	return setupExit(c)
}

// setupExit — 0: Chrome есть (Xvfb и шрифты — рекомендации), 1 — Chrome нет.
func setupExit(c BrowserCheck) int {
	if c.OK {
		return ExitOK
	}
	return ExitError
}

func (e *Env) printCheck(c BrowserCheck) {
	if e.ForceJSON {
		return
	}
	w := e.Stdout
	mark := func(ok bool) string { return map[bool]string{true: "ok  ", false: "--  "}[ok] }
	if c.Chrome != "" {
		fmt.Fprintln(w, mark(true)+"Chrome: "+c.Chrome+" "+c.Version)
	} else {
		fmt.Fprintln(w, mark(false)+"Chrome: not found — the browser does not work without it")
	}
	if c.OS != "darwin" && c.OS != "windows" {
		switch {
		case c.Screen:
			fmt.Fprintln(w, mark(true)+"Screen: this computer's")
		case c.Xvfb != "":
			fmt.Fprintln(w, mark(true)+"Screen: virtual (Xvfb "+c.Xvfb+")")
		default:
			fmt.Fprintln(w, mark(false)+"Screen: none and no Xvfb — the browser will run headless (recommended: Xvfb)")
		}
		if !c.Screen {
			fmt.Fprintln(w, mark(c.Fonts >= 5)+fmt.Sprintf("Fonts: %d", c.Fonts))
		}
	}
	if len(c.Commands) > 0 {
		fmt.Fprintln(w, "\nTo install what is missing:")
		for _, cmd := range c.Commands {
			fmt.Fprintln(w, "  "+cmd)
		}
	}
	for _, n := range c.Notes {
		fmt.Fprintln(w, "Note: "+n)
	}
}

// browserChecks — браузер в gate4ai doctor: Chrome обязателен (fail), экран
// и шрифты на сервере — предупреждения; режим запущенного браузера — у daemon.
func (e *Env) browserChecks(ctx context.Context) []sandbox.Check {
	c := CheckBrowser(setupSysFn())
	fix := "gate4ai browser setup"
	var out []sandbox.Check
	if c.Chrome == "" {
		out = append(out, sandbox.Check{ID: "browser_chrome", Status: "fail", Detail: "Chrome is not installed: the browser connector does not work", Fix: fix})
	} else {
		out = append(out, sandbox.Check{ID: "browser_chrome", Status: "ok", Detail: strings.TrimSpace(c.Chrome + " " + c.Version)})
	}
	switch c.Display {
	case "headless":
		out = append(out, sandbox.Check{ID: "browser_screen", Status: "warn", Detail: "no screen and no Xvfb: the browser runs headless, sites with bot protection ask for a captcha or block more often", Fix: fix})
	case "virtual":
		out = append(out, sandbox.Check{ID: "browser_screen", Status: "ok", Detail: "a virtual screen (Xvfb): the browser starts it itself"})
	}
	for _, m := range c.Missing {
		if m == "fonts" {
			out = append(out, sandbox.Check{ID: "browser_fonts", Status: "warn", Detail: "few fonts: pages show boxes instead of letters", Fix: fix})
		}
	}
	var st struct {
		Display string `json:"display"`
		Warning string `json:"warning"`
	}
	if err := e.Client().Do(ctx, "GET", "/v1/browser/sessions", nil, &st); err == nil && st.Display == "headless" && c.Display != "headless" {
		out = append(out, sandbox.Check{ID: "browser_running_headless", Status: "warn", Detail: "the running browser was started headless; it gets the virtual screen after a restart of the browser package", Fix: "gate4ai daemon restart"})
	}
	return out
}
