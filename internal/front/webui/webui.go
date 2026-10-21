// SPDX-License-Identifier: AGPL-3.0-only

// Package webui — Web UI владельца на localhost: подтверждения, агенты (токены,
// коды подключения, отзыв), журнал, базовый редактор политик с симулятором и
// откатом, алиасы Vault. Вход — одноразовая ссылка, которую выдаёт daemon по
// сокету владельца (`gate4ai ui`, трей); дальше — сессия в HttpOnly-cookie. Если в
// Настройках включена защита входа (protect.go), ссылка сама сессию не даёт: нужны
// пароль и/или подтверждение входа в Telegram-боте.
// Защита: проверка Host (DNS rebinding), SameSite=Strict, CSRF-токен в формах.
//
// Удалённо Web UI доступен на публичном адресе устройства под /ui/ (RemoteHandler):
// своя ссылка входа (`gate4ai ui --remote`) и своя cookie, локальная сессия там
// не действует и наоборот; изменения политик, агентов и секретов — только локально.
package webui

import (
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
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/approval"
	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
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
	"join":     strings.Join,
	"ago":      ago,
	"effectRu": func(e string) string { return or(effectRu[e], e) },
	"classRu":  func(c string) string { return or(classRu[c], c) },
	"viaRu":    func(v string) string { return or(viaRu[v], v) },
	"describe": func(w string) string { s, _ := DescribeWhen(w); return s },
	"isCode":   func(w string) bool { _, ok := DescribeWhen(w); return !ok },
	"has": func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	},
	"capRu":     humanCapability,
	"errView":   errView,
	"accAnchor": AccountAnchor,
	"secAnchor": SecretAnchor,
	"title":     func(nav string) string { return or(titles[nav], nav) },
	"inc":       func(i int) int { return i + 1 },
	"list":      func(s ...string) []string { return s },
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"lines": func(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") },
	"first": func(s string) string { return strings.SplitN(s, "\n", 2)[0] },
}

var tpl = template.Must(template.New("").Funcs(funcs).ParseFS(tplFS, "templates/*.html"))

var titles = map[string]string{"overview": "Обзор", "approvals": "Подтверждения", "telegram": "Telegram", "agents": "Агенты", "agent": "Агент",
	"agent_new": "Новый агент", "connectors": "Коннекторы", "connector": "Коннектор", "policy": "Правила", "rule": "Правило", "templates": "Шаблоны",
	"policy_yaml": "YAML", "journal": "Журнал", "receipt": "Квитанция", "vault": "Секреты", "settings": "Настройки", "relay": "Relay",
	"remote_only": "Только на компьютере"}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ago — «3 мин назад».
func ago(t time.Time) string {
	if t.IsZero() {
		return "никогда"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "только что"
	case d < time.Hour:
		return fmt.Sprintf("%d мин назад", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч назад", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d дн назад", int(d.Hours()/24))
	}
	return t.Local().Format("2006-01-02")
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
	// Bin — бинарник gate4ai для конфигурации клиентов ("" — os.Executable).
	Bin string
	// Autostart — включение автозапуска (подменяется в тестах).
	Autostart Autostart
	// Confirm — подтверждение входа на другом устройстве (nil — Telegram-бот из Channels).
	Confirm LoginConfirmer

	mu       sync.Mutex
	logins   map[string]login // одноразовые токены входа
	sessions map[string]session
	attempts map[string]*attempt // попытки входа при включённой защите
	limits   [2]limiter          // защита от перебора пароля: локально, удалённо
}

type login struct {
	expires time.Time
	remote  bool
}

type session struct {
	csrf      string
	expires   time.Time
	remote    bool
	protected bool // вход прошёл защиту (пароль и/или Telegram)
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
	return &UI{D: d, Now: time.Now, logins: map[string]login{}, sessions: map[string]session{}, attempts: map[string]*attempt{},
		Clients: agentsetup.Default(), Autostart: systemAutostart{}}
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// LoginURL — одноразовая ссылка входа (2 минуты).
func (u *UI) LoginURL() string {
	return "http://" + u.Addr + "/login?t=" + u.newLogin(false)
}

// errRelayOffline — адрес устройства на relay есть, но туннеля нет: ссылка не
// откроется (relay подержит соединение и сбросит его, браузер покажет таймаут).
var errRelayOffline = errors.New("the device is not connected to the relay, a link would not open")

// RemoteLoginURL — одноразовая ссылка входа в удалённый Web UI (2 минуты).
func (u *UI) RemoteLoginURL() (string, error) {
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
	return pub + remote.base + "/login?t=" + u.newLogin(true), nil
}

// loginTTL — срок одноразовой ссылки входа.
const loginTTL = 2 * time.Minute

func (u *UI) newLogin(rem bool) string {
	t := random()
	u.mu.Lock()
	u.logins[t] = login{expires: u.Now().Add(loginTTL), remote: rem}
	u.mu.Unlock()
	return t
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

// OwnerRoutes — маршрут сокета владельца, выдающий ссылку входа (трей, `gate4ai ui`).
func (u *UI) OwnerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ui/session", func(w http.ResponseWriter, r *http.Request) {
		if !daemon.PrincipalFrom(r.Context()).IsOwner() {
			http.Error(w, `{"error":{"code":"owner_only","message":"owner only"}}`, 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Remote bool `json:"remote"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		link := u.LoginURL()
		if req.Remote {
			var err error
			if link, err = u.RemoteLoginURL(); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(409)
				code := "no_public_address"
				if errors.Is(err, errRelayOffline) {
					code = "relay_offline"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
				return
			}
		}
		// requires — что ещё нужно для входа по ссылке (пароль, подтверждение в Telegram);
		// link_required — нужна ли ссылка вообще (иначе вход и по постоянному адресу).
		p := u.Protection()
		_ = json.NewEncoder(w).Encode(map[string]any{"url": link, "requires": p.Requires(), "link_required": !p.NoLink})
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
				"summary": strings.SplitN(a.Preview, "\n", 2)[0], "reason": a.Decision.Reason, "expires": a.Expires})
		}
		_, n := u.D.Config.CurrentRaw()
		seq, _, _ := u.D.Journal.Head()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pending": items, "config_version": n, "receipts": seq})
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

	// Удалённо: обзор, подтверждения, журнал (и подписка на push).
	get("/{$}", u.overview(m))
	get("/approvals", u.approvalsPage(m))
	mux.HandleFunc("GET "+b+"/approvals/live", u.livePending(m))
	get("/journal", u.journal)
	get("/journal/{hash}", u.receipt)
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
	localPage("/agents/new", u.agentNew)
	localPage("/agents/{name}", u.agentPage)
	localPage("/connectors", u.connectorsPage)
	localPage("/connectors/{name}", u.connectorPage)
	localPage("/policy", u.policyPage)
	localPage("/policy/rule", u.rulePage)
	localPage("/policy/templates", u.templatesPage)
	localPage("/policy/yaml", u.yamlPage)
	localPage("/vault", u.vaultPage)
	localPage("/settings", u.settingsPage)
	localPage("/settings/relay", u.relayPage)
	localPage("/approvals/telegram", u.telegramPage)

	change("/agents", u.createAgent)
	change("/agents/{name}/token", u.token)
	change("/agents/{name}/connect", u.connect)
	change("/agents/{name}/revoke", u.revoke)
	change("/agents/{name}/delete", u.deleteAgent)
	change("/agents/{name}/profile", u.setAgentProfile)
	change("/agents/{name}/client", u.connectClient)
	change("/agents/{name}/client/remove", u.disconnectClient)
	change("/connectors/install", u.installConnector)
	change("/connectors/refresh", u.refreshIndex)
	change("/connectors/{name}/settings", u.saveConnectorSettings)
	change("/connectors/{name}/profiles", u.saveConnectorProfiles)
	change("/connectors/{name}/remove", u.removeConnector)
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
	change("/settings/autostart", u.toggleAutostart)
	change("/settings/general", u.saveGeneral)
	change("/settings/remote-link", u.remoteLink)
	change("/settings/login/password", u.setLoginPassword)
	change("/settings/login/telegram", u.setLoginTelegram)
	change("/settings/login/link", u.setLoginLink)
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
	return o == "http://"+u.Addr || strings.HasPrefix(o, "http://localhost:")
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

// login — одноразовая ссылка. Без защиты она сразу даёт сессию; с защитой —
// попытку входа (cookie), которой нужны пароль и/или подтверждение в Telegram.
func (u *UI) login(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("t"); t != "" {
			u.mu.Lock()
			l, ok := u.logins[t]
			if ok && l.remote == m.remote {
				delete(u.logins, t)
			}
			valid := ok && l.remote == m.remote && !u.Now().After(l.expires)
			u.mu.Unlock()
			if valid {
				p := u.Protection()
				if !p.Extra() {
					u.startSession(w, m, false)
					http.Redirect(w, r, m.base+"/", http.StatusSeeOther)
					return
				}
				u.mu.Lock()
				id := u.newAttempt(m, p)
				u.mu.Unlock()
				u.setAttemptCookie(w, m, id)
				// Без токена в адресе: дальше попытка живёт только в cookie этого браузера.
				http.Redirect(w, r, m.base+"/login", http.StatusSeeOther)
				return
			}
		}
		u.mu.Lock()
		id, a := u.attemptOf(m, r)
		u.mu.Unlock()
		if a != nil {
			u.loginStep(w, r, m, id, "")
			return
		}
		u.loginPage(w, m)
	}
}

type view struct {
	Base             string // "" или "/ui"
	Remote           bool
	Title, Nav, CSRF string
	Flash, Error     string
	Data             map[string]any
	// Счётчик в меню.
	Pending int
}

func (u *UI) counts(v *view) {
	if p, err := u.D.State.ListApprovals(state.Pending, 100); err == nil {
		v.Pending = len(p)
	}
}

func (u *UI) page(m mount, f func(r *http.Request) (string, map[string]any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, s, ok := u.session(m, r)
		if !ok {
			u.loginPage(w, m)
			return
		}
		name, data, err := f(r)
		v := view{Base: m.base, Remote: m.remote, Nav: name, CSRF: s.csrf, Data: data, Flash: r.URL.Query().Get("flash")}
		if err != nil {
			v.Error = err.Error()
		}
		if e := r.URL.Query().Get("error"); e != "" && v.Error == "" {
			v.Error = e
		}
		u.counts(&v)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tpl.ExecuteTemplate(w, name+".html", v); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// post — действие формы: сессия, CSRF-токен, Origin; результат — редирект или страница.
func (u *UI) post(m mount, f func(w http.ResponseWriter, r *http.Request) map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, s, ok := u.session(m, r)
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
		if red, ok := res["redirect"].(string); ok {
			http.Redirect(w, r, m.base+red, http.StatusSeeOther)
			return
		}
		name, _ := res["page"].(string)
		v := view{Base: m.base, Remote: m.remote, Nav: name, CSRF: s.csrf, Data: res}
		u.counts(&v)
		if e, ok := res["error"].(string); ok {
			v.Error = e
		}
		if fl, ok := res["flash"].(string); ok {
			v.Flash = fl
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if st, ok := res["status"].(int); ok {
			w.WriteHeader(st)
		}
		if err := tpl.ExecuteTemplate(w, name+".html", v); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

var owner = auth.OwnerVia("webui")

func urlq(s string) string { return template.URLQueryEscaper(s) }
