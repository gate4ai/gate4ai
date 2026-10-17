// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/skill"
	"github.com/gate4ai/gate4ai/internal/hostsvc/egress"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/policy"
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
}

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
	r.Proxy = &egress.Proxy{Auth: r.auth, Decide: r.decide, Secrets: r.secrets, CA: ca,
		Log: func(rec egress.Record) { _ = d.State.AddEgress(rec.TS, rec) }}
	srv := &http.Server{Handler: r.Proxy, ReadHeaderTimeout: 30 * time.Second}
	go func() { <-ctx.Done(); srv.Close(); r.closeAll() }()
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

func (r *Runs) auth(user, pass string) (string, bool) {
	id, ok := cutPrefix(user, "run-")
	if !ok {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.runs[id]
	if !ok || st.pass != pass {
		return "", false
	}
	return st.agent, true
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
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

func (r *Runs) secrets(agent, host string) []egress.Injection {
	var out []egress.Injection
	for _, s := range r.D.Config.Current().Egress.Secrets {
		if !policy.DomainMatches(host, []string{s.Domain}) {
			continue
		}
		v, err := r.D.Vault.Get(s.Secret)
		if err != nil {
			continue
		}
		out = append(out, egress.Injection{Header: s.Header, Placeholder: s.Placeholder, Value: v})
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (r *Runs) routes(mux *http.ServeMux) {
	owner := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			if !daemon.PrincipalFrom(req.Context()).IsOwner() {
				writeJSON(w, 403, map[string]any{"error": map[string]string{"code": "owner_only", "message": "owner only"}})
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
			writeJSON(w, 400, map[string]any{"error": map[string]string{"code": "run", "message": err.Error()}})
			return
		}
		writeJSON(w, 200, run)
	}))
	mux.HandleFunc("DELETE /v1/runs/{id}", owner(func(w http.ResponseWriter, req *http.Request) {
		r.Stop(req.PathValue("id"))
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /v1/egress/log", owner(func(w http.ResponseWriter, req *http.Request) {
		n, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		if n <= 0 {
			n = 100
		}
		l, err := r.D.State.EgressLog(n)
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": map[string]string{"code": "egress", "message": err.Error()}})
			return
		}
		writeJSON(w, 200, l)
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
	tok, err := r.D.State.IssueToken(agent, "run:"+id, "agent")
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
	r.mu.Lock()
	r.runs[id] = &runState{agent: agent, token: tok, pass: pass, sock: sock, srv: srv}
	r.mu.Unlock()
	host := "127.0.0.1"
	if r.Addr.IP != nil && !r.Addr.IP.IsUnspecified() {
		host = r.Addr.IP.String()
	}
	proxy := (&url.URL{Scheme: "http", User: url.UserPassword("run-"+id, pass), Host: net.JoinHostPort(host, strconv.Itoa(r.Addr.Port))}).String()
	var tools []skill.Tool
	b, _ := json.Marshal(r.D.Tools(agentPrincipal(agent)))
	_ = json.Unmarshal(b, &tools)
	return Run{ID: id, Agent: agent, Token: tok, Socket: sock, ProxyURL: proxy, ProxyHost: host, ProxyPort: r.Addr.Port,
		CAPEM: string(r.CA.PEM()), AllowDomains: prof.Sandbox.AllowDomains, AskDomains: prof.Sandbox.AskDomains,
		SkillMD: skill.Generate(tools)}, nil
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
	ids := make([]string, 0, len(r.runs))
	for id := range r.runs {
		ids = append(ids, id)
	}
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
