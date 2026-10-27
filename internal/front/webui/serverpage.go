// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/browserenv"
	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/gate4ai/internal/relayclient"
)

// Страница «Сервер» (Настройки → Сервер): состояние компонентов установки на
// одном экране — служба и автозапуск, хранилище ключей, адрес из интернета,
// защита входа, Telegram, браузер агентов (пакет, Chrome, экран, шрифты),
// агенты на этом же компьютере. У каждого пункта — что сделать: кнопка на
// страницу настроек (локально) или команда для терминала. Нужна на сервере без
// графики, куда владелец заходит удалённо: там страница доступна и в /ui/,
// только для чтения.

// ServerEnv — окружение проверок (подменяется в тестах).
type ServerEnv struct {
	Browser browserenv.Sys
	// Linger — включён ли linger у пользователя (systemd: служба живёт без
	// входа в систему); "" — неизвестно (не Linux, нет loginctl).
	Linger func() string
	// Keystore — содержимое маркера хранилища ключей в каталоге данных.
	Keystore func(home string) string
}

// SystemServerEnv — этот компьютер.
func SystemServerEnv() ServerEnv {
	return ServerEnv{Browser: browserenv.System(),
		Linger: func() string {
			if _, err := exec.LookPath("loginctl"); err != nil {
				return ""
			}
			out, err := exec.Command("loginctl", "show-user", os.Getenv("USER"), "-p", "Linger", "--value").Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		},
		Keystore: func(home string) string {
			b, _ := os.ReadFile(filepath.Join(home, "keystore"))
			return strings.TrimSpace(string(b))
		}}
}

// ServerCheck — один компонент.
type ServerCheck struct {
	ID     string
	Title  Msg
	Status string // ok | warn | bad | off
	Detail Msg    // состояние словами
	Fix    Msg    // что сделать ("" — ничего)
	Cmd    string // команда для терминала
	Link   string // страница Web UI с настройкой (локально)
	Button Msg
}

// headless — сервер без графического сеанса (Linux без DISPLAY).
func (e ServerEnv) headless() bool {
	s := e.Browser
	if s.GOOS == "darwin" || s.GOOS == "windows" {
		return false
	}
	return s.Getenv("DISPLAY") == "" && s.Getenv("WAYLAND_DISPLAY") == ""
}

func (u *UI) serverEnv() ServerEnv {
	if u.Server != nil {
		return u.Server()
	}
	return SystemServerEnv()
}

// serverChecks — состояние компонентов.
func (u *UI) serverChecks(ctx context.Context) []ServerCheck {
	env := u.serverEnv()
	headless := env.headless()
	cfg := u.D.Config.Current()
	var out []ServerCheck
	add := func(c ServerCheck) { out = append(out, c) }

	// Служба.
	c := ServerCheck{ID: "service", Title: M("gate4.ai service"), Status: "ok", Detail: M("running, version %s", version.Version)}
	if u.Autostart != nil {
		st := u.Autostart.State()
		switch autostartState(st) {
		case "on-running":
			c.Detail = M("runs as a service, starts at every login; version %s", version.Version)
		case "on":
			c.Status, c.Detail = "warn", M("starts at the next login, but right now it runs without the service")
			c.Fix, c.Cmd = M("Restart as a service."), "gate4ai service install"
		default:
			c.Status, c.Detail = "warn", M("no autostart: after a reboot agents get “device offline”")
			c.Fix, c.Cmd, c.Link, c.Button = M("Turn the autostart on."), "gate4ai service install", "/settings#autostart", M("Settings")
		}
	}
	add(c)
	if headless && env.Linger != nil {
		switch env.Linger() {
		case "yes":
			add(ServerCheck{ID: "linger", Title: M("Service without a login session"), Status: "ok", Detail: M("linger is on: the service keeps running after you log out of the server")})
		case "no":
			add(ServerCheck{ID: "linger", Title: M("Service without a login session"), Status: "warn", Detail: M("linger is off: user services stop when you log out of the server"),
				Fix: M("Keep them running."), Cmd: "loginctl enable-linger $USER"})
		}
	}

	// Ключи.
	ks := ""
	if env.Keystore != nil {
		ks = env.Keystore(u.D.Home)
	}
	switch ks {
	case "file":
		add(ServerCheck{ID: "keystore", Title: M("Key store"), Status: "warn", Detail: M("a file in the data folder (keys.json): secrets are protected only by file permissions — usual for a server without a keychain"),
			Fix: M("Keep the data folder private to the gate4ai user; agents run as another OS user or in gate4ai run.")})
	case "keychain":
		add(ServerCheck{ID: "keystore", Title: M("Key store"), Status: "ok", Detail: M("the system keychain")})
	}

	// Адрес из интернета.
	pub, rs := u.D.PublicURL(), u.relay()
	switch {
	case rs.Disabled:
		add(ServerCheck{ID: "public", Title: M("Access from the internet"), Status: "off", Detail: M("turned off by the owner: the Web UI, approvals and browser sessions are reachable only on this computer"),
			Fix: M("Turn internet access on."), Link: "/settings/internet", Button: M("Turn on")})
	case rs.Configured && !rs.Online:
		add(ServerCheck{ID: "public", Title: M("Access from the internet"), Status: "bad", Detail: M("the relay is set up, but the device is not connected to it%s", relayErr(rs.Error)),
			Fix: M("Check the network and the daemon log."), Cmd: "gate4ai relay status", Link: "/settings/internet", Button: M("Internet access")})
	case rs.TempCert:
		add(ServerCheck{ID: "public", Title: M("Access from the internet"), Status: "warn", Detail: M("%s works on a temporary certificate: the browser warns about it, cloud assistants do not connect yet%s", pub, relayErr(rs.Error)),
			Fix: M("The certificate is issued again by itself; nothing to do."), Cmd: "gate4ai relay status", Link: "/settings/internet", Button: M("Internet access")})
	case pub != "":
		add(ServerCheck{ID: "public", Title: M("Access from the internet"), Status: "ok", Detail: M("%s — the Web UI from the phone at /ui/, agents on other computers, links to browser sessions", pub)})
	case rs.Phase == relayclient.PhaseRegistering:
		add(ServerCheck{ID: "public", Title: M("Access from the internet"), Status: "warn", Detail: M("turning on: registering this computer on the relay…"), Link: "/settings/internet", Button: M("Internet access")})
	case rs.Phase == relayclient.PhaseFailed:
		var next any = ""
		if at := rs.NextRetryAt(); at != "" {
			next = M(" Next attempt at %s.", at)
		}
		add(ServerCheck{ID: "public", Title: M("Access from the internet"), Status: "bad", Detail: M("could not turn on%s.%v", relayErr(rs.Error), next),
			Fix: M("Check the network; the daemon retries by itself."), Cmd: "gate4ai relay status", Link: "/settings/internet", Button: M("Internet access")})
	default:
		st := "warn"
		if !headless {
			st = "off"
		}
		add(ServerCheck{ID: "public", Title: M("Access from the internet"), Status: st, Detail: M("no public address: the Web UI, approvals and browser sessions are reachable only on this computer (or through an ssh tunnel)"),
			Fix: M("Register the device on a relay."), Cmd: "gate4ai relay register --url https://relay.gate4.ai", Link: "/settings/internet", Button: M("Turn on")})
	}

	// Вход.
	p := u.Protection()
	switch {
	case p.Set() && p.Telegram:
		add(ServerCheck{ID: "login", Title: M("Signing in to the Web UI"), Status: "ok", Detail: M("password and confirmation in Telegram")})
	default:
		var fix Msg
		if headless {
			fix = M("On a server turn on Telegram confirmation too: a password typed on another computer may be read there.")
		}
		add(ServerCheck{ID: "login", Title: M("Signing in to the Web UI"), Status: "ok", Detail: M("password"), Fix: fix, Link: "/settings/login", Button: M("Signing in")})
	}

	// Telegram.
	tg := ServerCheck{ID: "telegram", Title: M("Telegram bot"), Status: "off", Detail: M("not connected: approvals, handoffs and sign-in confirmations reach you only in the Web UI"),
		Fix: M("Connect your bot."), Link: "/approvals/telegram", Button: M("Connect")}
	if headless {
		tg.Status = "warn"
	}
	for _, ch := range u.channelStatus() {
		if ch.Name != "telegram" || !ch.On {
			continue
		}
		if ch.State == "ok" {
			tg = ServerCheck{ID: "telegram", Title: M("Telegram bot"), Status: "ok", Detail: M("connected: approvals, browser handoffs and sign-in confirmations come to your phone")}
		} else {
			tg = ServerCheck{ID: "telegram", Title: M("Telegram bot"), Status: "bad", Detail: M("on, but does not work: %s", or(ch.Detail, ch.State)), Link: "/settings/notifications", Button: M("Notifications")}
		}
	}
	add(tg)

	// Браузер.
	bctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	bs, _ := u.D.BrowserStatus(bctx)
	cancel()
	ck := browserenv.CheckBrowser(env.Browser)
	switch {
	case bs.Missing:
		add(ServerCheck{ID: "browser_pkg", Title: M("Browser"), Status: "off", Detail: M("not available in this build: agents have no browser")})
	case bs.Running:
		add(ServerCheck{ID: "browser_pkg", Title: M("Browser"), Status: "ok", Detail: M("running, %d sessions", len(bs.Sessions))})
	default:
		add(ServerCheck{ID: "browser_pkg", Title: M("Browser"), Status: "ok", Detail: M("built in; Chrome starts with the first browser action")})
	}
	if !bs.Missing {
		if ck.Chrome == "" {
			add(ServerCheck{ID: "chrome", Title: M("Chrome"), Status: "bad", Detail: M("not installed: agents get browser_unavailable"), Fix: M("Install it (asks first)."), Cmd: "gate4ai browser setup", Link: "/browser", Button: M("Browser")})
		} else if len(ck.Libraries) > 0 {
			add(ServerCheck{ID: "chrome", Title: M("Chrome"), Status: "bad", Detail: M("%s does not start: missing libraries %s", strings.TrimSpace(ck.Chrome+" "+ck.Version), strings.Join(ck.Libraries, ", ")),
				Fix: M("Install them (asks first): %s", strings.Join(ck.Commands, "; ")), Cmd: "gate4ai browser setup", Link: "/browser", Button: M("Browser")})
		} else {
			add(ServerCheck{ID: "chrome", Title: M("Chrome"), Status: "ok", Detail: M("%s", strings.TrimSpace(ck.Chrome+" "+ck.Version))})
		}
		if ck.OS != "darwin" && ck.OS != "windows" {
			if ck.Chrome != "" && (ck.Sandbox == "blocked" || bs.Sandbox == "off") {
				add(ServerCheck{ID: "sandbox", Title: M("Chrome sandbox: off (--no-sandbox)"), Status: "warn", Detail: M("the kernel does not let this Chrome create user namespaces (AppArmor): it runs without its sandbox; keep it updated"), Cmd: "gate4ai browser setup --user"})
			}
			switch {
			case ck.Screen:
				add(ServerCheck{ID: "screen", Title: M("Screen for the browser"), Status: "ok", Detail: M("this computer's")})
			case ck.Xvfb != "" || bs.Display == "virtual":
				add(ServerCheck{ID: "screen", Title: M("Screen for the browser"), Status: "ok", Detail: M("virtual (Xvfb): sites see an ordinary Chrome")})
			default:
				add(ServerCheck{ID: "screen", Title: M("Screen for the browser"), Status: "warn", Detail: M("none and no Xvfb: the browser runs headless, sites with bot protection ask for a captcha or block more often"),
					Fix: M("Install Xvfb."), Cmd: "gate4ai browser setup"})
			}
			if !ck.Screen && ck.Chrome != "" {
				if ck.Fonts < 5 {
					add(ServerCheck{ID: "fonts", Title: M("Fonts"), Status: "warn", Detail: M("few fonts: pages show boxes instead of letters"), Fix: M("Install Noto fonts."), Cmd: "gate4ai browser setup"})
				} else {
					add(ServerCheck{ID: "fonts", Title: M("Fonts"), Status: "ok", Detail: M("%d", ck.Fonts)})
				}
			}
		}
	}

	// Агенты на этом же компьютере.
	if headless {
		seen, _ := u.D.State.LastSeen()
		var local []string
		for a, s := range seen {
			if s.Via == "agent-socket" || s.Via == "socket" {
				local = append(local, a)
			}
		}
		if len(local) > 0 {
			add(ServerCheck{ID: "agents_local", Title: M("Agents on this server"), Status: "warn",
				Detail: M("%s work through the local socket, most likely as the same OS user as gate4ai: such a process can read the data folder and go around the policies", strings.Join(local, ", ")),
				Fix:    M("Run agents as another OS user (they connect through the device address, GATE4AI_URL) or in gate4ai run.")})
		} else if len(cfg.Agents) > 0 {
			add(ServerCheck{ID: "agents_local", Title: M("Agents on this server"), Status: "ok", Detail: M("no agent uses the local socket")})
		}
	}
	return out
}

func relayErr(e string) string {
	if e == "" {
		return ""
	}
	return ": " + e
}

// serverPage — Настройки → Сервер; удалённо — только чтение.
func (u *UI) serverPage(m mount) func(r *http.Request) (string, map[string]any, error) {
	return func(r *http.Request) (string, map[string]any, error) {
		checks := u.serverChecks(r.Context())
		warn := 0
		for _, c := range checks {
			if c.Status == "warn" || c.Status == "bad" {
				warn++
			}
		}
		return "server", map[string]any{"Checks": checks, "Warn": warn, "Headless": u.serverEnv().headless()}, nil
	}
}
