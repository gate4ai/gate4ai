// SPDX-License-Identifier: AGPL-3.0-only

package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

const verifier = "verifier-0123456789-0123456789-0123456789-abcdef"

func challenge(v string) string {
	s := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

type env struct {
	t   *testing.T
	s   *Server
	srv *httptest.Server
	st  *state.Store
	c   *http.Client
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"), keys.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, func(a string) bool { return a == "claude" })
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &env{t: t, s: s, srv: srv, st: st, c: c}
}

func (e *env) register(redirect string) (int, map[string]any) {
	e.t.Helper()
	resp, err := e.c.Post(e.srv.URL+"/register", "application/json", strings.NewReader(`{"client_name":"Test","redirect_uris":["`+redirect+`"]}`))
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func (e *env) authorize(clientID, redirect, code string) *http.Response {
	e.t.Helper()
	f := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "state": {"st"}, "code_challenge": {challenge(verifier)},
		"code_challenge_method": {"S256"}, "response_type": {"code"}, "connect_code": {code}, "decision": {"approve"}}
	resp, err := e.c.PostForm(e.srv.URL+"/authorize", f)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (e *env) token(f url.Values) (int, map[string]any) {
	e.t.Helper()
	resp, err := e.c.PostForm(e.srv.URL+"/token", f)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

// login — полный вход: регистрация, код подключения, код авторизации, токены.
func (e *env) login() (clientID string, tok map[string]any) {
	e.t.Helper()
	const redirect = "http://127.0.0.1:3000/cb"
	code, m := e.register(redirect)
	if code != 201 {
		e.t.Fatal(code, m)
	}
	clientID = m["client_id"].(string)
	cc, _, err := e.s.NewConnectCode("claude")
	if err != nil {
		e.t.Fatal(err)
	}
	resp := e.authorize(clientID, redirect, strings.ToLower(strings.ReplaceAll(cc, "-", "")))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || loc.Query().Get("state") != "st" || loc.Query().Get("code") == "" {
		e.t.Fatal(resp.StatusCode, loc)
	}
	f := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier}}
	st, tok := e.token(f)
	if st != 200 || tok["access_token"] == nil || tok["refresh_token"] == nil {
		e.t.Fatal(st, tok)
	}
	// Код одноразовый.
	if st, _ := e.token(f); st != 400 {
		e.t.Fatal("code reused")
	}
	return clientID, tok
}

func TestFlowAndRefreshRotation(t *testing.T) {
	e := newEnv(t)
	clientID, tok := e.login()
	if a, err := e.st.AgentByToken(tok["access_token"].(string)); err != nil || a != "claude" {
		t.Fatal(a, err)
	}
	// refresh не принимается как access.
	if _, err := e.st.AgentByToken(tok["refresh_token"].(string)); err == nil {
		t.Fatal("refresh accepted as access")
	}
	rt := tok["refresh_token"].(string)
	st, tok2 := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {clientID}})
	if st != 200 {
		t.Fatal(st, tok2)
	}
	// Повторное использование старого refresh — отказ и отзыв всех токенов клиента.
	if st, _ := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}}); st != 400 {
		t.Fatal("rotated refresh token reused")
	}
	if _, err := e.st.AgentByToken(tok2["access_token"].(string)); err == nil {
		t.Fatal("tokens of the client survive refresh token reuse")
	}
	if st, _ := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok2["refresh_token"].(string)}}); st != 400 {
		t.Fatal("new refresh token survives reuse of the old one")
	}
}

// Параллельные запросы с одним refresh-токеном: новую пару получает один.
func TestRefreshRace(t *testing.T) {
	e := newEnv(t)
	_, tok := e.login()
	rt := tok["refresh_token"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st, _ := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}}); st == 200 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d parallel refreshes succeeded", ok)
	}
}

func TestPKCEAndRedirects(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"http://evil.example/cb", "https://x.example/cb#frag", "javascript:alert(1)"} {
		if code, _ := e.register(bad); code != 400 {
			t.Errorf("%s registered", bad)
		}
	}
	const redirect = "https://app.example/cb"
	_, m := e.register(redirect)
	clientID := m["client_id"].(string)
	// Чужой redirect_uri — ошибка на странице, а не редирект (open redirect).
	q := url.Values{"client_id": {clientID}, "redirect_uri": {"https://evil.example/cb"}, "response_type": {"code"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}
	resp, _ := e.c.Get(e.srv.URL + "/authorize?" + q.Encode())
	if resp.StatusCode != 400 || resp.Header.Get("Location") != "" {
		t.Fatal(resp.StatusCode)
	}
	// Без PKCE S256 — ошибка редиректом на зарегистрированный адрес.
	q.Set("redirect_uri", redirect)
	q.Set("code_challenge_method", "plain")
	resp, _ = e.c.Get(e.srv.URL + "/authorize?" + q.Encode())
	if loc := resp.Header.Get("Location"); resp.StatusCode != 302 || !strings.HasPrefix(loc, redirect) || !strings.Contains(loc, "error=invalid_request") {
		t.Fatal(resp.StatusCode, loc)
	}
	// Неверный verifier.
	cc, _, _ := e.s.NewConnectCode("claude")
	r := e.authorize(clientID, redirect, cc)
	loc, _ := url.Parse(r.Header.Get("Location"))
	if st, _ := e.token(url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code_verifier": {strings.Repeat("x", 50)}}); st != 400 {
		t.Fatal("bad PKCE verifier accepted")
	}
	if _, _, err := e.s.NewConnectCode("ghost"); err == nil {
		t.Fatal("connect code for an unknown agent")
	}
}

// Перебор кода подключения: после maxConnectFailures неверных вводов
// выданные коды аннулируются; частота неверных вводов ограничена.
func TestConnectCodeBruteForce(t *testing.T) {
	e := newEnv(t)
	const redirect = "https://app.example/cb"
	_, m := e.register(redirect)
	clientID := m["client_id"].(string)
	cc, _, _ := e.s.NewConnectCode("claude")
	for i := 0; i < maxConnectFailures; i++ {
		if r := e.authorize(clientID, redirect, fmt.Sprintf("AAAA-%04d", i)); r.StatusCode != 400 {
			t.Fatal(r.StatusCode)
		}
	}
	if r := e.authorize(clientID, redirect, cc); r.StatusCode == 302 {
		t.Fatal("connect code survives brute force")
	}
	for i := 0; i < maxFailuresPerMinute; i++ {
		e.authorize(clientID, redirect, "BBBB-BBBB")
	}
	cc, _, _ = e.s.NewConnectCode("claude")
	if r := e.authorize(clientID, redirect, cc); r.StatusCode != http.StatusTooManyRequests {
		t.Fatal("no global limit on wrong codes", r.StatusCode)
	}
	// Через минуту — снова можно.
	now := time.Now().Add(2 * time.Minute)
	e.s.Now = func() time.Time { return now }
	cc, _, _ = e.s.NewConnectCode("claude")
	if r := e.authorize(clientID, redirect, cc); r.StatusCode != 302 {
		t.Fatal(r.StatusCode)
	}
}

// Регистрация клиентов ограничена по частоте и общему числу; старые
// клиенты без токенов удаляются.
func TestRegistrationLimits(t *testing.T) {
	e := newEnv(t)
	e.s.RegisterPerHour = 3
	for i := 0; i < 3; i++ {
		if code, _ := e.register("https://app.example/cb"); code != 201 {
			t.Fatal(code)
		}
	}
	if code, _ := e.register("https://app.example/cb"); code != http.StatusTooManyRequests {
		t.Fatal("no rate limit", code)
	}
	e.s.RegisterPerHour = 0
	e.s.MaxClients = 4
	clientID, _ := e.login() // 4-й клиент, с токенами
	if code, _ := e.register("https://app.example/cb"); code != http.StatusServiceUnavailable {
		t.Fatal("no client limit", code)
	}
	// Через сутки клиенты без токенов удаляются, клиент с токенами остаётся.
	later := time.Now().Add(25 * time.Hour)
	e.s.Now = func() time.Time { return later }
	if code, _ := e.register("https://app.example/cb"); code != 201 {
		t.Fatal(code)
	}
	if _, ok := e.s.client(clientID); !ok {
		t.Fatal("client with tokens pruned")
	}
	if n, _ := e.st.CountKV("oauth.client."); n != 2 {
		t.Fatal(n)
	}
}

// Issuer — настроенный публичный адрес; без него Host запроса
// используется, только если это корректное имя.
func TestIssuer(t *testing.T) {
	e := newEnv(t)
	get := func(host string) map[string]any {
		req, _ := http.NewRequest("GET", e.srv.URL+"/.well-known/oauth-authorization-server", nil)
		req.Host = host
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &m)
		return m
	}
	if m := get("127.0.0.1:7431"); m["issuer"] != "http://127.0.0.1:7431" || m["token_endpoint"] != "http://127.0.0.1:7431/token" {
		t.Fatal(m)
	}
	if m := get(`evil.example/"><script>`); strings.Contains(fmt.Sprint(m["issuer"]), "evil") {
		t.Fatal(m)
	}
	e.s.Issuer = func(*http.Request) string { return "https://dev1.relay.gate4.ai/" }
	if m := get("attacker.example"); m["issuer"] != "https://dev1.relay.gate4.ai" || m["registration_endpoint"] != "https://dev1.relay.gate4.ai/register" {
		t.Fatal(m)
	}
}

// Просроченные коды подключения и авторизации не копятся в памяти.
func TestSweep(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.s.NewConnectCode("claude")
	}
	later := time.Now().Add(time.Hour)
	e.s.Now = func() time.Time { return later }
	e.s.NewConnectCode("claude")
	e.s.mu.Lock()
	n := len(e.s.connect)
	e.s.mu.Unlock()
	if n != 1 {
		t.Fatal(n)
	}
}
