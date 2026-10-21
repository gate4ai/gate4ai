// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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

// EnableOAuth создаёт authorization server (до PublicHandler). issuer == nil —
// адрес из запроса или внешний адрес слушателя за reverse proxy (WithExternalURL).
func (d *Daemon) EnableOAuth(issuer func(*http.Request) string) *oauth.Server {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.oauth == nil {
		if issuer == nil {
			issuer = externalURL
		}
		d.oauth = oauth.New(d.State, func(a string) bool { _, ok := d.Config.Current().Agents[a]; return ok })
		d.oauth.Issuer = issuer
	}
	return d.oauth
}

type publicRoute struct {
	pattern string
	h       http.Handler
}

// AddPublic добавляет маршрут на публичный адрес устройства. Каждый маршрут сам проверяет свою аутентификацию: Web UI — сессию,
// Telegram — секретный заголовок бота; учётные данные одного маршрута не
// действуют на другом.
func (d *Daemon) AddPublic(pattern string, h http.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.public = append(d.public, publicRoute{pattern, h})
	d.publicMux = nil // пересобрать при следующем запросе
}

// AddPublicURL добавляет источник публичного адреса (прямой HTTPS, relay);
// PublicURL — первый непустой.
func (d *Daemon) AddPublicURL(f func() string) {
	d.mu.Lock()
	d.publicURLs = append(d.publicURLs, f)
	d.mu.Unlock()
}

// PublicURL — публичный адрес устройства (https://…, без «/» в конце) или "".
func (d *Daemon) PublicURL() string {
	d.mu.Lock()
	fs := d.publicURLs
	d.mu.Unlock()
	for _, f := range fs {
		if u := strings.TrimSuffix(f(), "/"); u != "" {
			return u
		}
	}
	return ""
}

type externalKey struct{}

// WithExternalURL — для слушателя за reverse proxy: адрес, под которым его
// видят снаружи (issuer OAuth и ссылки строятся от него, а не от Host).
func WithExternalURL(h http.Handler, u string) http.Handler {
	u = strings.TrimSuffix(u, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), externalKey{}, u)))
	})
}

func externalURL(r *http.Request) string {
	u, _ := r.Context().Value(externalKey{}).(string)
	return u
}

// PublicHandler — то, что видно снаружи (relay, прямой HTTPS). Разделение по путям:
//   - OAuth 2.1 (/.well-known/…, /authorize, /token, /register, /revoke) и MCP
//     (POST/DELETE на корень; /mcp — совместимость) — токен агента;
//   - /v1/whoami, /v1/tools, /v1/call, /v1/status/{id} — API агента (удалённый CLI), токен агента;
//   - маршруты AddPublic: /ui/ (Web UI, сессия), /telegram (вебхук, секрет бота).
//
// API владельца сюда не входит.
func (d *Daemon) PublicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		h := d.publicMux
		d.mu.Unlock()
		if h == nil {
			d.mu.Lock()
			n := len(d.public)
			d.mu.Unlock()
			h = d.buildPublic()
			d.mu.Lock()
			if len(d.public) == n { // маршрут не добавили, пока собирали
				d.publicMux = h
			}
			d.mu.Unlock()
		}
		h.ServeHTTP(w, r)
	})
}

// publicAgent — агент по токену на публичном адресе; владельца здесь нет.
func (d *Daemon) publicAgent(r *http.Request, via string) (string, error) {
	tok := bearer(r)
	if tok == "" {
		return "", errors.New("authorization required")
	}
	agent, err := d.State.AgentByToken(tok)
	if err != nil {
		return "", err
	}
	if _, ok := d.Config.Current().Agents[agent]; !ok {
		return "", fmt.Errorf("agent %q is not in the configuration", agent)
	}
	d.State.Seen(agent, via, tok)
	return agent, nil
}

func (d *Daemon) buildPublic() http.Handler {
	as := d.EnableOAuth(nil)
	mux := http.NewServeMux()
	as.Routes(mux)
	d.agentRoutes(mux, func(h func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			agent, err := d.publicAgent(r, "https")
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+as.ResourceMetadataURL(r)+`"`)
				writeErr(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			p := auth.AgentVia(agent, "https")
			h(w, r.WithContext(WithPrincipal(r.Context(), p)), p)
		}
	})
	d.mu.Lock()
	extra := d.public
	d.mu.Unlock()
	for _, rt := range extra {
		mux.Handle(rt.pattern, rt.h)
	}
	// MCP (Streamable HTTP) — по корню адреса устройства (его пользователь
	// вставляет в Claude.ai / ChatGPT); GET корня — подсказка или 405 для
	// SSE-потока, которого нет. /mcp — для коннекторов, добавленных раньше.
	serveMCP := func(w http.ResponseWriter, r *http.Request) {
		agent, err := d.publicAgent(r, "mcp-http")
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+as.ResourceMetadataURL(r)+`"`)
			writeErr(w, http.StatusUnauthorized, "invalid_token", "authorization required")
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
	}
	endSession := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	mux.HandleFunc("POST /{$}", serveMCP)
	mux.HandleFunc("DELETE /{$}", endSession)
	mux.HandleFunc("POST /mcp", serveMCP)
	mux.HandleFunc("DELETE /mcp", endSession)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Allow", "POST, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "gate4.ai device endpoint. Add this URL as a custom MCP connector.\n")
	})
	return mux
}

// ServePublic обслуживает публичный обработчик на слушателе (прямой HTTPS или
// HTTP за reverse proxy с внешним адресом external) до Shutdown.
func (d *Daemon) ServePublic(l net.Listener, external string) {
	var h http.Handler = d.PublicHandler()
	if external != "" {
		h = WithExternalURL(h, external)
	}
	// Ответ пишется не дольше самого долгого ожидания решения (maxPublicWait)
	// с запасом; простаивающие соединения из интернета не копятся.
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 15 * time.Second, ReadTimeout: time.Minute,
		WriteTimeout: maxPublicWait + time.Minute, IdleTimeout: 2 * time.Minute}
	d.mu.Lock()
	d.servers = append(d.servers, srv)
	d.mu.Unlock()
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.Log.Error("public listener", "err", err)
		}
	}()
}

func isInitialize(body []byte) bool {
	var m struct {
		Method string `json:"method"`
	}
	return jsonUnmarshal(body, &m) == nil && m.Method == "initialize"
}
