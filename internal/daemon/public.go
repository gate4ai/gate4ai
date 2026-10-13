// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/front/mcp"
	"github.com/gate4ai/gate4ai/internal/front/oauth"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// backend — MCP-бэкенд в процессе daemon от имени агента.
type backend struct {
	d *Daemon
	p auth.Principal
}

func (b backend) Tools(ctx context.Context) ([]mcp.Tool, error) {
	var out []mcp.Tool
	for _, t := range b.d.Tools(b.p) {
		out = append(out, mcp.Tool{Name: t.Name, Description: t.Description, Class: t.Class, InputSchema: t.InputSchema})
	}
	return out, nil
}

func (b backend) Call(ctx context.Context, action string, args map[string]any) (map[string]any, error) {
	var res pipeline.Result
	if action == "@status" {
		id, _ := args["id"].(string)
		a, err := b.d.State.GetApproval(id)
		if err != nil || a.Agent != b.p.Agent {
			return map[string]any{"status": "error", "code": "not_found", "message": "unknown id"}, nil
		}
		wait, _ := args["wait"].(string)
		d, _ := config.ParseDuration(wait)
		if d > 2*time.Minute {
			d = 2 * time.Minute
		}
		res = b.d.Pipeline.WaitFor(ctx, id, d)
	} else {
		res = b.d.Pipeline.Execute(ctx, pipeline.Call{Agent: b.p.Agent, Action: action, Args: args})
	}
	return toMap(res), nil
}

func toMap(v any) map[string]any {
	b, _ := jsonMarshal(v)
	var m map[string]any
	_ = jsonUnmarshal(b, &m)
	return m
}

// EnableOAuth создаёт authorization server (до PublicHandler).
func (d *Daemon) EnableOAuth(issuer func(*http.Request) string) *oauth.Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.oauth == nil {
		d.oauth = oauth.New(d.State, func(a string) bool { _, ok := d.Config.Current().Agents[a]; return ok })
		d.oauth.Issuer = issuer
	}
	return d.oauth
}

// PublicHandler — то, что видят облачные агенты через relay: remote MCP
// (Streamable HTTP) и OAuth 2.1. API владельца сюда не входит.
func (d *Daemon) PublicHandler() http.Handler {
	as := d.EnableOAuth(nil)
	mux := http.NewServeMux()
	as.Routes(mux)
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		agent, err := d.State.AgentByToken(tok)
		if tok == "" || err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+as.ResourceMetadataURL(r)+`"`)
			writeErr(w, http.StatusUnauthorized, "invalid_token", "authorization required")
			return
		}
		if _, ok := d.Config.Current().Agents[agent]; !ok {
			writeErr(w, http.StatusForbidden, "agent_removed", "agent is not configured")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			writeErr(w, 400, "bad_request", err.Error())
			return
		}
		srv := mcp.NewServer(backend{d: d, p: auth.AgentVia(agent, "mcp-http")})
		if _, err := srv.Handle(r.Context(), "tools/list", nil); err != nil {
			writeErr(w, 500, "catalog", err.Error())
			return
		}
		out, _ := srv.HandleMessage(r.Context(), body)
		if isInitialize(body) {
			sid := make([]byte, 16)
			_, _ = rand.Read(sid)
			w.Header().Set("Mcp-Session-Id", hex.EncodeToString(sid))
		}
		if out == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("DELETE /mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "gate4.ai device endpoint. Add <this URL>/mcp as a custom MCP connector.\n")
	})
	return mux
}

func isInitialize(body []byte) bool {
	var m struct {
		Method string `json:"method"`
	}
	return jsonUnmarshal(body, &m) == nil && m.Method == "initialize"
}
