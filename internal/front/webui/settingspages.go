// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"encoding/base64"
	"errors"
	"html/template"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/gate4ai/gate4ai/internal/approval"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/autostart"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/gate4ai/internal/relayclient"
)

// ---- Пароли и учётки ----

func (u *UI) vaultPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	set := map[string]bool{}
	for _, a := range u.D.Vault.Aliases() {
		set[a] = true
	}
	return "vault", map[string]any{"Aliases": u.D.Vault.Aliases(), "Uses": secretUses(cfg, u.D.Registry.Manifests()),
		"Accounts": accountsView(cfg, set), "Grants": grants(cfg)}, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// aliasOf — имя секрета из названия, которое ввёл человек: «Work mail» → @work-mail.
func aliasOf(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "@"))), "-"), "-.")
	if s == "" {
		return ""
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return "@" + s
}

func (u *UI) setSecret(w http.ResponseWriter, r *http.Request) map[string]any {
	alias := strings.TrimSpace(r.PostForm.Get("alias"))
	if !strings.HasPrefix(alias, "@") {
		alias = aliasOf(or(alias, r.PostForm.Get("name")))
	}
	if alias == "" {
		return redirectErr("/vault", M("Give the password a name"))
	}
	if err := u.D.Vault.Set(alias, r.PostForm.Get("value")); err != nil {
		return redirectErr("/vault", err)
	}
	if r.PostForm.Get("edit") == "1" {
		return redirect("/vault#"+SecretAnchor(alias), M("%s is replaced", alias))
	}
	return redirect("/vault#"+SecretAnchor(alias), M("Saved as %s", alias))
}

func (u *UI) deleteSecret(w http.ResponseWriter, r *http.Request) map[string]any {
	if err := u.D.Vault.Delete(r.PostForm.Get("alias")); err != nil {
		return redirectErr("/vault", err)
	}
	return redirect("/vault", M("%s is deleted", r.PostForm.Get("alias")))
}

// editAccount — окно «Изменить» учётки: профили агентов (use), адреса входа (bind)
// и, по желанию, новые значения её секретов. Поля формы, коннектор входа и
// коннекторы с доступом — только в YAML и на страницах коннекторов.
func (u *UI) editAccount(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	alias := strings.TrimSpace(f.Get("alias"))
	back := "/vault#" + AccountAnchor(alias)
	var bind []string
	for _, b := range strings.Split(f.Get("bind"), "\n") {
		if b = strings.TrimSpace(b); b != "" {
			bind = append(bind, b)
		}
	}
	if len(bind) == 0 {
		return redirectErr(back, M("Enter at least one sign-in address, e.g. https://login.example.com"))
	}
	var cr config.Credential
	err := u.edit("account "+alias, func(c *config.Config) error {
		var ok bool
		if cr, ok = c.Credentials[alias]; !ok {
			return M("There is no account %s", alias)
		}
		cr.Bind, cr.Use = bind, f["profiles"]
		c.Credentials[alias] = cr
		return nil
	})
	if err != nil {
		return redirectErr(back, err)
	}
	for _, sec := range cr.Secrets() {
		if v := f.Get("secret." + sec); v != "" {
			if err := u.D.Vault.Set(sec, v); err != nil {
				return redirectErr(back, err)
			}
		}
	}
	return redirect(back, M("Account %s is saved", alias))
}

// deleteAccount — учётка из конфигурации и её секреты из хранилища.
func (u *UI) deleteAccount(w http.ResponseWriter, r *http.Request) map[string]any {
	alias := strings.TrimSpace(r.PostForm.Get("alias"))
	var secrets []string
	err := u.edit("delete account "+alias, func(c *config.Config) error {
		cr, ok := c.Credentials[alias]
		if !ok {
			return M("There is no account %s", alias)
		}
		secrets = cr.Secrets()
		delete(c.Credentials, alias)
		return nil
	})
	if err != nil {
		return redirectErr("/vault#accounts", err)
	}
	for _, sec := range secrets {
		_ = u.D.Vault.Delete(sec)
	}
	return redirect("/vault#accounts", M("Account %s is deleted", alias))
}

// ---- Настройки ----

// Autostart — автозапуск daemon при входе (подменяется в тестах).
type Autostart interface {
	State() map[string]any
	Enable(bin string) error
	Disable() error
}

type systemAutostart struct{}

func (systemAutostart) State() map[string]any {
	h, _ := os.UserHomeDir()
	return autostart.State(h)
}

func (systemAutostart) Enable(bin string) error {
	h, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	_, err = autostart.Install(autostart.Spec{Bin: bin, Tray: autostart.FindTray(bin), Log: paths.DaemonLog(), Home: h, NoStart: true})
	return err
}

func (systemAutostart) Disable() error {
	h, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	autostart.Uninstall(h, true)
	return nil
}

// autostartState — состояние автозапуска словами: on-running (служба запускает
// gate4.ai), on (включён, сейчас gate4.ai запущен не службой), off.
func autostartState(st map[string]any) string {
	installed, _ := st["installed"].(bool)
	enabled, hasEnabled := st["enabled"].(bool)
	if !hasEnabled {
		enabled = installed
	}
	if !enabled {
		return "off"
	}
	if running, ok := st["running"].(bool); ok && running {
		return "on-running"
	}
	return "on"
}

// selfConfigDenied — шаблон «Агентам нельзя менять настройки» применён ко всем профилям с агентами.
func selfConfigDenied(cfg *config.Config) bool {
	any := false
	for _, g := range grants(cfg) {
		if len(g.Agents) == 0 {
			continue
		}
		any = true
		if !slices.ContainsFunc(cfg.Profiles[g.Profile].Rules, func(r config.Rule) bool {
			return r.Action == daemon.SelfConnector+".change_policy" && r.Effect == config.EffectDeny && r.When == ""
		}) {
			return false
		}
	}
	return any
}

func (u *UI) settingsPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	data := map[string]any{"Sub": "main", "Timeout": timeoutWord(cfg.Approvals.Timeout.D()), "Timeouts": timeouts,
		"SelfDenied": selfConfigDenied(cfg), "PublicURL": u.D.PublicURL(), "Relay": u.relay()}
	if u.Autostart != nil {
		st := u.Autostart.State()
		data["Autostart"], data["AutostartState"] = st, autostartState(st)
	}
	p := u.Protection()
	data["Protection"] = map[string]bool{"Password": p.Set(), "Telegram": p.Telegram}
	return "settings", data, nil
}

// notificationsPage — каналы уведомлений: один список, у канала одна кнопка по состоянию.
func (u *UI) notificationsPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	data := map[string]any{"Sub": "notifications", "Timeout": timeoutWord(cfg.Approvals.Timeout.D()), "TelegramSet": cfg.Approvals.Telegram != nil,
		"PushOn": cfg.ChannelOn("webpush"), "PublicURL": u.D.PublicURL()}
	data["Channels"] = u.channelStatus()
	if u.Channels != nil && u.Channels.Push != nil {
		data["PushKey"] = u.Channels.Push.PublicKey()
	}
	return "settings", data, nil
}

// channelStatus — состояние каналов; без подключённых каналов (тесты, ошибка
// запуска) — по конфигурации: включён / выключен.
func (u *UI) channelStatus() []approval.ChannelStatus {
	if u.Channels != nil {
		return u.Channels.Status()
	}
	cfg := u.D.Config.Current()
	st := func(name, title string) approval.ChannelStatus {
		on := cfg.ChannelOn(name)
		c := approval.ChannelStatus{Name: name, Title: title, On: on, State: approval.StateOff}
		if on {
			c.State = approval.StateOK
		}
		return c
	}
	return []approval.ChannelStatus{st("desktop", "Notifications on this computer"), st("telegram", "Telegram"), st("webpush", "Browser push")}
}

func (u *UI) loginSettingsPage(r *http.Request) (string, map[string]any, error) {
	// Хэш пароля на страницу не попадает — только «задан / не задан».
	p := u.Protection()
	data := map[string]any{"Sub": "login", "Protection": map[string]bool{"Password": p.Set(), "Telegram": p.Telegram},
		"Addr": u.Addr, "PublicURL": u.D.PublicURL(), "TelegramReady": u.confirmer() != nil}
	return "settings", data, nil
}

func (u *UI) internetPage(r *http.Request) (string, map[string]any, error) {
	data := map[string]any{"Sub": "internet", "PublicURL": u.D.PublicURL()}
	if link := r.URL.Query().Get("link"); link == "1" {
		if l, err := u.RemoteURL(""); err == nil {
			data["RemoteLink"] = l
			if code, err := qr.Encode(l, qr.M); err == nil {
				// template.URL: иначе html/template не пускает data: в src и подставляет #ZgotmplZ
				data["RemoteQR"] = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()))
			}
		} else {
			data["RemoteLinkError"] = err.Error()
			data["RemoteLinkOffline"] = errors.Is(err, errRelayOffline)
		}
	}
	maps.Copy(data, u.relayData())
	return "settings", data, nil
}

func (u *UI) advancedPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	data := map[string]any{"Sub": "advanced", "Config": cfg, "Version": version.Version, "Home": u.D.Home, "Log": paths.DaemonLog(),
		"Retention": map[string]string{"receipts": retentionWord(cfg.Retention.Receipts.D()), "denied": retentionWord(cfg.Retention.Denied.D()),
			"evidence_other": retentionWord(cfg.Retention.EvidenceOther.D()), "evidence_money": retentionWord(cfg.Retention.EvidenceMoney.D())},
		"Retentions": retentions}
	if u.Autostart != nil {
		data["Autostart"] = u.Autostart.State()
	}
	return "settings", data, nil
}

// retentions — сроки хранения в выпадающем списке ("0s" — бессрочно).
var retentions = []string{"30d", "90d", "1y", "3y", "0s"}

func retentionWord(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	days := int(d.Hours() / 24)
	if days%365 == 0 {
		return strconv.Itoa(days/365) + "y"
	}
	return strconv.Itoa(days) + "d"
}

// relayData — данные блока «Доступ из интернета»: состояние, relay по умолчанию и
// прямой адрес (--public), если daemon запущен с ним.
func (u *UI) relayData() map[string]any {
	rs := u.relay()
	pub := u.D.PublicURL()
	direct := ""
	if pub != "" && (!rs.Configured || pub != "https://"+rs.Host) {
		direct = pub
	}
	def, _ := url.Parse(relayclient.DefaultURL)
	return map[string]any{"Relay": rs, "RelayDefault": relayclient.DefaultURL, "RelayDefaultHost": def.Host, "Direct": direct}
}

// relayPage — свой relay, свой HTTPS-адрес без relay и повторная регистрация.
func (u *UI) relayPage(r *http.Request) (string, map[string]any, error) {
	return "relay", u.relayData(), nil
}

func (u *UI) registerRelay(w http.ResponseWriter, r *http.Request) map[string]any {
	addr := strings.TrimRight(strings.TrimSpace(or(r.PostForm.Get("url"), relayclient.DefaultURL)), "/")
	if !strings.HasPrefix(addr, "https://") {
		addr = "https://" + strings.TrimPrefix(addr, "http://")
	}
	// Повторная регистрация — новое устройство: прежний адрес перестаёт работать,
	// выпускается новый сертификат. Только с явным подтверждением.
	if u.D.Config.Current().Relay.DeviceID != "" && r.PostForm.Get("confirm") != "1" {
		return redirectErr("/settings/relay#again", M("This computer is already registered: to register it again, confirm that the current address will stop working"))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := relayclient.RegisterDevice(ctx, u.D, owner, addr, "", false)
	if err != nil {
		back := "/settings/internet"
		if f := r.PostForm.Get("form"); f == "custom" || f == "again" {
			back = "/settings/relay#" + f
		}
		return redirectErr(back, M("Relay %s: %s", addr, err))
	}
	return redirect("/settings/internet", M("Internet access is on. The address for Claude.ai / ChatGPT is %v. Getting a certificate and connecting usually takes under a minute; the page updates itself.", res["mcp_url"]))
}

func (u *UI) toggleAutostart(w http.ResponseWriter, r *http.Request) map[string]any {
	back := or(r.PostForm.Get("back"), "/settings#autostart")
	if u.Autostart == nil {
		return redirectErr(back, M("Autostart is not available here"))
	}
	var err error
	on := r.PostForm.Get("on") == "1"
	if on {
		err = u.Autostart.Enable(u.bin())
	} else {
		err = u.Autostart.Disable()
	}
	if err != nil {
		return redirectErr(back, err)
	}
	if on {
		return redirect(back, "Autostart is on: gate4.ai starts when you log in.")
	}
	return redirect(back, "Autostart is off. The running gate4.ai keeps working until you log out.")
}

// saveSelfConfig — «Агенты могут менять настройки gate4.ai»: спрашивать (как любое
// изменяющее действие) или нельзя (правило deny во всех правах с агентами).
func (u *UI) saveSelfConfig(w http.ResponseWriter, r *http.Request) map[string]any {
	deny := r.PostForm.Get("self") == "deny"
	action := daemon.SelfConnector + ".change_policy"
	err := u.edit("agents change settings: "+map[bool]string{true: "deny", false: "ask"}[deny], func(c *config.Config) error {
		for _, g := range grants(c) {
			if len(g.Agents) == 0 {
				continue
			}
			p := c.Profiles[g.Profile]
			p.Rules = slices.DeleteFunc(p.Rules, func(x config.Rule) bool {
				return x.Action == action && x.When == "" && x.Effect == config.EffectDeny
			})
			c.Profiles[g.Profile] = p
			if deny {
				if err := c.PutRule(g.Profile, config.Rule{ID: c.Profiles[g.Profile].UniqueRuleID("no-self-config"), Action: action,
					Effect: config.EffectDeny, Reason: "Only the owner changes gate4.ai settings"}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return redirectErr("/settings", err)
	}
	return redirect("/settings", "Saved")
}

func (u *UI) saveGeneral(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	back := or(f.Get("back"), "/settings/advanced")
	err := u.edit("settings", func(c *config.Config) error {
		if f.Has("registry") {
			c.Store.Registry = strings.TrimSpace(f.Get("registry"))
			c.Store.RegistryKeys = splitItems(f.Get("registry_keys"))
		}
		for _, k := range []string{"receipts", "denied", "evidence_other", "evidence_money"} {
			if !f.Has("ret_" + k) {
				continue
			}
			var d time.Duration
			if v := strings.TrimSpace(f.Get("ret_" + k)); v != "" && v != "0s" {
				var err error
				if d, err = config.ParseDuration(v); err != nil {
					return M("%s: %s", k, err)
				}
			}
			dd := config.Duration(d)
			switch k {
			case "receipts":
				c.Retention.Receipts = dd
			case "denied":
				c.Retention.Denied = dd
			case "evidence_other":
				c.Retention.EvidenceOther = dd
			case "evidence_money":
				c.Retention.EvidenceMoney = dd
			}
		}
		return nil
	})
	if err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Settings saved")
}

func (u *UI) remoteLink(w http.ResponseWriter, r *http.Request) map[string]any {
	return redirect("/settings/internet?link=1#remote", nil)
}

// serviceWorker — Service Worker для браузерного push (область — база Web UI).
func serviceWorker(w http.ResponseWriter, r *http.Request) {
	b, _ := staticFS.ReadFile("static/sw.js")
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(b)
}
