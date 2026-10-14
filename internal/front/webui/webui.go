// SPDX-License-Identifier: FSL-1.1-ALv2

// Package webui — Web UI владельца на localhost: подтверждения, агенты (токены,
// коды подключения, отзыв), журнал, базовый редактор политик с симулятором и
// откатом, алиасы Vault. Вход — одноразовая ссылка, которую выдаёт daemon по
// сокету владельца (`gate4ai ui`, трей); дальше — сессия в HttpOnly-cookie.
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
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/state"
)

//go:embed templates/*.html
var tplFS embed.FS

var funcs = template.FuncMap{
	"short": audit.Short,
	"ts":    func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
	"json": func(v any) string {
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	},
	"join": strings.Join,
}

var tpl = template.Must(template.New("").Funcs(funcs).ParseFS(tplFS, "templates/*.html"))

// UI — Web UI.
type UI struct {
	D    *daemon.Daemon
	Addr string // 127.0.0.1:port после Listen
	Now  func() time.Time

	mu       sync.Mutex
	logins   map[string]login // одноразовые токены входа
	sessions map[string]session
}

type login struct {
	expires time.Time
	remote  bool
}

type session struct {
	csrf    string
	expires time.Time
	remote  bool
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
	return &UI{D: d, Now: time.Now, logins: map[string]login{}, sessions: map[string]session{}}
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

// RemoteLoginURL — одноразовая ссылка входа в удалённый Web UI (2 минуты).
func (u *UI) RemoteLoginURL() (string, error) {
	pub := u.D.PublicURL()
	if pub == "" {
		return "", fmt.Errorf("no public address: register the device on a relay or start the daemon with --public")
	}
	return pub + remote.base + "/login?t=" + u.newLogin(true), nil
}

func (u *UI) newLogin(rem bool) string {
	t := random()
	u.mu.Lock()
	u.logins[t] = login{expires: u.Now().Add(2 * time.Minute), remote: rem}
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
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "no_public_address", "message": err.Error()}})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": link})
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
	mux.HandleFunc("GET "+b+"/login", u.login(m))
	mux.HandleFunc("GET "+b+"/{$}", u.page(m, u.approvals))
	mux.HandleFunc("GET "+b+"/agents", u.page(m, u.agents))
	mux.HandleFunc("GET "+b+"/journal", u.page(m, u.journal))
	mux.HandleFunc("GET "+b+"/journal/{hash}", u.page(m, u.receipt))
	mux.HandleFunc("GET "+b+"/policy", u.page(m, u.policyPage))
	mux.HandleFunc("GET "+b+"/vault", u.page(m, u.vaultPage))
	mux.HandleFunc("POST "+b+"/approvals/{id}", u.post(m, u.decide(m)))
	mux.HandleFunc("POST "+b+"/policy/simulate", u.post(m, u.simulate))
	// Изменения политик, агентов и секретов — только локально.
	mux.HandleFunc("POST "+b+"/agents/{name}/token", u.post(m, localOnly(m, u.token)))
	mux.HandleFunc("POST "+b+"/agents/{name}/connect", u.post(m, localOnly(m, u.connect)))
	mux.HandleFunc("POST "+b+"/agents/{name}/revoke", u.post(m, localOnly(m, u.revoke)))
	mux.HandleFunc("POST "+b+"/policy", u.post(m, localOnly(m, u.savePolicy)))
	mux.HandleFunc("POST "+b+"/policy/rollback", u.post(m, localOnly(m, u.rollback)))
	mux.HandleFunc("POST "+b+"/vault", u.post(m, localOnly(m, u.setSecret)))
	mux.HandleFunc("POST "+b+"/logout", u.post(m, func(w http.ResponseWriter, r *http.Request) map[string]any {
		c, _ := r.Cookie(m.cookie())
		u.mu.Lock()
		delete(u.sessions, c.Value)
		u.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: m.cookie(), Value: "", Path: b + "/", MaxAge: -1, Secure: m.remote})
		return map[string]any{"redirect": "/login"}
	}))
	return u.guard(m, mux)
}

// errLocalOnly — действие запрещено в удалённой сессии.
const errLocalOnly = "local_only"

func localOnly(m mount, f func(w http.ResponseWriter, r *http.Request) map[string]any) func(w http.ResponseWriter, r *http.Request) map[string]any {
	if !m.remote {
		return f
	}
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		return map[string]any{"status": http.StatusForbidden, "page": "approvals", "error": errLocalOnly + ": this change is allowed only on this computer"}
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (u *UI) login(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := r.URL.Query().Get("t")
		u.mu.Lock()
		l, ok := u.logins[t]
		if ok && l.remote == m.remote {
			delete(u.logins, t)
		}
		u.mu.Unlock()
		if t == "" || !ok || l.remote != m.remote || u.Now().After(l.expires) {
			loginPage(w, m)
			return
		}
		sid := random()
		u.mu.Lock()
		u.sessions[sid] = session{csrf: random(), expires: u.Now().Add(12 * time.Hour), remote: m.remote}
		u.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: m.cookie(), Value: sid, Path: m.base + "/", HttpOnly: true, Secure: m.remote,
			SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
		http.Redirect(w, r, m.base+"/", http.StatusSeeOther)
	}
}

func loginPage(w http.ResponseWriter, m mount) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(401)
	_ = tpl.ExecuteTemplate(w, "login.html", view{Base: m.base, Remote: m.remote})
}

type view struct {
	Base             string // "" или "/ui"
	Remote           bool
	Title, Nav, CSRF string
	Flash, Error     string
	Data             map[string]any
}

func (u *UI) page(m mount, f func(r *http.Request) (string, map[string]any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, s, ok := u.session(m, r)
		if !ok {
			loginPage(w, m)
			return
		}
		name, data, err := f(r)
		v := view{Base: m.base, Remote: m.remote, Nav: name, CSRF: s.csrf, Data: data, Flash: r.URL.Query().Get("flash")}
		if err != nil {
			v.Error = err.Error()
		}
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
		if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(s.csrf)) != 1 {
			http.Error(w, "bad csrf token", 403)
			return
		}
		res := f(w, r)
		if red, ok := res["redirect"].(string); ok {
			http.Redirect(w, r, m.base+red, http.StatusSeeOther)
			return
		}
		name, _ := res["page"].(string)
		v := view{Base: m.base, Remote: m.remote, Nav: name, CSRF: s.csrf, Data: res}
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

func (u *UI) approvals(r *http.Request) (string, map[string]any, error) {
	pend, err := u.D.State.ListApprovals(state.Pending, 50)
	recent, _ := u.D.State.ListApprovals("", 20)
	return "approvals", map[string]any{"Pending": pend, "Recent": recent}, err
}

// decide — решение по запросу; канал в квитанции различает локальный и удалённый вход.
func (u *UI) decide(m mount) func(w http.ResponseWriter, r *http.Request) map[string]any {
	channel := "webui"
	if m.remote {
		channel = "webui-remote"
	}
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		res, err := u.D.Pipeline.Decide(r.Context(), r.PathValue("id"), r.PostForm.Get("decision") == "approve", channel, "owner")
		if err != nil {
			return map[string]any{"redirect": "/?flash=" + urlq(err.Error())}
		}
		return map[string]any{"redirect": "/?flash=" + urlq(res.Status+" "+res.Receipt)}
	}
}

func (u *UI) agents(r *http.Request) (string, map[string]any, error) {
	toks, err := u.D.State.Tokens()
	return "agents", map[string]any{"Agents": u.D.Config.Current().Agents, "Tokens": toks}, err
}

func (u *UI) agentsWith(extra map[string]any) map[string]any {
	_, d, _ := u.agents(nil)
	for k, v := range extra {
		d[k] = v
	}
	d["page"] = "agents"
	return d
}

func (u *UI) token(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	if _, ok := u.D.Config.Current().Agents[name]; !ok {
		return u.agentsWith(map[string]any{"error": "unknown agent " + name})
	}
	tok, err := u.D.State.IssueToken(name, r.PostForm.Get("label"), "agent")
	if err != nil {
		return u.agentsWith(map[string]any{"error": err.Error()})
	}
	return u.agentsWith(map[string]any{"NewToken": tok, "NewTokenAgent": name})
}

func (u *UI) connect(w http.ResponseWriter, r *http.Request) map[string]any {
	code, exp, err := u.D.EnableOAuth(nil).NewConnectCode(r.PathValue("name"))
	if err != nil {
		return u.agentsWith(map[string]any{"error": err.Error()})
	}
	return u.agentsWith(map[string]any{"ConnectCode": code, "ConnectAgent": r.PathValue("name"), "ConnectExpires": exp})
}

func (u *UI) revoke(w http.ResponseWriter, r *http.Request) map[string]any {
	n, err := u.D.State.RevokeAgent(r.PathValue("name"))
	if err != nil {
		return u.agentsWith(map[string]any{"error": err.Error()})
	}
	return map[string]any{"redirect": "/agents?flash=" + urlq(fmt.Sprintf("revoked %d token(s) of %s", n, r.PathValue("name")))}
}

func (u *UI) journal(r *http.Request) (string, map[string]any, error) {
	q := r.URL.Query()
	es, err := u.D.Journal.List(audit.Filter{Agent: q.Get("agent"), Action: q.Get("action"), Effect: q.Get("effect"), Limit: 200, Desc: true})
	verr := u.D.Journal.VerifyChain()
	chain := "ok"
	if verr != nil {
		chain = verr.Error()
	}
	return "journal", map[string]any{"Entries": es, "Q": q, "Chain": chain}, err
}

func (u *UI) receipt(r *http.Request) (string, map[string]any, error) {
	e, err := u.D.Journal.Find(r.PathValue("hash"))
	return "receipt", map[string]any{"Entry": e}, err
}

func (u *UI) policyData(yaml string) map[string]any {
	raw, n := u.D.Config.CurrentRaw()
	if yaml == "" {
		yaml = string(raw)
	}
	vs, _ := u.D.Config.Versions()
	var actions []string
	for _, t := range u.D.Tools(owner) {
		actions = append(actions, t.Name)
	}
	return map[string]any{"YAML": yaml, "Version": n, "Versions": vs, "Agents": u.D.Config.Current().Agents, "Actions": actions, "page": "policy"}
}

func (u *UI) policyPage(r *http.Request) (string, map[string]any, error) {
	return "policy", u.policyData(""), nil
}

func (u *UI) savePolicy(w http.ResponseWriter, r *http.Request) map[string]any {
	y := r.PostForm.Get("yaml")
	v, err := u.D.Config.Put(owner, []byte(y), r.PostForm.Get("comment"))
	if err != nil {
		d := u.policyData(y)
		d["error"] = err.Error()
		return d
	}
	return map[string]any{"redirect": "/policy?flash=" + urlq("saved version "+strconv.Itoa(v.N))}
}

func (u *UI) rollback(w http.ResponseWriter, r *http.Request) map[string]any {
	n, _ := strconv.Atoi(r.PostForm.Get("version"))
	v, err := u.D.Config.Rollback(owner, n)
	if err != nil {
		d := u.policyData("")
		d["error"] = err.Error()
		return d
	}
	return map[string]any{"redirect": "/policy?flash=" + urlq(fmt.Sprintf("rolled back to %d (new version %d)", n, v.N))}
}

// simulate — «что будет, если агент попробует…».
func (u *UI) simulate(w http.ResponseWriter, r *http.Request) map[string]any {
	d := u.policyData(r.PostForm.Get("yaml"))
	agent, action := r.PostForm.Get("agent"), r.PostForm.Get("action")
	var args map[string]any
	if err := json.Unmarshal([]byte(r.PostForm.Get("args")), &args); err != nil {
		d["error"] = "arguments must be a JSON object: " + err.Error()
		return d
	}
	eng := u.D.Pipeline.Engine()
	if y := r.PostForm.Get("yaml"); y != "" {
		cfg, err := u.D.Config.Validate([]byte(y))
		if err == nil {
			eng, err = policy.New(cfg, policy.Catalog(u.D.Registry.Catalog()))
		}
		if err != nil {
			d["error"] = err.Error()
			return d
		}
	}
	_, act, ok := u.D.Registry.Lookup(action)
	if !ok {
		d["error"] = "unknown action " + action
		return d
	}
	nargs, err := act.Normalize(args)
	if err != nil {
		d["error"] = err.Error()
		return d
	}
	dec := eng.Decide(policy.Request{Agent: agent, Action: action, Class: act.Class, Args: nargs, Now: u.Now()}, u.D.State.Counters())
	d["Sim"] = dec
	d["SimAgent"], d["SimAction"], d["SimArgs"] = agent, action, r.PostForm.Get("args")
	return d
}

func (u *UI) vaultPage(r *http.Request) (string, map[string]any, error) {
	return "vault", map[string]any{"Aliases": u.D.Vault.Aliases()}, nil
}

func (u *UI) setSecret(w http.ResponseWriter, r *http.Request) map[string]any {
	if err := u.D.Vault.Set(r.PostForm.Get("alias"), r.PostForm.Get("value")); err != nil {
		return map[string]any{"page": "vault", "Aliases": u.D.Vault.Aliases(), "error": err.Error()}
	}
	return map[string]any{"redirect": "/vault?flash=" + urlq("saved "+r.PostForm.Get("alias"))}
}

func urlq(s string) string { return template.URLQueryEscaper(s) }
