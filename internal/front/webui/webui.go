// SPDX-License-Identifier: AGPL-3.0-only

// Package webui — Web UI владельца на localhost: подтверждения, агенты (токены,
// коды подключения, отзыв), журнал, базовый редактор политик с симулятором и
// откатом, алиасы Vault. Вход — пароль (и, по выбору, подтверждение в
// Telegram-боте; protect.go) на постоянном адресе; дальше — сессия в
// HttpOnly-cookie. `gate4ai ui` и трей просто открывают этот адрес.
// Защита: проверка Host (DNS rebinding), SameSite=Strict, CSRF-токен в формах.
//
// Удалённо Web UI доступен на публичном адресе устройства под /ui/ (RemoteHandler):
// свой вход и своя cookie, локальная сессия там не действует и наоборот;
// изменения политик, агентов и секретов — только локально.
package webui

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/approval"
	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/platform/browserenv"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/internal/state"
)

//go:embed templates/*.html
var tplFS embed.FS

//go:embed static
var staticFS embed.FS

var funcs = template.FuncMap{
	"short": audit.Short,
	"ts":    func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"json": func(v any) string {
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	},
	"join":      strings.Join,
	"base":      filepath.Base,
	"isCode":    func(w string) bool { _, ok := DescribeWhen(w); return !ok },
	"has":       slices.Contains[[]string],
	"accAnchor": AccountAnchor,
	"secAnchor": SecretAnchor,
	"inc":       func(i int) int { return i + 1 },
	"list":      func(s ...string) []string { return s },
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"lines":         func(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") },
	"first":         func(s string) string { return strings.SplitN(s, "\n", 2)[0] },
	"langs":         func() any { return Langs },
	"pageOf":        pageOf,
	"verifiedAfter": func() int { return pipeline.VerifiedAfter },
}

// tpls — шаблоны на каждом языке: функции перевода привязаны к языку.
var tpls = func() map[string]*template.Template {
	out := map[string]*template.Template{}
	for _, l := range []string{langEN, langRU} {
		out[l] = template.Must(template.New("").Funcs(funcs).Funcs(langFuncs(l)).ParseFS(tplFS, "templates/*.html"))
	}
	return out
}()

// tplIn — шаблоны на языке l.
func tplIn(l string) *template.Template {
	if t, ok := tpls[l]; ok {
		return t
	}
	return tpls[langEN]
}

var titles = map[string]string{"overview": "Home", "approvals": "Approvals", "telegram": "Telegram", "agents": "Agents", "agent": "Agent",
	"agent_new": "New agent", "setup": "Getting started", "connectors": "Access", "connector": "Connector", "knowledge": "Knowledge", "policy": "Rules", "rule": "Rule", "templates": "Ready-made rules",
	"policy_yaml": "YAML", "journal": "Journal", "receipt": "Journal entry", "vault": "Passwords and accounts", "settings": "Settings", "relay": "Internet access",
	"remote_only": "Only on this computer", "browser": "Browser", "browser_view": "Browser session", "server": "Server"}

// or — первое непустое.
var or = cmp.Or[string]

// agoIn — «3 min ago» на языке l (t — time.Time; нулевое — «never»).
func agoIn(l string, v any) string {
	t, _ := v.(time.Time)
	if t.IsZero() {
		return tr(l, "never")
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return tr(l, "just now")
	case d < time.Hour:
		return M("%d min ago", int(d.Minutes())).In(l)
	case d < 24*time.Hour:
		return M("%d h ago", int(d.Hours())).In(l)
	case d < 30*24*time.Hour:
		return M("%d d ago", int(d.Hours()/24)).In(l)
	}
	return t.Local().Format("2006-01-02")
}

// ago — по-английски (сообщения, которые переводятся целиком).
func ago(t time.Time) string { return agoIn(langEN, t) }

// leftIn — сколько осталось до t: «28 min left».
func leftIn(l string, v any) string {
	t, _ := v.(time.Time)
	d := time.Until(t)
	switch {
	case t.IsZero():
		return ""
	case d <= 0:
		return tr(l, "time is up")
	case d < time.Hour:
		return M("%d min left", int(d.Minutes())+1).In(l)
	case d < 48*time.Hour:
		return M("%d h left", int(d.Hours())).In(l)
	}
	return M("%d d left", int(d.Hours()/24)).In(l)
}

// UI — Web UI.
type UI struct {
	D    *daemon.Daemon
	Addr string // 127.0.0.1:port после Listen
	Now  func() time.Time

	// Channels — каналы подтверждений (nil — не подключены, например в тестах).
	Channels *approval.Channels
	// Relay — состояние relay (nil — relay не подключён).
	Relay func() relayclient.Status
	// Clients — окружение пользователя для автоподключения агентов.
	Clients agentsetup.Env
	// KeyDir — каталог ключей агентов (agentkeys): автоподключение кладёт туда
	// ключ, CLI агента находит его сам.
	KeyDir string
	// Bin — бинарник gate4ai для конфигурации клиентов ("" — os.Executable).
	Bin string
	// Autostart — включение автозапуска (подменяется в тестах).
	Autostart Autostart
	// Confirm — подтверждение входа на другом устройстве (nil — Telegram-бот из Channels).
	Confirm LoginConfirmer
	// DefaultLang — язык, если браузер не выбрал (переключатель, Accept-Language); "" — английский.
	DefaultLang string
	// BrowserEnv — проверка Chrome и Xvfb для раздела «Браузер» (nil — этот компьютер).
	BrowserEnv func() browserenv.Sys
	// Server — окружение страницы «Сервер» (nil — этот компьютер).
	Server func() ServerEnv

	mu       sync.Mutex
	sessions map[string]session
	attempts map[string]*attempt // попытки входа
	initial  string              // сгенерированный пароль, которым ещё не входили (см. EnsurePassword)
	limits   [2]limiter          // защита от перебора пароля: локально, удалённо
	notes    map[string]note     // сообщение после действия: сессия → одно последнее
}

// note — сообщение после действия (flash или ошибка) до следующей страницы сессии.
type note struct {
	flash, err any // Msg, Word, error или английская строка
	detail     string
}

type session struct {
	csrf     string
	expires  time.Time
	remote   bool
	telegram bool // вход подтверждён в Telegram
}

// mount — где обслуживается Web UI: локально (loopback) или удалённо (/ui/ на
// публичном адресе устройства).
type mount struct {
	base   string // "" или "/ui"
	remote bool
}

var local, remote = mount{}, mount{base: "/ui", remote: true}

func (m mount) cookie() string {
	if m.remote {
		return "g4a_rsession"
	}
	return "g4a_session"
}

// New создаёт Web UI.
func New(d *daemon.Daemon) *UI {
	return &UI{D: d, Now: time.Now, sessions: map[string]session{}, attempts: map[string]*attempt{},
		notes: map[string]note{}, Clients: agentsetup.Default(), Autostart: systemAutostart{}, KeyDir: filepath.Join(d.Home, "agents")}
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// URL — адрес Web UI на этом компьютере; next — страница после входа
// (только раздел «Браузер», см. safeNext), "" — главная.
func (u *UI) URL(next string) string {
	return "http://" + u.Addr + cmp.Or(safeNext(next), "/")
}

// errRelayOffline — адрес устройства на relay есть, но туннеля нет: адрес не
// откроется (relay подержит соединение и сбросит его, браузер покажет таймаут).
var errRelayOffline = errors.New("the device is not connected to the relay, the address would not open")

// RemoteURL — адрес удалённого Web UI (/ui/ на публичном адресе устройства).
func (u *UI) RemoteURL(next string) (string, error) {
	pub := u.D.PublicURL()
	if pub == "" {
		return "", fmt.Errorf("no public address: register the device on a relay or start the daemon with --public")
	}
	if st := u.relay(); st.Configured && !st.Online && pub == "https://"+st.Host {
		if st.Error != "" {
			return "", fmt.Errorf("%w: %s", errRelayOffline, st.Error)
		}
		return "", errRelayOffline
	}
	return pub + remote.base + cmp.Or(safeNext(next), "/"), nil
}

// remoteState — адрес Web UI из интернета и его состояние для ответа /v1/ui/url:
// remote_url — адрес, когда он откроется (прямой адрес или relay онлайн);
// remote_phase — этап relay (registering, issuing, connecting, online,
// reconnecting, failed; "" — доступ из интернета не настраивается);
// remote_cert — "ok" или "temporary" (туннель на временном самоподписанном
// сертификате, пока выпуск не удался: браузер предупредит о нём);
// remote_error — последняя ошибка; remote_next_retry — когда повторится.
func (u *UI) remoteState(next string) map[string]any {
	out := map[string]any{}
	st := u.relay()
	if st.Disabled {
		out["remote_disabled"] = true
		return out
	}
	if st.Phase != "" {
		out["remote_phase"] = st.Phase
	}
	if st.Error != "" {
		out["remote_error"] = st.Error
	}
	if !st.NextRetry.IsZero() {
		out["remote_next_retry"] = st.NextRetry.Format(time.RFC3339)
	}
	if link, err := u.RemoteURL(next); err == nil {
		out["remote_url"] = link
		out["remote_cert"] = "ok"
		if st.TempCert {
			out["remote_cert"] = "temporary"
		}
	}
	return out
}

// Listen слушает только loopback.
func (u *UI) Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("web UI listens only on loopback, got %s", addr)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	u.Addr = l.Addr().String()
	return l, nil
}

// OwnerRoutes — маршрут сокета владельца, выдающий адрес Web UI (трей, `gate4ai ui`).
func (u *UI) OwnerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ui/url", func(w http.ResponseWriter, r *http.Request) {
		if !daemon.PrincipalFrom(r.Context()).IsOwner() {
			http.Error(w, `{"error":{"code":"owner_only","message":"owner only"}}`, 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Remote bool   `json:"remote"`
			Next   string `json:"next"` // после входа — эта страница (просмотр сеанса браузера)
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		link := u.URL(req.Next)
		if req.Remote {
			var err error
			if link, err = u.RemoteURL(req.Next); err != nil {
				w.WriteHeader(409)
				code := "no_public_address"
				if errors.Is(err, errRelayOffline) {
					code = "relay_offline"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
				return
			}
		}
		// requires — что нужно для входа (пароль, подтверждение в Telegram);
		// password — сгенерированный пароль, которым ещё не входили (только здесь,
		// на сокете владельца: его показывают установщик, gate4ai ui и трей).
		// remote_* — адрес из интернета и его состояние: установщик и агент
		// отдают пользователю https-адрес, а не проброс порта по ssh.
		out := map[string]any{"url": link, "requires": u.Protection().Requires(), "password": u.InitialPassword()}
		for k, v := range u.remoteState(req.Next) {
			out[k] = v
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	// Сброс защиты входа: новый сгенерированный пароль, Telegram выключен,
	// сессии закрыты (`gate4ai ui --reset-password`).
	mux.HandleFunc("POST /v1/ui/reset-login", func(w http.ResponseWriter, r *http.Request) {
		if !daemon.PrincipalFrom(r.Context()).IsOwner() {
			http.Error(w, `{"error":{"code":"owner_only","message":"owner only"}}`, 403)
			return
		}
		pw, err := u.ResetLogin()
		if err != nil {
			http.Error(w, `{"error":{"code":"internal","message":"`+err.Error()+`"}}`, 500)
			return
		}
		u.D.Log.Warn("web UI login protection reset: new password generated, Telegram confirmation off, sessions closed")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"url": u.URL(""), "password": pw})
	})
	mux.HandleFunc("GET /v1/tray/state", func(w http.ResponseWriter, r *http.Request) {
		if !daemon.PrincipalFrom(r.Context()).IsOwner() {
			http.Error(w, `{"error":{"code":"owner_only","message":"owner only"}}`, 403)
			return
		}
		pend, _ := u.D.State.ListApprovals(state.Pending, 20)
		items := []map[string]any{}
		for _, a := range pend {
			items = append(items, map[string]any{"id": a.ID, "agent": a.Agent, "action": a.Action, "hash": audit.Short(a.PreviewHash),
				"summary": strings.SplitN(a.Preview, "\n", 2)[0], "reason": a.Decision.Reason, "expires": a.Expires,
				// web_only — трей не даёт «Разрешить» одним нажатием.
				"web_only": approval.WebOnly(a.Action)})
		}
		_, n := u.D.Config.CurrentRaw()
		seq, _, _ := u.D.Journal.Head()
		bctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		bs, _ := u.D.BrowserStatus(bctx)
		cancel()
		waiting := 0
		for _, s := range bs.Sessions {
			if s.State == "waiting" {
				waiting++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pending": items, "config_version": n, "receipts": seq,
			"browser": len(bs.Sessions), "browser_waiting": waiting,
			// ui_password — сгенерированный пароль Web UI, которым ещё не входили: трей показывает его в меню.
			"ui_password": u.InitialPassword()})
	})
}

func (u *UI) session(m mount, r *http.Request) (string, session, bool) {
	c, err := r.Cookie(m.cookie())
	if err != nil {
		return "", session{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s, ok := u.sessions[c.Value]
	if ok && s.remote != m.remote {
		return "", session{}, false
	}
	if !ok || u.Now().After(s.expires) {
		delete(u.sessions, c.Value)
		return "", session{}, false
	}
	return c.Value, s, true
}

// Handler — HTTP Web UI на loopback.
func (u *UI) Handler() http.Handler { return u.handler(local) }

// RemoteHandler — Web UI для публичного адреса устройства (маршрут /ui/).
func (u *UI) RemoteHandler() http.Handler { return u.handler(remote) }

func (u *UI) handler(m mount) http.Handler {
	mux := http.NewServeMux()
	b := m.base
	get := func(path string, f func(r *http.Request) (string, map[string]any, error)) {
		mux.HandleFunc("GET "+b+path, u.page(m, f))
	}
	post := func(path string, f func(w http.ResponseWriter, r *http.Request) map[string]any) {
		mux.HandleFunc("POST "+b+path, u.post(m, f))
	}
	// Изменения конфигурации, агентов, секретов и коннекторов — только локально.
	change := func(path string, f func(w http.ResponseWriter, r *http.Request) map[string]any) {
		post(path, localOnly(m, f))
	}
	mux.HandleFunc("GET "+b+"/login", u.login(m))
	mux.HandleFunc("POST "+b+"/login", u.loginPost(m))
	mux.Handle("GET "+b+"/static/", http.StripPrefix(b, http.FileServerFS(staticFS)))
	mux.HandleFunc("GET "+b+"/sw.js", serviceWorker)
	mux.HandleFunc("GET "+b+"/lang", u.setLang(m))

	// Удалённо: обзор, подтверждения, журнал (и подписка на push).
	get("/{$}", u.overview(m))
	get("/approvals", u.approvalsPage)
	mux.HandleFunc("GET "+b+"/approvals/live", u.livePending(m))
	get("/journal", u.journal)
	get("/journal/{hash}", u.receipt)
	get("/settings/server", u.serverPage(m)) // и удалённо: только чтение
	get("/browser", u.browserList)
	get("/browser/{id}", u.browserView(m))
	mux.HandleFunc("GET "+b+"/browser/{id}/ws", u.browserWS(m))
	post("/browser/{id}/control", u.browserControl(m))
	post("/browser/{id}/close", u.browserClose)
	post("/browser/{id}/handoff", u.browserHandoff)
	post("/approvals/{id}", u.decide(m))
	post("/questions/{id}", u.answer)
	post("/push/subscribe", u.pushSubscribe(m))
	post("/logout", func(w http.ResponseWriter, r *http.Request) map[string]any {
		c, _ := r.Cookie(m.cookie())
		u.mu.Lock()
		delete(u.sessions, c.Value)
		u.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: m.cookie(), Value: "", Path: b + "/", MaxAge: -1, Secure: m.remote})
		return map[string]any{"redirect": "/login"}
	})

	// Только на этом компьютере: страницы настройки показываются, изменения — нет.
	localPage := func(path string, f func(r *http.Request) (string, map[string]any, error)) {
		if m.remote {
			get(path, func(*http.Request) (string, map[string]any, error) {
				return "remote_only", nil, nil
			})
			return
		}
		get(path, f)
	}
	localPage("/agents", u.agents)
	localPage("/agents/{name}", u.agentPage)
	localPage("/access/browser", u.builtinPage("browser"))
	localPage("/access/files", u.builtinPage("files"))
	localPage("/connectors", u.connectorsPage)
	localPage("/connectors/{name}", u.connectorPage)
	localPage("/knowledge", u.knowledgePage)
	localPage("/policy", u.policyPage)
	localPage("/policy/rule", u.rulePage)
	localPage("/policy/templates", u.templatesPage)
	localPage("/policy/yaml", u.yamlPage)
	localPage("/vault", u.vaultPage)
	localPage("/settings", u.settingsPage)
	localPage("/settings/relay", u.relayPage)
	localPage("/settings/notifications", u.notificationsPage)
	localPage("/settings/login", u.loginSettingsPage)
	localPage("/settings/internet", u.internetPage)
	localPage("/settings/advanced", u.advancedPage)
	localPage("/approvals/telegram", u.telegramPage)
	localPage("/setup", u.setupPage)
	localPage("/setup/access", u.setupAccessPage)
	localPage("/setup/check", u.setupCheck)
	if !m.remote {
		mux.HandleFunc("GET "+b+"/setup/feed", u.setupFeedPart(m))
		mux.HandleFunc("GET "+b+"/setup/skip", u.setupSkip(m))
	}

	change("/setup", u.setupSave)
	change("/setup/code", u.setupCode)
	change("/access/group", u.setGroup)
	change("/access/sites", u.setAccess("sites"))
	change("/access/folders", u.setAccess("folders"))
	change("/connectors/{name}/sites", u.setConnectorAccess("sites"))
	change("/connectors/{name}/folders", u.setConnectorAccess("folders"))
	change("/vault/account/edit", u.editAccount)
	change("/vault/account/delete", u.deleteAccount)
	change("/settings/self", u.saveSelfConfig)
	change("/agents", u.createAgent)
	change("/pairings/{id}", u.decidePairing)
	change("/agents/{name}/token", u.token)
	change("/agents/{name}/connect", u.connect)
	change("/agents/{name}/revoke", u.revoke)
	change("/agents/{name}/delete", u.deleteAgent)
	change("/agents/{name}/profile", u.setAgentProfile)
	change("/agents/{name}/client", u.connectClient)
	change("/agents/{name}/client/remove", u.disconnectClient)
	change("/connectors/install", u.installConnector)
	change("/connectors/refresh", u.refreshIndex)
	change("/knowledge/check", u.checkKnowledge)
	change("/connectors/{name}/settings", u.saveConnectorSettings)
	change("/connectors/{name}/remove", u.removeConnector)
	change("/connectors/{name}/reinstall", u.reinstallConnector)
	change("/connectors/{name}/decision", u.saveActionDecision)
	change("/policy", u.savePolicy)
	change("/policy/rollback", u.rollback)
	change("/policy/simulate", u.simulate)
	change("/policy/rule", u.saveRule)
	change("/policy/rule/delete", u.deleteRule)
	change("/policy/rule/move", u.moveRule)
	change("/policy/template", u.applyTemplate)
	change("/policy/profile", u.createProfile)
	change("/policy/profile/connectors", u.saveProfileConnectors)
	change("/policy/lists", u.saveList)
	change("/vault", u.setSecret)
	change("/vault/delete", u.deleteSecret)
	change("/channels/{name}/toggle", u.toggleChannel)
	change("/channels/{name}/test", u.testChannel)
	change("/channels/telegram", u.telegramStep)
	change("/settings/approvals", u.saveApprovalSettings)
	change("/settings/relay", u.registerRelay)
	change("/settings/relay/toggle", u.toggleRelay)
	change("/settings/autostart", u.toggleAutostart)
	change("/settings/general", u.saveGeneral)
	change("/settings/remote-link", u.remoteLink)
	change("/settings/login/password", u.setLoginPassword)
	change("/settings/login/telegram", u.setLoginTelegram)
	return u.guard(m, mux)
}

// errLocalOnly — действие запрещено в удалённой сессии.
const errLocalOnly = "local_only"

func localOnly(m mount, f func(w http.ResponseWriter, r *http.Request) map[string]any) func(w http.ResponseWriter, r *http.Request) map[string]any {
	if !m.remote {
		return f
	}
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		return map[string]any{"status": http.StatusForbidden, "page": "remote_only", "error": errLocalOnly + ": this change is allowed only on this computer"}
	}
}

// host — ожидаемый Host запроса.
func (u *UI) hostOK(m mount, host string) bool {
	if m.remote {
		pu, err := url.Parse(u.D.PublicURL())
		return err == nil && pu.Host != "" && host == pu.Host
	}
	_, port, _ := net.SplitHostPort(u.Addr)
	return host == u.Addr || host == "localhost:"+port
}

func (u *UI) originOK(m mount, r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	if m.remote {
		return o == "https://"+r.Host
	}
	// Точно свой адрес: страница чужого сервера на localhost:<другой порт>
	// проходила бы проверку (SameSite порт не различает).
	_, port, _ := net.SplitHostPort(u.Addr)
	return o == "http://"+u.Addr || (port != "" && o == "http://localhost:"+port)
}

// guard — проверка Host (защита от DNS rebinding) и заголовки безопасности.
func (u *UI) guard(m mount, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !u.hostOK(m, r.Host) {
			http.Error(w, "bad host", http.StatusMisdirectedRequest)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
		// same-origin, а не no-referrer: при no-referrer браузер шлёт в формах Origin: null,
		// и проверка Origin отклоняет любое действие.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// Куда вернуться после входа (cookie <сессия>_next): только раздел «Браузер»
// и просмотр сеанса — ссылка view_url, которую агент дал человеку.

// safeNext — адрес возврата: только /browser/<id> этого Web UI.
func safeNext(p string) string {
	if p == "/browser" {
		return p
	}
	id, ok := strings.CutPrefix(p, "/browser/")
	if !ok || id == "" || strings.ContainsAny(id, "/\\?#%") {
		return ""
	}
	return p
}

func setNext(w http.ResponseWriter, m mount, p string) {
	if p = safeNext(p); p != "" {
		http.SetCookie(w, &http.Cookie{Name: m.cookie() + "_next", Value: p, Path: m.base + "/", HttpOnly: true, Secure: m.remote,
			SameSite: http.SameSiteLaxMode, MaxAge: 600})
	}
}

// takeNext — адрес после входа: из cookie, иначе «/».
func (u *UI) takeNext(w http.ResponseWriter, r *http.Request, m mount) string {
	p := ""
	if c, err := r.Cookie(m.cookie() + "_next"); err == nil {
		p = safeNext(c.Value)
		http.SetCookie(w, &http.Cookie{Name: m.cookie() + "_next", Value: "", Path: m.base + "/", MaxAge: -1, Secure: m.remote})
	}
	if p == "" {
		return "/"
	}
	return p
}

// login — страница входа: продолжение попытки входа этого браузера или её начало.
func (u *UI) login(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		id, a := u.attemptOf(m, r)
		u.mu.Unlock()
		if a != nil {
			u.loginStep(w, r, m, id, "")
			return
		}
		u.loginPage(w, r, m)
	}
}

type view struct {
	Base             string // "" или "/ui"
	Remote           bool
	Lang             string // язык страницы (en, ru)
	Path             string // адрес страницы (переключатель языка возвращает сюда)
	Title, Nav, CSRF string
	Flash, Error     string
	Detail           string // подробности сообщения (пути, исходный текст ошибки)
	Data             map[string]any
	// Счётчик в меню.
	Pending int
}

// section — пункт главного меню, к которому относится страница.
func (v view) Section() string {
	switch v.Nav {
	case "overview", "approvals", "telegram", "setup":
		return "home"
	case "agents", "agent", "agent_new":
		return "agents"
	case "connectors", "connector", "builtin", "knowledge", "policy", "rule", "templates", "policy_yaml":
		return "access"
	case "journal", "receipt":
		return "journal"
	case "browser", "browser_view":
		return "browser"
	case "vault":
		return "vault"
	case "settings", "relay", "server":
		return "settings"
	}
	return ""
}

func (u *UI) counts(v *view) {
	if p, err := u.D.State.ListApprovals(state.Pending, 100); err == nil {
		v.Pending = len(p)
	}
}

// popNote — сообщение после действия для этой сессии (показывается один раз).
func (u *UI) popNote(sid string) (note, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	n, ok := u.notes[sid]
	delete(u.notes, sid)
	return n, ok
}

func (u *UI) putNote(sid string, n note) {
	if sid == "" || (n.flash == nil && n.err == nil) {
		return
	}
	u.mu.Lock()
	u.notes[sid] = n
	u.mu.Unlock()
}

// render — страница name на языке запроса.
func (u *UI) render(w http.ResponseWriter, r *http.Request, name string, v view, status int) {
	v.Lang = u.lang(r)
	v.Path = r.URL.RequestURI()
	if r.Method != http.MethodGet {
		v.Path = v.Base + "/"
	}
	u.counts(&v)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != 0 {
		w.WriteHeader(status)
	}
	if err := tplIn(v.Lang).ExecuteTemplate(w, name+".html", v); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// fill — сообщения страницы: только из сессии (после действия). Из адреса
// (?flash=, ?error=) текст не берётся: по ссылке владельцу нельзя показать
// произвольный «системный» текст.
func (u *UI) fill(v *view, r *http.Request, sid string) {
	l := u.lang(r)
	if n, ok := u.popNote(sid); ok {
		v.Flash, v.Detail = say(l, n.flash), n.detail
		if n.err != nil {
			v.Error = say(l, n.err)
		}
	}
}

func (u *UI) page(m mount, f func(r *http.Request) (string, map[string]any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid, s, ok := u.session(m, r)
		if !ok {
			// Ссылка на сеанс браузера без входа: после входа — на неё же.
			setNext(w, m, strings.TrimPrefix(r.URL.Path, m.base))
			u.loginPage(w, r, m)
			return
		}
		name, data, err := f(r)
		if red, ok := data["redirect"].(string); ok && name == "" {
			http.Redirect(w, r, m.base+red, http.StatusSeeOther)
			return
		}
		v := view{Base: m.base, Remote: m.remote, Nav: name, CSRF: s.csrf, Data: data}
		u.fill(&v, r, sid)
		if err != nil {
			v.Error = say(u.lang(r), err)
		}
		u.render(w, r, name, v, 0)
	}
}

// post — действие формы: сессия, CSRF-токен, Origin; результат — редирект или страница.
// Сообщение редиректа (flash, error) запоминается в сессии и показывается на
// следующей странице — в адресе его нет.
func (u *UI) post(m mount, f func(w http.ResponseWriter, r *http.Request) map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid, s, ok := u.session(m, r)
		if !ok {
			http.Error(w, "login required", 401)
			return
		}
		if !u.originOK(m, r) {
			http.Error(w, "bad origin", 403)
			return
		}
		_ = r.ParseForm()
		tok := r.PostForm.Get("csrf")
		if h := r.Header.Get("X-CSRF-Token"); h != "" {
			tok = h
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.csrf)) != 1 {
			http.Error(w, "bad csrf token", 403)
			return
		}
		res := f(w, r)
		if res == nil { // ответ уже записан (JSON для app.js)
			return
		}
		detail, _ := res["detail"].(string)
		if red, ok := res["redirect"].(string); ok {
			u.putNote(sid, note{flash: res["flash"], err: res["error"], detail: detail})
			http.Redirect(w, r, m.base+localPath(red), http.StatusSeeOther)
			return
		}
		name, _ := res["page"].(string)
		l := u.lang(r)
		v := view{Base: m.base, Remote: m.remote, Nav: name, CSRF: s.csrf, Data: res, Detail: detail}
		if e, ok := res["error"]; ok && e != nil {
			v.Error = say(l, e)
		}
		if fl, ok := res["flash"]; ok && fl != nil {
			v.Flash = say(l, fl)
		}
		st, _ := res["status"].(int)
		u.render(w, r, name, v, st)
	}
}

// setLang — переключатель языка: cookie на год и возврат на ту же страницу.
func (u *UI) setLang(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := r.URL.Query().Get("l")
		if supported(l) {
			http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: m.base + "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, Secure: m.remote})
		}
		back := r.URL.Query().Get("back")
		// Только путь этого же Web UI: открытый редирект на чужой сайт не нужен.
		if !strings.HasPrefix(back, m.base+"/") || strings.HasPrefix(back, "//") || strings.Contains(back, "\\") {
			back = m.base + "/"
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
}

// localPath — адрес перехода внутри Web UI: путь от корня, без «//host» и
// «\» (back приходит из формы — открытый редирект недопустим).
func localPath(to string) string {
	if !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.ContainsAny(to, "\\\r\n") {
		return "/"
	}
	return to
}

var owner = auth.OwnerVia("webui")
