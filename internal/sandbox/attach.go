// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/skill"
	"github.com/gate4ai/gate4ai/internal/hostsvc/egress"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Runs — запуски агентов в песочнице и Egress Proxy (часть daemon).
type Runs struct {
	D       *daemon.Daemon
	RunDir  string
	Proxy   *egress.Proxy
	CA      *egress.CA
	Addr    *net.TCPAddr
	AskWait time.Duration

	mu   sync.Mutex
	runs map[string]*runState
}

type runState struct {
	agent, token, pass, sock string
	srv                      *http.Server
	// placeholders — одноразовые заглушки запуска: env → заглушка.
	placeholders map[string]string
	seen         time.Time // последний keepalive запуска
}

// RunTTL — срок токена и учётных данных прокси запуска без keepalive от
// `gate4ai run` (если CLI убит, запуск гаснет сам); KeepAliveEvery — как часто
// CLI продлевает запуск.
var RunTTL = 30 * time.Minute

const KeepAliveEvery = 5 * time.Minute

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Attach запускает Egress Proxy на listen (loopback, с аутентификацией запуска)
// и маршруты владельца /v1/runs, /v1/egress/log.
func Attach(ctx context.Context, d *daemon.Daemon, runDir, listen string) (*Runs, error) {
	ca, err := egress.NewCA("gate4ai egress (local)")
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	r := &Runs{D: d, RunDir: runDir, CA: ca, Addr: l.Addr().(*net.TCPAddr), AskWait: 2 * time.Minute, runs: map[string]*runState{}}
	r.Proxy = &egress.Proxy{Auth: r.auth, Decide: r.decide, Secrets: r.secrets, AllowLocal: r.allowLocal, CA: ca,
		Log: func(rec egress.Record) { _ = d.State.AddEgress(rec.TS, rec) }}
	srv := &http.Server{Handler: r.Proxy, ReadHeaderTimeout: 30 * time.Second}
	go func() { <-ctx.Done(); srv.Close(); r.Proxy.Close(); r.closeAll() }()
	go srv.Serve(l)
	// Контейнеры видят хост как host.docker.internal (шлюз docker0): тот же порт там.
	if ip := bridgeIP(); ip != nil && r.Addr.IP.IsLoopback() {
		if bl, err := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(r.Addr.Port))); err == nil {
			go srv.Serve(bl)
		}
	}
	d.AddRoutes(r.routes)
	return r, nil
}

func (r *Runs) auth(user, pass string) (agent, run string, ok bool) {
	id, ok := strings.CutPrefix(user, "run-")
	if !ok {
		return "", "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.runs[id]
	if !ok || subtle.ConstantTimeCompare([]byte(st.pass), []byte(pass)) != 1 || time.Since(st.seen) > RunTTL {
		return "", "", false
	}
	return st.agent, id, true
}

// decide: allow_domains профиля — сразу; ask_domains — через pipeline
// (egress.connect: политика, подтверждение человеком, квитанция); остальное — deny.
func (r *Runs) decide(ctx context.Context, agent, host string, port int) (bool, string) {
	prof, ok := r.D.Config.Current().ProfileOf(agent)
	if !ok {
		return false, "unknown agent"
	}
	if policy.DomainMatches(host, prof.Sandbox.AllowDomains) {
		return true, "allow_domains"
	}
	if !policy.DomainMatches(host, prof.Sandbox.AskDomains) {
		return false, "not in sandbox.allow_domains of profile"
	}
	res := r.D.Pipeline.Execute(ctx, pipeline.Call{Agent: agent, Action: "egress.connect", Internal: true, Wait: r.AskWait,
		Args: map[string]any{"host": host, "port": float64(port)}})
	if res.Status == pipeline.StatusOK {
		return true, "approved " + res.Receipt
	}
	return false, res.Code + ": " + res.Reason
}

// secrets — подстановки для агента и origin https://host:port: domain
// подстановки — точный хост (origin https://host:443) или точный https-origin
// ("https://api.example.com:8443"), без wildcard; agents, если заданы, —
// только этим агентам.
func (r *Runs) secrets(run, host string, port int) []egress.Injection {
	r.mu.Lock()
	st, ok := r.runs[run]
	var agent string
	var phs map[string]string
	if ok {
		agent, phs = st.agent, st.placeholders
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	var out []egress.Injection
	for _, s := range r.D.Config.Current().Egress.Secrets {
		if len(s.Agents) > 0 && !slices.Contains(s.Agents, agent) {
			continue
		}
		// С env — только заглушка этого запуска; без env — постоянная из
		// конфигурации (совместимость).
		ph := s.Placeholder
		if s.Env != "" {
			if ph = phs[s.Env]; ph == "" {
				continue
			}
		}
		if h, p, ok := egressOrigin(s.Domain); !ok || h != host || p != port {
			continue
		}
		v, err := r.D.Vault.Get(s.Secret)
		if err != nil {
			continue
		}
		out = append(out, egress.Injection{Header: s.Header, Placeholder: ph, Value: v})
	}
	return out
}

// egressOrigin — хост и порт HTTPS-origin подстановки ("api.example.com" —
// порт 443; "https://api.example.com:8443"). Wildcard и не-https — не origin.
func egressOrigin(d string) (string, int, bool) {
	d = strings.TrimSpace(d)
	if !strings.Contains(d, "://") {
		d = "https://" + d
	}
	o, err := domain.BindOrigin(d)
	if err != nil || !strings.HasPrefix(o, "https://") || strings.Contains(o, "*") {
		return "", 0, false
	}
	u, err := url.Parse(o)
	if err != nil {
		return "", 0, false
	}
	port := 443
	if ps := u.Port(); ps != "" {
		port, _ = strconv.Atoi(ps)
	}
	return u.Hostname(), port, true
}

// allowLocal — адрес этой машины или частной сети host явно указан (без
// wildcard) в allow_domains или ask_domains профиля агента.
func (r *Runs) allowLocal(agent, host string) bool {
	prof, ok := r.D.Config.Current().ProfileOf(agent)
	if !ok {
		return false
	}
	for _, d := range append(slices.Clone(prof.Sandbox.AllowDomains), prof.Sandbox.AskDomains...) {
		if !strings.Contains(d, "*") && strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(d), "."), host) {
			return true
		}
	}
	return false
}

func (r *Runs) routes(mux *http.ServeMux) {
	owner := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			if !daemon.PrincipalFrom(req.Context()).IsOwner() {
				daemon.WriteError(w, 403, "owner_only", "owner only")
				return
			}
			h(w, req)
		}
	}
	mux.HandleFunc("POST /v1/runs", owner(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Agent string `json:"agent"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		run, err := r.Start(body.Agent)
		if err != nil {
			daemon.WriteError(w, 400, "run", err.Error())
			return
		}
		daemon.WriteJSON(w, 200, run)
	}))
	mux.HandleFunc("POST /v1/runs/{id}/keepalive", owner(func(w http.ResponseWriter, req *http.Request) {
		if err := r.KeepAlive(req.PathValue("id")); err != nil {
			daemon.WriteError(w, 404, "not_found", err.Error())
			return
		}
		daemon.WriteJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("DELETE /v1/runs/{id}", owner(func(w http.ResponseWriter, req *http.Request) {
		r.Stop(req.PathValue("id"))
		daemon.WriteJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /v1/egress/log", owner(func(w http.ResponseWriter, req *http.Request) {
		n, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		if n <= 0 {
			n = 100
		}
		l, err := r.D.State.EgressLog(n)
		if err != nil {
			daemon.WriteError(w, 500, "egress", err.Error())
			return
		}
		daemon.WriteJSON(w, 200, l)
	}))
}

// Start создаёт запуск: отдельный токен, отдельный агентский сокет, учётные
// данные прокси.
func (r *Runs) Start(agent string) (Run, error) {
	prof, ok := r.D.Config.Current().ProfileOf(agent)
	if !ok {
		return Run{}, fmt.Errorf("agent %q is not in the configuration (agents:)", agent)
	}
	id := randHex(6)
	tok, err := r.D.State.IssueTokenTTL(agent, "run:"+id, "agent", RunTTL)
	if err != nil {
		return Run{}, err
	}
	sock := filepath.Join(r.RunDir, "agent-"+id+".sock")
	l, err := ipc.Listen(sock)
	if err != nil {
		_ = r.D.State.RevokeToken(tok)
		return Run{}, err
	}
	srv := r.D.ServeListener(l, false, nil)
	pass := randHex(16)
	// Одноразовые заглушки egress-секретов запуска (в окружение агента).
	phs := map[string]string{}
	for _, s := range r.D.Config.Current().Egress.Secrets {
		if len(s.Agents) > 0 && !slices.Contains(s.Agents, agent) {
			continue
		}
		if s.Env == "" {
			r.D.Log.Warn("egress secret without env: its placeholder is permanent and shared by runs; set env for a one-time placeholder per run",
				"domain", s.Domain, "secret", s.Secret)
			continue
		}
		prefix := s.Placeholder
		if prefix == "" {
			prefix = "gate4ai"
		}
		phs[s.Env] = prefix + "-" + randHex(16)
	}
	r.mu.Lock()
	r.runs[id] = &runState{agent: agent, token: tok, pass: pass, sock: sock, srv: srv, placeholders: phs, seen: time.Now()}
	r.mu.Unlock()
	host := "127.0.0.1"
	if r.Addr.IP != nil && !r.Addr.IP.IsUnspecified() {
		host = r.Addr.IP.String()
	}
	proxy := (&url.URL{Scheme: "http", User: url.UserPassword("run-"+id, pass), Host: net.JoinHostPort(host, strconv.Itoa(r.Addr.Port))}).String()
	var tools []skill.Tool
	b, _ := json.Marshal(r.D.Tools(auth.AgentVia(agent, "run")))
	_ = json.Unmarshal(b, &tools)
	return Run{ID: id, Agent: agent, Token: tok, Socket: sock, ProxyURL: proxy, ProxyHost: host, ProxyPort: r.Addr.Port, Home: r.D.Home,
		SecretEnv: phs, KeepAliveS: int(KeepAliveEvery / time.Second),
		CAPEM: string(r.CA.PEM()), AllowDomains: prof.Sandbox.AllowDomains, AskDomains: prof.Sandbox.AskDomains,
		SkillMD: skill.Generate(tools)}, nil
}

// KeepAlive продлевает запуск: токен и учётные данные прокси ещё на RunTTL.
// Запуск, который не продлевали дольше RunTTL, завершается.
func (r *Runs) KeepAlive(id string) error {
	r.mu.Lock()
	st, ok := r.runs[id]
	expired := ok && time.Since(st.seen) > RunTTL
	r.mu.Unlock()
	if !ok || expired {
		r.Stop(id)
		return ErrNoRun
	}
	if err := r.D.State.ExtendToken(st.token, RunTTL); err != nil {
		r.Stop(id)
		return ErrNoRun
	}
	r.mu.Lock()
	st.seen = time.Now()
	r.mu.Unlock()
	return nil
}

// Stop завершает запуск: токен отзывается, сокет закрывается.
func (r *Runs) Stop(id string) {
	r.mu.Lock()
	st, ok := r.runs[id]
	delete(r.runs, id)
	r.mu.Unlock()
	if !ok {
		return
	}
	_ = r.D.State.RevokeToken(st.token)
	_ = st.srv.Close()
	_ = os.Remove(st.sock)
}

func (r *Runs) closeAll() {
	r.mu.Lock()
	ids := slices.Collect(maps.Keys(r.runs))
	r.mu.Unlock()
	for _, id := range ids {
		r.Stop(id)
	}
}

// ErrNoRun — запуск не найден.
var ErrNoRun = errors.New("run not found")

// bridgeIP — IPv4 моста docker0 (Linux), если он есть.
func bridgeIP() net.IP {
	ifc, err := net.InterfaceByName("docker0")
	if err != nil {
		return nil
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP
		}
	}
	return nil
}
