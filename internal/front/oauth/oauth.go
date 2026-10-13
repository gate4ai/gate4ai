// SPDX-License-Identifier: FSL-1.1-ALv2

// Package oauth — встроенный OAuth 2.1 authorization server для remote MCP:
// динамическая регистрация клиентов (RFC 7591), authorization code + PKCE S256,
// refresh с ротацией, отзыв (RFC 7009), метаданные (RFC 8414, RFC 9728).
// Владелец подтверждает подключение одноразовым кодом подключения, который
// выдаёт локально (`gate4ai agents connect <agent>`, Web UI); код привязан к агенту,
// поэтому каждый агент получает свой токен.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/state"
)

// Server — authorization server.
type Server struct {
	State      *state.Store
	AgentKnown func(agent string) bool
	// Issuer — базовый URL устройства (https://<device-id>.relay.gate4.ai); nil — из запроса.
	Issuer     func(r *http.Request) string
	Now        func() time.Time
	AccessTTL  time.Duration
	RefreshTTL time.Duration

	mu      sync.Mutex
	codes   map[string]*authCode
	connect map[string]*connectCode
}

type authCode struct {
	clientID, redirectURI, challenge, agent, resource string
	expires                                           time.Time
}

type connectCode struct {
	agent    string
	expires  time.Time
	failures int
}

// Client — зарегистрированный клиент.
type Client struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name,omitempty"`
	RedirectURIs []string `json:"redirect_uris"`
	CreatedAt    int64    `json:"client_id_issued_at"`
}

// New создаёт сервер.
func New(st *state.Store, known func(string) bool) *Server {
	return &Server{State: st, AgentKnown: known, Now: time.Now, AccessTTL: time.Hour, RefreshTTL: 30 * 24 * time.Hour,
		codes: map[string]*authCode{}, connect: map[string]*connectCode{}}
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomCode(n int) string {
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		b[i] = codeAlphabet[k.Int64()]
	}
	return string(b)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewConnectCode — одноразовый код подключения агента (10 минут). Только владелец.
func (s *Server) NewConnectCode(agent string) (string, time.Time, error) {
	if !s.AgentKnown(agent) {
		return "", time.Time{}, errors.New("unknown agent " + agent)
	}
	code := randomCode(4) + "-" + randomCode(4)
	exp := s.Now().Add(10 * time.Minute)
	s.mu.Lock()
	s.connect[code] = &connectCode{agent: agent, expires: exp}
	s.mu.Unlock()
	return code, exp, nil
}

func (s *Server) issuer(r *http.Request) string {
	if s.Issuer != nil {
		if v := s.Issuer(r); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// ResourceMetadataURL — для заголовка WWW-Authenticate ресурса MCP.
func (s *Server) ResourceMetadataURL(r *http.Request) string {
	return s.issuer(r) + "/.well-known/oauth-protected-resource"
}

// Routes регистрирует эндпоинты.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.asMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.asMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.prMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.prMetadata)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("GET /authorize", s.authorizeForm)
	mux.HandleFunc("POST /authorize", s.authorizeSubmit)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("POST /revoke", s.revoke)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthErr(w http.ResponseWriter, code int, e, desc string) {
	writeJSON(w, code, map[string]string{"error": e, "error_description": desc})
}

func (s *Server) asMetadata(w http.ResponseWriter, r *http.Request) {
	iss := s.issuer(r)
	writeJSON(w, 200, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/authorize",
		"token_endpoint":                        iss + "/token",
		"registration_endpoint":                 iss + "/register",
		"revocation_endpoint":                   iss + "/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
	})
}

func (s *Server) prMetadata(w http.ResponseWriter, r *http.Request) {
	iss := s.issuer(r)
	writeJSON(w, 200, map[string]any{
		"resource":                 iss + "/mcp",
		"authorization_servers":    []string{iss},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
		"resource_name":            "gate4.ai",
	})
}

func validRedirect(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Fragment != "" || p.Host == "" {
		return false
	}
	if p.Scheme == "https" {
		return true
	}
	h := p.Hostname()
	return p.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1")
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthErr(w, 400, "invalid_client_metadata", err.Error())
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthErr(w, 400, "invalid_redirect_uri", "1..10 redirect_uris required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirect(u) {
			oauthErr(w, 400, "invalid_redirect_uri", "redirect_uri must be https (or http on localhost): "+u)
			return
		}
	}
	if len(req.ClientName) > 100 {
		req.ClientName = req.ClientName[:100]
	}
	c := Client{ClientID: "c_" + randomToken()[:22], ClientName: req.ClientName, RedirectURIs: req.RedirectURIs, CreatedAt: s.Now().Unix()}
	b, _ := json.Marshal(c)
	if err := s.State.Set("oauth.client."+c.ClientID, string(b)); err != nil {
		oauthErr(w, 500, "server_error", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"client_id": c.ClientID, "client_name": c.ClientName, "redirect_uris": c.RedirectURIs,
		"client_id_issued_at": c.CreatedAt, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}})
}

func (s *Server) client(id string) (*Client, bool) {
	v, ok := s.State.Get("oauth.client." + id)
	if !ok {
		return nil, false
	}
	var c Client
	if json.Unmarshal([]byte(v), &c) != nil {
		return nil, false
	}
	return &c, true
}

type authReq struct {
	ClientID, RedirectURI, State, Challenge, Method, Scope, Resource string
	ClientName, Error                                                string
}

// parseAuth проверяет параметры запроса авторизации; ошибка до проверки
// redirect_uri показывается на странице, а не редиректом (защита от open redirect).
func (s *Server) parseAuth(v url.Values) (authReq, *Client, string) {
	a := authReq{ClientID: v.Get("client_id"), RedirectURI: v.Get("redirect_uri"), State: v.Get("state"),
		Challenge: v.Get("code_challenge"), Method: v.Get("code_challenge_method"), Scope: v.Get("scope"), Resource: v.Get("resource")}
	c, ok := s.client(a.ClientID)
	if !ok {
		return a, nil, "unknown client_id"
	}
	a.ClientName = c.ClientName
	okURI := false
	for _, u := range c.RedirectURIs {
		okURI = okURI || u == a.RedirectURI
	}
	if !okURI {
		return a, nil, "redirect_uri is not registered for this client"
	}
	if v.Get("response_type") != "code" {
		return a, c, "unsupported_response_type"
	}
	if a.Method != "S256" || len(a.Challenge) < 43 {
		return a, c, "invalid_request: PKCE with S256 is required"
	}
	return a, c, ""
}

func redirectErr(w http.ResponseWriter, r *http.Request, a authReq, e string) {
	u, _ := url.Parse(a.RedirectURI)
	q := u.Query()
	q.Set("error", e)
	if a.State != "" {
		q.Set("state", a.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

var page = template.Must(template.New("authorize").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>gate4.ai — подключение агента</title>
<style>body{font-family:system-ui,sans-serif;max-width:28rem;margin:3rem auto;padding:0 1rem;color:#1d1d1f}
input{font-size:1.4rem;letter-spacing:.15em;padding:.4rem;width:100%;box-sizing:border-box;text-transform:uppercase}
button{margin-top:1rem;font-size:1rem;padding:.6rem 1.2rem}.err{color:#b00020}.muted{color:#666;font-size:.9rem}</style></head>
<body><h1>Подключить агента</h1>
<p><b>{{.ClientName}}</b> просит доступ к gate4.ai на этом устройстве.</p>
<p class="muted">Введите код подключения. Его выдаёт владелец на устройстве: в Web UI («Агенты → Подключить») или командой <code>gate4ai agents connect &lt;агент&gt;</code>. Код определяет, каким агентом и с каким профилем будет работать клиент.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/authorize">
<input type="hidden" name="client_id" value="{{.ClientID}}"><input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="state" value="{{.State}}"><input type="hidden" name="code_challenge" value="{{.Challenge}}">
<input type="hidden" name="code_challenge_method" value="{{.Method}}"><input type="hidden" name="scope" value="{{.Scope}}">
<input type="hidden" name="resource" value="{{.Resource}}"><input type="hidden" name="response_type" value="code">
<label>Код подключения<br><input name="connect_code" autocomplete="one-time-code" required autofocus></label>
<button type="submit" name="decision" value="approve">Подключить</button>
<button type="submit" name="decision" value="deny">Отклонить</button>
</form></body></html>`))

func secureHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

func (s *Server) authorizeForm(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	a, c, e := s.parseAuth(r.URL.Query())
	if c == nil {
		http.Error(w, "invalid authorization request: "+e, 400)
		return
	}
	if e != "" {
		redirectErr(w, r, a, strings.SplitN(e, ":", 2)[0])
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, a)
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a, c, e := s.parseAuth(r.PostForm)
	if c == nil {
		http.Error(w, "invalid authorization request: "+e, 400)
		return
	}
	if e != "" {
		redirectErr(w, r, a, strings.SplitN(e, ":", 2)[0])
		return
	}
	if r.PostForm.Get("decision") == "deny" {
		redirectErr(w, r, a, "access_denied")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("connect_code")))
	if len(code) == 8 && !strings.Contains(code, "-") {
		code = code[:4] + "-" + code[4:]
	}
	s.mu.Lock()
	cc, ok := s.connect[code]
	if ok && s.Now().After(cc.expires) {
		delete(s.connect, code)
		ok = false
	}
	if !ok {
		s.mu.Unlock()
		a.Error = "Неверный или просроченный код подключения."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(400)
		_ = page.Execute(w, a)
		return
	}
	delete(s.connect, code) // одноразовый
	ac := randomToken()
	s.codes[ac] = &authCode{clientID: a.ClientID, redirectURI: a.RedirectURI, challenge: a.Challenge, agent: cc.agent,
		resource: a.Resource, expires: s.Now().Add(2 * time.Minute)}
	s.mu.Unlock()
	u, _ := url.Parse(a.RedirectURI)
	q := u.Query()
	q.Set("code", ac)
	if a.State != "" {
		q.Set("state", a.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func (s *Server) issue(w http.ResponseWriter, agent, clientID string) {
	acc, err := s.State.IssueTokenTTL(agent, clientID, "oauth", s.AccessTTL)
	if err != nil {
		oauthErr(w, 500, "server_error", err.Error())
		return
	}
	ref, err := s.State.IssueTokenTTL(agent, clientID, "refresh", s.RefreshTTL)
	if err != nil {
		oauthErr(w, 500, "server_error", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"access_token": acc, "token_type": "Bearer", "expires_in": int(s.AccessTTL.Seconds()),
		"refresh_token": ref, "scope": "mcp"})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthErr(w, 400, "invalid_request", err.Error())
		return
	}
	f := r.PostForm
	switch f.Get("grant_type") {
	case "authorization_code":
		s.mu.Lock()
		ac, ok := s.codes[f.Get("code")]
		delete(s.codes, f.Get("code")) // одноразовый в любом случае
		s.mu.Unlock()
		if !ok || s.Now().After(ac.expires) {
			oauthErr(w, 400, "invalid_grant", "unknown or expired code")
			return
		}
		if ac.clientID != f.Get("client_id") || ac.redirectURI != f.Get("redirect_uri") {
			oauthErr(w, 400, "invalid_grant", "client_id or redirect_uri mismatch")
			return
		}
		if !pkceOK(f.Get("code_verifier"), ac.challenge) {
			oauthErr(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		if !s.AgentKnown(ac.agent) {
			oauthErr(w, 400, "invalid_grant", "agent no longer configured")
			return
		}
		s.issue(w, ac.agent, ac.clientID)
	case "refresh_token":
		rt := f.Get("refresh_token")
		agent, clientID, err := s.State.RefreshToken(rt)
		if err != nil || (f.Get("client_id") != "" && f.Get("client_id") != clientID) || !s.AgentKnown(agent) {
			oauthErr(w, 400, "invalid_grant", "invalid refresh token")
			return
		}
		_ = s.State.RevokeToken(rt) // ротация
		s.issue(w, agent, clientID)
	default:
		oauthErr(w, 400, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err == nil {
		if t := r.PostForm.Get("token"); t != "" {
			_ = s.State.RevokeToken(t)
		}
	}
	w.WriteHeader(200)
}
