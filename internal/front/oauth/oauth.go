// SPDX-License-Identifier: AGPL-3.0-only

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
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/state"
)

// Server — authorization server.
type Server struct {
	State      *state.Store
	AgentKnown func(agent string) bool
	// Issuer — базовый URL устройства (https://<device-id>.relay.gate4.ai,
	// внешний адрес слушателя). Пусто — из Host запроса, только если Host —
	// корректное имя (локальный слушатель без настроенного адреса).
	Issuer     func(r *http.Request) string
	Now        func() time.Time
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	// MaxClients — предел зарегистрированных клиентов (регистрация публична):
	// при достижении удаляются старые клиенты без токенов, затем — отказ.
	MaxClients int
	// RegisterPerHour — предел регистраций клиентов в час (на всё устройство).
	RegisterPerHour int

	mu      sync.Mutex
	codes   map[string]*authCode
	connect map[string]*connectCode
	// окно частоты регистраций
	regStart time.Time
	regCount int
	// неудачные вводы кода подключения за последнюю минуту (на всё устройство)
	failStart time.Time
	failCount int
}

// Пределы перебора кода подключения: после maxConnectFailures неверных вводов
// выданные коды аннулируются (владелец выдаёт новый); не больше
// maxFailuresPerMinute неверных вводов в минуту на устройство.
const (
	maxConnectFailures   = 10
	maxFailuresPerMinute = 20
	unusedClientTTL      = 24 * time.Hour
)

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
		MaxClients: 1000, RegisterPerHour: 60,
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
	s.sweepLocked()
	s.connect[code] = &connectCode{agent: agent, expires: exp}
	s.mu.Unlock()
	return code, exp, nil
}

// sweepLocked удаляет просроченные коды авторизации и подключения.
func (s *Server) sweepLocked() {
	now := s.Now()
	for k, c := range s.codes {
		if now.After(c.expires) {
			delete(s.codes, k)
		}
	}
	for k, c := range s.connect {
		if now.After(c.expires) {
			delete(s.connect, k)
		}
	}
}

// hostRe — Host, из которого можно строить issuer: имя или IP и порт.
var hostRe = regexp.MustCompile(`^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)

// issuer — настроенный публичный адрес (Issuer); без него — Host запроса
// (локальный слушатель), если это корректное имя: issuer и адреса в
// метаданных не строятся из произвольного заголовка клиента.
func (s *Server) issuer(r *http.Request) string {
	if s.Issuer != nil {
		if v := s.Issuer(r); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	host := r.Host
	if !hostRe.MatchString(host) {
		host = "localhost"
		if r.TLS == nil {
			if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
				host = a.String()
			}
		}
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + host
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
	// Ресурс — сам адрес устройства (MCP по корню).
	writeJSON(w, 200, map[string]any{
		"resource":                 iss,
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
	if code, msg := s.admitClient(); code != 0 {
		w.Header().Set("Retry-After", "3600")
		oauthErr(w, code, "temporarily_unavailable", msg)
		return
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

// admitClient — можно ли зарегистрировать ещё одного клиента: частота
// регистраций и общее число (старые клиенты без токенов удаляются).
func (s *Server) admitClient() (int, string) {
	s.mu.Lock()
	now := s.Now()
	if now.Sub(s.regStart) >= time.Hour {
		s.regStart, s.regCount = now, 0
	}
	if s.RegisterPerHour > 0 && s.regCount >= s.RegisterPerHour {
		s.mu.Unlock()
		return http.StatusTooManyRequests, "too many client registrations, try later"
	}
	s.regCount++
	s.mu.Unlock()
	if s.MaxClients <= 0 {
		return 0, ""
	}
	n, err := s.State.CountKV("oauth.client.")
	if err != nil {
		return http.StatusServiceUnavailable, "client registry unavailable"
	}
	if n >= s.MaxClients {
		_, _ = s.State.PruneOAuthClients(now.Add(-unusedClientTTL))
		if n, err = s.State.CountKV("oauth.client."); err != nil || n >= s.MaxClients {
			return http.StatusServiceUnavailable, "too many registered clients"
		}
	}
	return 0, ""
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
	if !slices.Contains(c.RedirectURIs, a.RedirectURI) {
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

// authRequest — запрос авторизации из v; false — ответ уже записан (ошибка).
func (s *Server) authRequest(w http.ResponseWriter, r *http.Request, v url.Values) (authReq, bool) {
	a, c, e := s.parseAuth(v)
	if c == nil {
		http.Error(w, "invalid authorization request: "+e, 400)
		return a, false
	}
	if e != "" {
		redirectBack(w, r, a, "error", strings.SplitN(e, ":", 2)[0])
		return a, false
	}
	return a, true
}

// redirectBack — возврат клиенту на redirect_uri с параметром k=v (и state).
func redirectBack(w http.ResponseWriter, r *http.Request, a authReq, k, v string) {
	u, _ := url.Parse(a.RedirectURI)
	q := u.Query()
	q.Set(k, v)
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
	if a, ok := s.authRequest(w, r, r.URL.Query()); ok {
		showPage(w, 0, a)
	}
}

// showPage — форма ввода кода; status 0 — 200.
func showPage(w http.ResponseWriter, status int, a authReq) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if status != 0 {
		w.WriteHeader(status)
	}
	_ = page.Execute(w, a)
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a, ok := s.authRequest(w, r, r.PostForm)
	if !ok {
		return
	}
	if r.PostForm.Get("decision") == "deny" {
		redirectBack(w, r, a, "error", "access_denied")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.PostForm.Get("connect_code")))
	if len(code) == 8 && !strings.Contains(code, "-") {
		code = code[:4] + "-" + code[4:]
	}
	s.mu.Lock()
	s.sweepLocked()
	now := s.Now()
	if now.Sub(s.failStart) >= time.Minute {
		s.failStart, s.failCount = now, 0
	}
	if s.failCount >= maxFailuresPerMinute {
		s.mu.Unlock()
		a.Error = "Слишком много неверных кодов. Попробуйте через минуту."
		w.Header().Set("Retry-After", "60")
		showPage(w, http.StatusTooManyRequests, a)
		return
	}
	cc, ok := s.connect[code]
	if !ok {
		// Перебор: каждый неверный ввод считается против всех выданных кодов;
		// после maxConnectFailures они аннулируются.
		s.failCount++
		for k, c := range s.connect {
			if c.failures++; c.failures >= maxConnectFailures {
				delete(s.connect, k)
			}
		}
		s.mu.Unlock()
		a.Error = "Неверный или просроченный код подключения."
		showPage(w, 400, a)
		return
	}
	delete(s.connect, code) // одноразовый
	ac := randomToken()
	s.codes[ac] = &authCode{clientID: a.ClientID, redirectURI: a.RedirectURI, challenge: a.Challenge, agent: cc.agent,
		resource: a.Resource, expires: s.Now().Add(2 * time.Minute)}
	s.mu.Unlock()
	redirectBack(w, r, a, "code", ac)
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
		s.sweepLocked()
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
		// Ротация атомарна: токен гасится одним запросом до выпуска новой пары,
		// из параллельных запросов проходит один. Повторное использование
		// погашенного токена — признак кражи: отзываются все токены клиента.
		agent, clientID, err := s.State.ConsumeRefreshToken(rt)
		if errors.Is(err, state.ErrTokenReused) {
			_, _ = s.State.RevokeLabel(agent, clientID)
		}
		if err != nil || (f.Get("client_id") != "" && f.Get("client_id") != clientID) || !s.AgentKnown(agent) {
			oauthErr(w, 400, "invalid_grant", "invalid refresh token")
			return
		}
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
