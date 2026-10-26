// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/gate4ai/gate4ai/internal/platform/browserenv"
	"github.com/gate4ai/gate4ai/internal/sandbox"
)

// gate4ai browser setup — проверка (browserenv) и установка недостающего:
// команды ставятся только с согласия (вопрос в терминале или --yes). Без
// терминала (--yes из установщика, запущенного агентом) sudo вызывается с -n:
// пароль спросить некому, и команда сразу отвечает, что нужен root, а не висит.

// setupSysFn — окружение для команды (тесты подменяют); userInstallFn —
// загрузка Chrome for Testing в каталог данных (без root).
var (
	setupSysFn    = browserenv.System
	userInstallFn = browserenv.InstallUserChrome
)

func (e *Env) browserSetup(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("gate4ai browser setup", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	yes := fs.Bool("yes", false, "install what is missing without asking (runs the commands, with sudo where needed)")
	dry := fs.Bool("dry-run", false, "only check and print the commands")
	user := fs.Bool("user", false, "no root: download Chrome for Testing into gate4ai's data folder (its system libraries still come from packages)")
	asJSON := fs.Bool("json", false, "JSON output")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return ExitUsage
	}
	if *asJSON {
		e.ForceJSON = true
	}
	s := setupSysFn()
	c := browserenv.CheckBrowser(s)
	if *user && c.Chrome == "" {
		if c = e.userChrome(ctx, s); c.Chrome == "" {
			return ExitError
		}
	}
	e.printCheck(c)
	if len(c.Commands) == 0 || *dry || !e.human() && !*yes {
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
	// Chrome нет, а root взять негде (не root, sudo без пароля не проходит):
	// сам бинарник ставится без root — Chrome for Testing в каталог данных;
	// библиотеки к нему всё равно из пакетов (команда ниже скажет, какие).
	if c.Chrome == "" && c.NeedsRoot && !s.Root && !canSudo(s) && browserenv.UserInstallAvailable(s) {
		if c = e.userChrome(ctx, s); c.Chrome == "" {
			return ExitError
		}
		e.printCheck(c)
		if len(c.Commands) == 0 {
			return setupExit(c)
		}
	}
	for _, cmd := range c.Commands {
		if !e.Interactive {
			cmd = strings.ReplaceAll(cmd, "sudo ", "sudo -n ")
		}
		fmt.Fprintln(e.Stderr, "+ "+cmd)
		if err := s.Run(ctx, cmd, e.Stderr); err != nil {
			msg := "gate4ai: the command failed: " + err.Error()
			if c.NeedsRoot && !s.Root && !e.Interactive {
				msg += "\ngate4ai: root is needed and sudo cannot ask for a password here. Run as root (or with sudo):\n  " + strings.Join(c.Commands, "\n  ")
			}
			fmt.Fprintln(e.Stderr, msg)
			return ExitError
		}
	}
	c = browserenv.CheckBrowser(s)
	e.say("After installing:")
	e.printCheck(c)
	return setupExit(c)
}

// userChrome ставит Chrome for Testing в каталог данных и проверяет заново.
func (e *Env) userChrome(ctx context.Context, s browserenv.Sys) browserenv.BrowserCheck {
	path, err := userInstallFn(ctx, s, s.DataDir, e.Stderr)
	if err != nil {
		fmt.Fprintln(e.Stderr, "gate4ai: Chrome for Testing: "+err.Error())
		return browserenv.BrowserCheck{}
	}
	fmt.Fprintln(e.Stderr, "Chrome for Testing: "+path)
	return browserenv.CheckBrowser(s)
}

// canSudo — sudo работает без пароля (или его не спросят).
func canSudo(s browserenv.Sys) bool {
	if _, err := s.LookPath("sudo"); err != nil {
		return false
	}
	_, err := s.Output("sudo", "-n", "true")
	return err == nil
}

// setupExit — 0: Chrome есть и запустится (Xvfb и шрифты — рекомендации),
// 1 — Chrome нет или не хватает его библиотек.
func setupExit(c browserenv.BrowserCheck) int {
	if c.OK {
		return ExitOK
	}
	return ExitError
}

func (e *Env) printCheck(c browserenv.BrowserCheck) {
	if !e.human() {
		return // JSON — e.out
	}
	w := e.Stdout
	mark := func(ok bool) string { return map[bool]string{true: "ok  ", false: "--  "}[ok] }
	switch {
	case c.Chrome == "":
		fmt.Fprintln(w, mark(false)+"Chrome: not found — the browser does not work without it")
	case len(c.Libraries) > 0:
		fmt.Fprintln(w, mark(false)+"Chrome: "+c.Chrome+" "+c.Version+" — does not start: missing libraries "+strings.Join(c.Libraries, ", "))
	default:
		fmt.Fprintln(w, mark(true)+"Chrome: "+c.Chrome+" "+c.Version)
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
	if c.Summary != "" {
		fmt.Fprintln(w, "\n"+c.Summary)
	}
	if len(c.Commands) > 0 {
		root := ""
		if c.NeedsRoot {
			root = " (needs root)"
		}
		fmt.Fprintln(w, "To install what is missing"+root+":")
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
	c := browserenv.CheckBrowser(setupSysFn())
	fix := "gate4ai browser setup"
	var out []sandbox.Check
	switch {
	case c.Chrome == "":
		out = append(out, sandbox.Check{ID: "browser_chrome", Status: "fail", Detail: "Chrome is not installed: the browser connector does not work. " + c.Summary, Fix: fix})
	case len(c.Libraries) > 0:
		out = append(out, sandbox.Check{ID: "browser_chrome", Status: "fail", Detail: strings.TrimSpace(c.Chrome+" "+c.Version) + " does not start: missing libraries " + strings.Join(c.Libraries, ", ") + ". " + c.Summary, Fix: fix})
	default:
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
