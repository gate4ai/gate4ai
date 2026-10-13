// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/sdk-go/manifest"
)

// Tool — действие в каталоге агента.
type Tool struct {
	Name        string         `json:"name"` // email.send
	Connector   string         `json:"connector"`
	Action      string         `json:"action"`
	Description string         `json:"description"`
	Class       string         `json:"class"`
	Args        []manifest.Arg `json:"args"`
	InputSchema map[string]any `json:"input_schema"`
	Example     string         `json:"example,omitempty"`
}

// Tools — каталог действий; для агента — только коннекторы его профиля.
func (d *Daemon) Tools(p auth.Principal) []Tool {
	var prof config.Profile
	filter := p.Kind == auth.Agent
	if filter {
		var ok bool
		if prof, ok = d.Config.Current().ProfileOf(p.Agent); !ok {
			return []Tool{}
		}
	}
	out := []Tool{}
	for _, m := range d.Registry.Manifests() {
		if policy.Implicit[m.Connector] || (filter && !prof.HasConnector(m.Connector)) {
			continue
		}
		for _, a := range m.Actions {
			out = append(out, Tool{Name: m.Full(a), Connector: m.Connector, Action: a.Name, Description: a.Description,
				Class: a.Class, Args: a.Args, InputSchema: a.JSONSchema(), Example: a.Example})
		}
	}
	return out
}

type ctxKey struct{}

// PrincipalFrom — собеседник запроса.
func PrincipalFrom(ctx context.Context) auth.Principal {
	p, _ := ctx.Value(ctxKey{}).(auth.Principal)
	return p
}

// WithPrincipal кладёт собеседника в контекст.
func WithPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// identify: на сокете владельца проверенный собеседник — владелец, если не
// предъявил токен агента; на сокете агентов нужен токен, прав владельца нет.
func (d *Daemon) identify(r *http.Request, ownerSock bool) (auth.Principal, error) {
	tok := bearer(r)
	if tok != "" {
		agent, err := d.State.AgentByToken(tok)
		if err != nil {
			return auth.Principal{}, err
		}
		if _, ok := d.Config.Current().Agents[agent]; !ok {
			return auth.Principal{}, fmt.Errorf("agent %q is not in the configuration", agent)
		}
		return auth.AgentVia(agent, map[bool]string{true: "socket", false: "agent-socket"}[ownerSock]), nil
	}
	if ownerSock {
		if _, ok := ipc.PeerFrom(r.Context()); ok {
			p := auth.OwnerVia("socket")
			return p, nil
		}
	}
	return auth.Principal{}, errors.New("authentication required: pass an agent token (GATE4AI_TOKEN)")
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// APIError — ошибка API.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]APIError{"error": {Code: code, Message: msg}})
}

func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// numbersToFloat приводит json.Number к float64 (для аргументов действий).
func numbersToFloat(v any) any {
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		return f
	case map[string]any:
		for k, x := range t {
			t[k] = numbersToFloat(x)
		}
	case []any:
		for i, x := range t {
			t[i] = numbersToFloat(x)
		}
	}
	return v
}

// Handler — HTTP API: owner=true — сокет владельца.
func (d *Daemon) Handler(owner bool) http.Handler {
	mux := http.NewServeMux()
	anyp := func(h func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p, err := d.identify(r, owner)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			h(w, r.WithContext(WithPrincipal(r.Context(), p)), p)
		}
	}
	own := func(h func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
		return anyp(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
			if !p.IsOwner() {
				writeErr(w, http.StatusForbidden, "owner_only", "this operation requires the owner (local socket without an agent token)")
				return
			}
			h(w, r, p)
		})
	}
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		_, n := d.Config.CurrentRaw()
		writeJSON(w, 200, map[string]any{"ok": true, "config_version": n, "connectors": len(d.Registry.Manifests())})
	})
	mux.HandleFunc("GET /v1/whoami", anyp(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		writeJSON(w, 200, map[string]any{"owner": p.IsOwner(), "agent": p.Agent, "via": p.Via})
	}))
	mux.HandleFunc("GET /v1/tools", anyp(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if as := r.URL.Query().Get("agent"); as != "" && p.IsOwner() {
			p = auth.AgentVia(as, "socket")
		}
		writeJSON(w, 200, d.Tools(p))
	}))
	mux.HandleFunc("POST /v1/call", anyp(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			Action string         `json:"action"`
			Args   map[string]any `json:"args"`
			Wait   string         `json:"wait"`
			Agent  string         `json:"agent"` // только владелец: выполнить от имени агента (его профиль)
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		agent := p.Agent
		if p.IsOwner() {
			if req.Agent == "" {
				writeErr(w, 400, "agent_required", "owner calls must name the agent whose profile applies (--as <agent>)")
				return
			}
			agent = req.Agent
		}
		wait, err := config.ParseDuration(req.Wait)
		if err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		args, _ := numbersToFloat(map[string]any(req.Args)).(map[string]any)
		res := d.Pipeline.Execute(r.Context(), pipeline.Call{Agent: agent, Action: req.Action, Args: args, Wait: wait})
		writeJSON(w, 200, res)
	}))
	mux.HandleFunc("GET /v1/status/{id}", anyp(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		id := r.PathValue("id")
		a, err := d.State.GetApproval(id)
		if err != nil || (!p.IsOwner() && a.Agent != p.Agent) {
			writeErr(w, 404, "not_found", "unknown id")
			return
		}
		wait, _ := config.ParseDuration(r.URL.Query().Get("wait"))
		if wait > 0 {
			writeJSON(w, 200, d.Pipeline.WaitFor(r.Context(), id, wait))
			return
		}
		res, _ := d.Pipeline.Status(id)
		writeJSON(w, 200, res)
	}))

	// ---- Только владелец ----
	mux.HandleFunc("GET /v1/log", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit == 0 {
			limit = 50
		}
		f := audit.Filter{Agent: q.Get("agent"), Action: q.Get("action"), Effect: q.Get("effect"), Limit: limit, Desc: true}
		if s := q.Get("since"); s != "" {
			if dur, err := config.ParseDuration(s); err == nil {
				f.Since = time.Now().Add(-dur)
			}
		}
		es, err := d.Journal.List(f)
		if err != nil {
			writeErr(w, 500, "journal", err.Error())
			return
		}
		if es == nil {
			es = []audit.Entry{}
		}
		writeJSON(w, 200, es)
	}))
	mux.HandleFunc("GET /v1/show/{key}", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		e, err := d.Journal.Find(r.PathValue("key"))
		if err != nil {
			writeErr(w, 404, "not_found", err.Error())
			return
		}
		writeJSON(w, 200, e)
	}))
	mux.HandleFunc("POST /v1/export", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			Passphrase string `json:"passphrase"`
		}
		_ = readJSON(r, &req)
		var buf bytes.Buffer
		if err := d.Journal.Export(&buf, req.Passphrase); err != nil {
			writeErr(w, 500, "export", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(buf.Bytes())
	}))
	mux.HandleFunc("POST /v1/journal/verify", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if err := d.Journal.VerifyChain(); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		seq, head, _ := d.Journal.Head()
		writeJSON(w, 200, map[string]any{"ok": true, "receipts": seq, "head": head})
	}))
	mux.HandleFunc("GET /v1/policy", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		raw, n := d.Config.CurrentRaw()
		writeJSON(w, 200, map[string]any{"version": n, "yaml": string(raw)})
	}))
	mux.HandleFunc("PUT /v1/policy", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			YAML    string `json:"yaml"`
			Comment string `json:"comment"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		v, err := d.Config.Put(p, []byte(req.YAML), req.Comment)
		if err != nil {
			writeErr(w, 400, "invalid_policy", err.Error())
			return
		}
		writeJSON(w, 200, v)
	}))
	mux.HandleFunc("GET /v1/policy/versions", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		vs, err := d.Config.Versions()
		if err != nil {
			writeErr(w, 500, "config", err.Error())
			return
		}
		writeJSON(w, 200, vs)
	}))
	mux.HandleFunc("POST /v1/policy/rollback", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			Version int `json:"version"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		v, err := d.Config.Rollback(p, req.Version)
		if err != nil {
			writeErr(w, 400, "rollback", err.Error())
			return
		}
		writeJSON(w, 200, v)
	}))
	mux.HandleFunc("POST /v1/policy/test", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			Agent  string         `json:"agent"`
			Action string         `json:"action"`
			Args   map[string]any `json:"args"`
			YAML   string         `json:"yaml"` // проверить черновик политики вместо текущей
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		d.policyTest(w, req.Agent, req.Action, numbersToFloat(map[string]any(req.Args)).(map[string]any), req.YAML)
	}))
	mux.HandleFunc("GET /v1/agents", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		toks, _ := d.State.Tokens()
		if toks == nil {
			toks = []state.TokenInfo{}
		}
		writeJSON(w, 200, map[string]any{"agents": d.Config.Current().Agents, "tokens": toks})
	}))
	mux.HandleFunc("POST /v1/agents/{name}/token", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		name := r.PathValue("name")
		if _, ok := d.Config.Current().Agents[name]; !ok {
			writeErr(w, 404, "not_found", fmt.Sprintf("agent %q is not in the configuration; add it to agents: first", name))
			return
		}
		var req struct {
			Label string `json:"label"`
		}
		_ = readJSON(r, &req)
		tok, err := d.State.IssueToken(name, req.Label, "agent")
		if err != nil {
			writeErr(w, 500, "token", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"agent": name, "token": tok})
	}))
	mux.HandleFunc("POST /v1/agents/{name}/connect", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		code, exp, err := d.EnableOAuth(nil).NewConnectCode(r.PathValue("name"))
		if err != nil {
			writeErr(w, 404, "not_found", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"agent": r.PathValue("name"), "code": code, "expires": exp,
			"hint": "add https://<device-id>.relay.gate4.ai/mcp as a custom connector and enter this code when asked"})
	}))
	mux.HandleFunc("POST /v1/agents/{name}/revoke", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		n, err := d.State.RevokeAgent(r.PathValue("name"))
		if err != nil {
			writeErr(w, 500, "revoke", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"revoked": n})
	}))
	mux.HandleFunc("GET /v1/approvals", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		as, err := d.State.ListApprovals(r.URL.Query().Get("status"), 100)
		if err != nil {
			writeErr(w, 500, "approvals", err.Error())
			return
		}
		if as == nil {
			as = []*state.Approval{}
		}
		writeJSON(w, 200, as)
	}))
	mux.HandleFunc("POST /v1/approvals/{id}/decide", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			Approve bool   `json:"approve"`
			Channel string `json:"channel"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		ch := req.Channel
		if ch == "" {
			ch = "cli"
		}
		res, err := d.Pipeline.Decide(r.Context(), r.PathValue("id"), req.Approve, ch, "owner")
		if err != nil {
			code := 400
			if errors.Is(err, state.ErrNoApproval) {
				code = 404
			}
			writeErr(w, code, "decide", err.Error())
			return
		}
		writeJSON(w, 200, res)
	}))
	mux.HandleFunc("GET /v1/vault", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		writeJSON(w, 200, d.Vault.Aliases())
	}))
	mux.HandleFunc("PUT /v1/vault/{alias}", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			Value string `json:"value"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		if err := d.Vault.Set(r.PathValue("alias"), req.Value); err != nil {
			writeErr(w, 400, "vault", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}))
	mux.HandleFunc("DELETE /v1/vault/{alias}", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		if err := d.Vault.Delete(r.PathValue("alias")); err != nil {
			writeErr(w, 400, "vault", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}))
	if owner && len(d.extra) > 0 {
		// Маршруты подсистем (Web UI, трей, relay): с опознанием собеседника.
		em := http.NewServeMux()
		for _, f := range d.extra {
			f(em)
		}
		mux.Handle("/", anyp(func(w http.ResponseWriter, r *http.Request, p auth.Principal) { em.ServeHTTP(w, r) }))
	}
	return mux
}

func (d *Daemon) policyTest(w http.ResponseWriter, agent, action string, args map[string]any, draft string) {
	eng := d.Pipeline.Engine()
	if draft != "" {
		cfg, err := d.Config.Validate([]byte(draft))
		if err != nil {
			writeErr(w, 400, "invalid_policy", err.Error())
			return
		}
		if eng, err = policy.New(cfg, policy.Catalog(d.Registry.Catalog())); err != nil {
			writeErr(w, 400, "invalid_policy", err.Error())
			return
		}
	}
	_, act, ok := d.Registry.Lookup(action)
	if !ok {
		writeErr(w, 404, "unknown_action", action)
		return
	}
	nargs, err := act.Normalize(args)
	if err != nil {
		writeErr(w, 400, "invalid_arguments", err.Error())
		return
	}
	dec := eng.Decide(policy.Request{Agent: agent, Action: action, Class: act.Class, Args: nargs, Now: time.Now()}, d.State.Counters())
	writeJSON(w, 200, dec)
}
