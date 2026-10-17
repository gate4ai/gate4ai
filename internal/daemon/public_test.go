// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

func TestRemoteMCPWithOAuth(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4aoauth")
	defer os.RemoveAll(home)
	n1, _ := connectors.NewInProcess(&notes{})
	d, err := New(Options{Home: filepath.Join(home, "d"), InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Connectors: []connectors.Client{n1}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ts := httptest.NewServer(d.PublicHandler())
	defer ts.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 1. Без токена — 401 с указанием на метаданные ресурса.
	resp, _ := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if resp.StatusCode != 401 || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource") {
		t.Fatal(resp.StatusCode, resp.Header)
	}
	var meta map[string]any
	getJSON(t, ts.URL+"/.well-known/oauth-protected-resource", &meta)
	if meta["authorization_servers"].([]any)[0] != ts.URL {
		t.Fatal(meta)
	}
	getJSON(t, ts.URL+"/.well-known/oauth-authorization-server", &meta)
	if meta["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Fatal(meta)
	}
	// 2. Динамическая регистрация.
	redirect := "https://claude.ai/api/mcp/auth_callback"
	if r, _ := http.Post(ts.URL+"/register", "application/json", strings.NewReader(`{"client_name":"x","redirect_uris":["http://evil.example/cb"]}`)); r.StatusCode != 400 {
		t.Fatal("http redirect accepted")
	}
	resp, _ = http.Post(ts.URL+"/register", "application/json", strings.NewReader(`{"client_name":"Claude","redirect_uris":["`+redirect+`"]}`))
	var reg map[string]any
	json.NewDecoder(resp.Body).Decode(&reg)
	clientID := reg["client_id"].(string)
	// 3. Авторизация с PKCE и кодом подключения.
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "state": {"st1"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	resp, _ = http.Get(ts.URL + "/authorize?" + q.Encode())
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Claude") || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal(resp.StatusCode, string(page))
	}
	bad := url.Values{"redirect_uri": {"https://evil.example/cb"}, "client_id": {clientID}, "response_type": {"code"}}
	if r, _ := noRedirect.Get(ts.URL + "/authorize?" + bad.Encode()); r.StatusCode != 400 {
		t.Fatal("unregistered redirect must not redirect", r.StatusCode)
	}
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("connect_code", "AAAA-BBBB")
	if r, _ := noRedirect.PostForm(ts.URL+"/authorize", form); r.StatusCode != 400 {
		t.Fatal("wrong connect code accepted", r.StatusCode)
	}
	code, _, err := d.EnableOAuth(nil).NewConnectCode("bot")
	if err != nil {
		t.Fatal(err)
	}
	form.Set("connect_code", strings.ToLower(strings.ReplaceAll(code, "-", "")))
	r, _ := noRedirect.PostForm(ts.URL+"/authorize", form)
	loc, _ := url.Parse(r.Header.Get("Location"))
	if r.StatusCode != 302 || loc.Query().Get("state") != "st1" || loc.Query().Get("code") == "" || !strings.HasPrefix(loc.String(), redirect) {
		t.Fatal(r.StatusCode, loc)
	}
	if r2, _ := noRedirect.PostForm(ts.URL+"/authorize", form); r2.StatusCode != 400 {
		t.Fatal("connect code reused")
	}
	// 4. Обмен кода: неверный verifier — отказ, и код сгорает.
	tokForm := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {redirect}, "code_verifier": {strings.Repeat("w", 50)}}
	if r, _ := http.PostForm(ts.URL+"/token", tokForm); r.StatusCode != 400 {
		t.Fatal("bad verifier accepted")
	}
	// Новый круг с правильным verifier.
	code, _, _ = d.EnableOAuth(nil).NewConnectCode("bot")
	form.Set("connect_code", code)
	r, _ = noRedirect.PostForm(ts.URL+"/authorize", form)
	loc, _ = url.Parse(r.Header.Get("Location"))
	tokForm.Set("code", loc.Query().Get("code"))
	tokForm.Set("code_verifier", verifier)
	resp, _ = http.PostForm(ts.URL+"/token", tokForm)
	var tok map[string]any
	json.NewDecoder(resp.Body).Decode(&tok)
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	if access == "" || refresh == "" || tok["token_type"] != "Bearer" {
		t.Fatal(tok)
	}
	// 5. MCP с токеном: каталог агента bot (только notes), вызов.
	mcpCall := func(token, body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	st, m := mcpCall(access, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := m["result"].(map[string]any)["tools"].([]any)
	if st != 200 || len(tools) != 5 || tools[2].(map[string]any)["name"] != "notes_read" {
		t.Fatal(st, m)
	}
	_, m = mcpCall(access, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"notes_append","arguments":{"text":"my password"}}}`)
	if m["result"].(map[string]any)["isError"] != true {
		t.Fatal(m)
	}
	// 6. Refresh с ротацией.
	resp, _ = http.PostForm(ts.URL+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	json.NewDecoder(resp.Body).Decode(&tok)
	if resp.StatusCode != 200 || tok["access_token"] == access {
		t.Fatal(tok)
	}
	if r, _ := http.PostForm(ts.URL+"/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); r.StatusCode != 400 {
		t.Fatal("old refresh token reused")
	}
	// 7. Отзыв агента в один клик → все его токены недействительны.
	d.State.RevokeAgent("bot")
	if st, _ := mcpCall(tok["access_token"].(string), `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`); st != 401 {
		t.Fatal("revoked token works", st)
	}
	// API владельца через публичный адрес недоступен.
	if r, _ := http.Get(ts.URL + "/v1/log"); r.StatusCode != 404 {
		t.Fatal("owner API exposed publicly", r.StatusCode)
	}
}

func getJSON(t *testing.T, u string, v any) {
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(v)
}
