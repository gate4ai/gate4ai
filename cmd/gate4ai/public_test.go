// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/approval"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/webui"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

type notes struct{}

func (notes) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "notes", Actions: []manifest.Action{
		{Name: "read", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { text: { type: string } } }`)}}}
}
func (notes) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Structured: "ok"}, nil
}
func (notes) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

// fakeBot — Bot API: запоминает секрет вебхука.
type fakeBot struct {
	mu     sync.Mutex
	secret string
	url    string
}

func (f *fakeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	var result any = true
	switch {
	case strings.HasSuffix(r.URL.Path, "/setWebhook"):
		f.mu.Lock()
		f.secret, _ = req["secret_token"].(string)
		f.url, _ = req["url"].(string)
		f.mu.Unlock()
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		result = []any{}
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		result = map[string]any{"message_id": 1}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeBot) get() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.secret, f.url
}

const publicHost = "gate.test"

const publicCfg = `agents:
  bot: {profile: p}
profiles:
  p:
    connectors: [notes]
approvals:
  channels: [telegram]
  telegram: {token: "@tg", owner_id: 42, webhook: true}
`

type env struct {
	d      *daemon.Daemon
	ui     *webui.UI
	base   string // http://127.0.0.1:port — публичный слушатель за «reverse proxy»
	local  string // http://127.0.0.1:port — локальный Web UI
	bot    *fakeBot
	cancel context.CancelFunc
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	c, _ := connectors.NewInProcess(notes{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte(publicCfg), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if err := d.Vault.Set("@tg", "123:SECRET"); err != nil {
		t.Fatal(err)
	}
	bot := &fakeBot{}
	tgSrv := httptest.NewServer(bot)
	t.Cleanup(tgSrv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	addr, err := servePublic(d, publicOpts{addr: "127.0.0.1:0", url: "https://" + publicHost}, d.Log)
	if err != nil {
		t.Fatal(err)
	}
	u := webui.New(d)
	// Пароль Web UI генерирует daemon до того, как Web UI станет доступен.
	if pw, err := u.EnsurePassword(); err != nil || pw == "" {
		t.Fatal("generated password", pw, err)
	} else {
		uiPassword = pw
	}
	d.AddPublic("/ui/", u.RemoteHandler())
	approval.Attach(ctx, d, nil, tgSrv.URL)
	l, err := u.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ls := &http.Server{Handler: u.Handler()}
	go ls.Serve(l)
	t.Cleanup(func() { ls.Close() })

	// Бот переходит на вебхук асинхронно.
	for range 200 {
		if s, _ := bot.get(); s != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &env{d: d, ui: u, base: "http://" + addr, local: "http://" + u.Addr, bot: bot, cancel: cancel}
}

// uiPassword — сгенерированный пароль Web UI текущего окружения (тесты не параллельны).
var uiPassword string

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// cred — способ предъявить учётные данные.
type cred struct {
	name  string
	apply func(*http.Request)
}

func bearerCred(name, tok string) cred {
	return cred{name, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }}
}
func cookieCred(name, cookie, val string) cred {
	return cred{name, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookie, Value: val}) }}
}
func headerCred(name, h, val string) cred {
	return cred{name, func(r *http.Request) { r.Header.Set(h, val) }}
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

// login входит с паролем по адресу Web UI base (…/ или …/ui/); возвращает cookie сессии и CSRF-токен.
func login(t *testing.T, base, host string) (*http.Cookie, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"login", strings.NewReader(url.Values{"start": {"1"}, "password": {uiPassword}}.Encode()))
	req.Host = host
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "session") && c.Value != "" {
			ck = c
		}
	}
	if resp.StatusCode != http.StatusSeeOther || ck == nil {
		t.Fatalf("login %s: %d", base, resp.StatusCode)
	}
	page := base
	req, _ = http.NewRequest("GET", page, nil)
	req.Host = host
	req.AddCookie(ck)
	resp, err = noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := csrfRe.FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("no csrf on %s: %d", page, resp.StatusCode)
	}
	return ck, m[1]
}

// TestPublicIsolation — на публичном адресе каждый путь принимает только свои
// учётные данные: токен агента не открывает Web UI и вебхук, сессия Web UI — API
// агента и MCP, секрет вебхука — ничего, кроме вебхука; API владельца нет вовсе.
func TestPublicIsolation(t *testing.T) {
	e := newEnv(t)
	tgSecret, hook := e.bot.get()
	if tgSecret == "" || hook != "https://"+publicHost+"/telegram" {
		t.Fatalf("webhook not set: %q %q", tgSecret, hook)
	}
	agentTok, _ := e.d.State.IssueToken("bot", "cli", "agent")
	oauthTok, _ := e.d.State.IssueTokenTTL("bot", "client", "oauth", time.Hour)
	refreshTok, _ := e.d.State.IssueTokenTTL("bot", "client", "refresh", time.Hour)

	rlink, err := e.ui.RemoteURL("")
	if err != nil {
		t.Fatal(err)
	}
	if rlink != "https://"+publicHost+"/ui/" {
		t.Fatalf("remote address %s", rlink)
	}
	rck, rcsrf := login(t, e.base+"/ui/", publicHost)
	if rck.Name != "g4a_rsession" || !rck.Secure || rck.Path != "/ui/" {
		t.Fatalf("remote cookie %+v", rck)
	}
	lck, _ := login(t, e.local+"/", strings.TrimPrefix(e.local, "http://"))

	creds := []cred{
		{"none", func(*http.Request) {}},
		bearerCred("agent-token", agentTok),
		bearerCred("oauth-token", oauthTok),
		bearerCred("refresh-token", refreshTok),
		cookieCred("agent-token-as-cookie", "g4a_rsession", agentTok),
		cookieCred("remote-ui", "g4a_rsession", rck.Value),
		bearerCred("remote-ui-as-bearer", rck.Value),
		cookieCred("local-ui", "g4a_session", lck.Value),
		cookieCred("local-ui-as-remote", "g4a_rsession", lck.Value),
		headerCred("telegram-secret", "X-Telegram-Bot-Api-Secret-Token", tgSecret),
		bearerCred("telegram-secret-as-bearer", tgSecret),
		headerCred("agent-token-as-telegram", "X-Telegram-Bot-Api-Secret-Token", agentTok),
	}
	agentOK := map[string]bool{"agent-token": true, "oauth-token": true}

	type route struct {
		name, method, path, body string
		// ok — учётные данные, которые открывают маршрут, и ожидаемый код.
		ok     func(c string) bool
		okCode int
		noCode int
	}
	update := `{"update_id":1,"message":{"from":{"id":42},"chat":{"id":42},"text":"/help"}}`
	routes := []route{
		{"mcp", "POST", "/", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, func(c string) bool { return agentOK[c] }, 200, 401},
		{"tools", "GET", "/v1/tools", "", func(c string) bool { return agentOK[c] }, 200, 401},
		{"whoami", "GET", "/v1/whoami", "", func(c string) bool { return agentOK[c] }, 200, 401},
		{"call", "POST", "/v1/call", `{"action":"notes.read","args":{}}`, func(c string) bool { return agentOK[c] }, 200, 401},
		{"ui-page", "GET", "/ui/", "", func(c string) bool { return c == "remote-ui" }, 200, 401},
		{"ui-journal", "GET", "/ui/journal", "", func(c string) bool { return c == "remote-ui" }, 200, 401},
		{"ui-decide", "POST", "/ui/approvals/nope", "csrf=" + rcsrf + "&decision=approve", func(c string) bool { return c == "remote-ui" }, 303, 401},
		{"telegram", "POST", "/telegram", update, func(c string) bool { return c == "telegram-secret" }, 200, 404},
	}
	// API владельца на публичном адресе отсутствует при любых учётных данных.
	for _, p := range []string{"GET /v1/health", "GET /v1/log", "GET /v1/policy", "PUT /v1/policy", "GET /v1/vault",
		"PUT /v1/vault/@x", "POST /v1/agents/bot/token", "POST /v1/agents/bot/connect", "POST /v1/ui/url",
		"GET /v1/approvals", "POST /v1/relay/register", "GET /ui/../v1/log"} {
		mp := strings.SplitN(p, " ", 2)
		routes = append(routes, route{"owner " + p, mp[0], mp[1], "{}", func(string) bool { return false }, 0, 404})
	}

	for _, rt := range routes {
		for _, c := range creds {
			req, _ := http.NewRequest(rt.method, e.base+rt.path, strings.NewReader(rt.body))
			req.Host = publicHost
			if strings.HasPrefix(rt.path, "/ui/") && rt.method == "POST" {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else if rt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			c.apply(req)
			resp, err := noRedirect.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			want := rt.noCode
			if rt.ok(c.name) {
				want = rt.okCode
			}
			// Путь с «..» ServeMux нормализует редиректом на /v1/log — тот проверен выше (404).
			if strings.Contains(rt.path, "..") && resp.Header.Get("Location") == "/v1/log" {
				continue
			}
			if resp.StatusCode != want {
				t.Errorf("%s with %s: got %d, want %d: %.200s", rt.name, c.name, resp.StatusCode, want, b)
			}
		}
	}
}

// TestPublicRemoteUI — удалённая сессия: подтверждения и журнал — да, изменения
// политик, агентов и секретов — нет; чужой Host и чужой Origin отклоняются;
// ссылки входа не взаимозаменяемы.
func TestPublicRemoteUI(t *testing.T) {
	e := newEnv(t)
	rck, csrf := login(t, e.base+"/ui/", publicHost)

	do := func(method, path, form, host, origin string) *http.Response {
		req, _ := http.NewRequest(method, e.base+path, strings.NewReader(form))
		req.Host = host
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.AddCookie(rck)
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	for _, p := range []string{"/ui/policy", "/ui/policy/rollback", "/ui/vault", "/ui/agents/bot/token", "/ui/agents/bot/connect", "/ui/agents/bot/revoke"} {
		if r := do("POST", p, "csrf="+csrf+"&yaml=x&alias=@a&value=v&version=1", publicHost, ""); r.StatusCode != http.StatusForbidden {
			t.Errorf("remote %s: %d, want 403", p, r.StatusCode)
		}
	}
	if toks, _ := e.d.State.Tokens(); len(toks) != 0 {
		t.Fatalf("remote session issued tokens: %v", toks)
	}
	if r := do("GET", "/ui/", "", "evil.test", ""); r.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("foreign Host: %d", r.StatusCode)
	}
	if r := do("POST", "/ui/approvals/x", "csrf="+csrf, publicHost, "https://evil.test"); r.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Origin: %d", r.StatusCode)
	}
	if r := do("POST", "/ui/approvals/x", "csrf=bad", publicHost, ""); r.StatusCode != http.StatusForbidden {
		t.Errorf("bad csrf: %d", r.StatusCode)
	}
	if r := do("POST", "/ui/approvals/x", "csrf="+csrf, publicHost, "https://"+publicHost); r.StatusCode != http.StatusSeeOther {
		t.Errorf("remote decide: %d", r.StatusCode)
	}

	// Сброс защиты: новый сгенерированный пароль, старый не подходит ни
	// удалённо, ни локально; формы первого пароля с публичного адреса нет.
	old := uiPassword
	pw, err := e.ui.ResetLogin()
	if err != nil || pw == "" || pw == old {
		t.Fatal("reset", pw, err)
	}
	req, _ := http.NewRequest("POST", e.base+"/ui/login", strings.NewReader("setup=1&password=remote+horse+1&repeat=remote+horse+1"))
	req.Host = publicHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://"+publicHost)
	if resp, _ := noRedirect.Do(req); resp.StatusCode != http.StatusUnauthorized || e.ui.InitialPassword() != pw {
		t.Errorf("first password set from the public address: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.base+"/ui/login", strings.NewReader(url.Values{"start": {"1"}, "password": {old}}.Encode()))
	req.Host = publicHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://"+publicHost)
	if resp, _ := noRedirect.Do(req); resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) > 0 && strings.HasSuffix(resp.Cookies()[0].Name, "session") {
		t.Error("old password works after reset")
	}
	uiPassword = pw
	if ck, _ := login(t, e.local+"/", ""); ck == nil {
		t.Error("new password after reset")
	}
}

// TestPublicTelegramWebhook — нажатие кнопки приходит вебхуком и решает запрос.
func TestPublicTelegramWebhook(t *testing.T) {
	e := newEnv(t)
	secret, _ := e.bot.get()
	post := func(sec, body string) int {
		req, _ := http.NewRequest("POST", e.base+"/telegram", strings.NewReader(body))
		req.Host = publicHost
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", sec)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(secret, "not json"); c != 400 {
		t.Errorf("bad update: %d", c)
	}
	if c := post(secret+"x", `{"update_id":1}`); c != 404 {
		t.Errorf("wrong secret: %d", c)
	}
	if c := post(secret, `{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42},"text":"/pending"}}`); c != 200 {
		t.Errorf("update: %d", c)
	}
}

// TestRemoteCLI — CLI агента с GATE4AI_URL работает через публичный адрес (TLS).
func TestRemoteCLI(t *testing.T) {
	home := t.TempDir()
	c, _ := connectors.NewInProcess(notes{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {bot: {profile: p}}\nprofiles: {p: {connectors: [notes]}}\n"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	cert, _, err := relayclient.SelfSigned("localhost")
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, caFile := filepath.Join(home, "c.pem"), filepath.Join(home, "k.pem"), filepath.Join(home, "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	kb, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(certFile, certPEM, 0o600)
	os.WriteFile(caFile, certPEM, 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600)
	if _, err := servePublic(d, publicOpts{addr: "0.0.0.0:0", url: "https://x.test"}, d.Log); err == nil {
		t.Fatal("plain HTTP on a non-loopback address must be refused")
	}
	addr, err := servePublic(d, publicOpts{addr: "127.0.0.1:0", url: "https://localhost", cert: certFile, key: keyFile}, d.Log)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := d.State.IssueToken("bot", "remote", "agent")
	addr = "localhost:" + addr[strings.LastIndex(addr, ":")+1:]

	cl, err := cli.NewRemoteClient("https://"+addr, tok, caFile)
	if err != nil {
		t.Fatal(err)
	}
	var who map[string]any
	if err := cl.Do(context.Background(), "GET", "/v1/whoami", nil, &who); err != nil {
		t.Fatal(err)
	}
	if who["agent"] != "bot" || who["via"] != "https" || who["owner"] != false {
		t.Fatalf("whoami %v", who)
	}
	var res map[string]any
	if err := cl.Do(context.Background(), "POST", "/v1/call", map[string]any{"action": "notes.read", "args": map[string]any{}}, &res); err != nil {
		t.Fatal(err)
	}
	if res["status"] != "ok" {
		t.Fatalf("call %v", res)
	}
	// Без своего CA самоподписанный сертификат не принимается.
	bad, _ := cli.NewRemoteClient("https://"+addr, tok, "")
	if err := bad.Do(context.Background(), "GET", "/v1/whoami", nil, &who); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if _, err := cli.NewRemoteClient("http://"+addr, tok, ""); err == nil {
		t.Fatal("http:// must be refused")
	}
}
