// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/pipeline"
)

type fakeAutostart struct{ on bool }

func (f *fakeAutostart) State() map[string]any { return map[string]any{"installed": f.on} }
func (f *fakeAutostart) Enable(string) error   { f.on = true; return nil }
func (f *fakeAutostart) Disable() error        { f.on = false; return nil }

// session — вход в Web UI: клиент с cookie и CSRF-токен.
func loginAs(t *testing.T, u *UI, base string) (*http.Client, string, func(path string, v url.Values) string, func(path string) string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	r, err := c.Get(u.LoginURL())
	if err != nil {
		t.Fatal(err)
	}
	csrf := csrfRe.FindStringSubmatch(body(t, r))[1]
	post := func(path string, v url.Values) string {
		t.Helper()
		v.Set("csrf", csrf)
		r, err := c.PostForm(base+path, v)
		if err != nil {
			t.Fatal(err)
		}
		p := body(t, r)
		if r.StatusCode >= 500 || strings.Contains(p, "template:") {
			t.Fatalf("POST %s: %d %s", path, r.StatusCode, p)
		}
		return p
	}
	get := func(path string) string {
		t.Helper()
		r, err := c.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		p := body(t, r)
		if r.StatusCode != 200 || strings.Contains(p, "template:") {
			t.Fatalf("GET %s: %d %s", path, r.StatusCode, p)
		}
		return p
	}
	return c, csrf, post, get
}

func TestFirstRunPaths(t *testing.T) {
	u, d, n, base := setup(t)
	home := t.TempDir()
	u.Clients = agentsetup.Env{Home: home, GOOS: "linux", Getenv: func(string) string { return "" }, LookPath: func(string) (string, error) { return "", errors.New("no") }}
	u.Bin = "/opt/gate4ai/gate4ai"
	as := &fakeAutostart{}
	u.Autostart = as
	_, _, post, get := loginAs(t, u, base)

	// Обзор: чек-лист первого запуска.
	p := get("/")
	if !strings.Contains(p, "Начало работы") || !strings.Contains(p, "Добавьте агента") {
		t.Fatal("checklist", p)
	}
	// Все страницы отрисовываются.
	for _, path := range []string{"/approvals", "/approvals/telegram", "/agents", "/agents/new", "/agents/new?client=cursor", "/agents/bot",
		"/agents/bot?client=cloud", "/agents/bot?client=codex", "/connectors", "/connectors/notes", "/policy", "/policy?sim=notes.append",
		"/policy/rule", "/policy/rule?profile=p&action=notes.append", "/policy/templates", "/policy/yaml", "/journal", "/vault", "/settings", "/settings?link=1"} {
		get(path)
	}

	// Мастер: новый агент Cursor с новым профилем и коннектором notes.
	p = post("/agents", url.Values{"name": {"cursor"}, "client": {"cursor"}, "profile": {"_new"}, "connectors": {"notes"}, "template": {"read-only"}})
	if !strings.Contains(p, "Агент cursor добавлен") {
		t.Fatal(p)
	}
	cfg := d.Config.Current()
	if cfg.Agents["cursor"].Profile != "cursor" || !cfg.Profiles["cursor"].HasConnector("notes") || len(cfg.Profiles["cursor"].Rules) != 1 {
		t.Fatalf("%+v", cfg.Profiles["cursor"])
	}
	// Автоподключение: токен в ~/.cursor/mcp.json, агент работает этим токеном.
	p = post("/agents/cursor/client", url.Values{"client": {"cursor"}})
	if !strings.Contains(p, "Cursor подключён") {
		t.Fatal(p)
	}
	b, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json"))
	if err != nil || !strings.Contains(string(b), "/opt/gate4ai/gate4ai") {
		t.Fatal(string(b), err)
	}
	tok := strings.Split(strings.Split(string(b), `"GATE4AI_TOKEN": "`)[1], `"`)[0]
	if a, err := d.State.AgentByToken(tok); err != nil || a != "cursor" {
		t.Fatal("token", a, err)
	}
	// Последнее обращение.
	d.State.Seen("cursor", "mcp", tok)
	if p := get("/agents"); !strings.Contains(p, "только что") || !strings.Contains(p, "MCP (stdio)") {
		t.Fatal("last seen", p)
	}
	// Отключение убирает сервер и отзывает токен.
	post("/agents/cursor/client/remove", url.Values{"client": {"cursor"}})
	if _, err := d.State.AgentByToken(tok); err == nil {
		t.Fatal("token not revoked")
	}

	// Конструктор правила: блоки → CEL.
	p = post("/policy/rule", url.Values{"profile": {"p"}, "action": {"notes.append"}, "effect": {"deny"}, "reason": {"no secrets"},
		"cond_kind": {"contains", ""}, "cond_arg": {"text", ""}, "cond_value": {"password", ""}})
	if !strings.Contains(p, "Правило сохранено") {
		t.Fatal(p)
	}
	rules := d.Config.Current().Profiles["p"].Rules
	if len(rules) != 1 || rules[0].When != `text.contains("password")` || rules[0].Effect != "deny" {
		t.Fatalf("%+v", rules)
	}
	if res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "my password"}}); res.Status != "denied" {
		t.Fatalf("%+v", res)
	}
	if n.n != 0 {
		t.Fatal("executed")
	}
	// Список через блок «из списка» создаётся сразу.
	p = post("/policy/rule", url.Values{"profile": {"p"}, "action": {"notes.append"}, "effect": {"allow"},
		"cond_kind": {"in_list"}, "cond_arg": {"text"}, "cond_value": {"ok_texts"}, "list_items_0": {"hello, world"}})
	if l := d.Config.Current().Lists["ok_texts"]; len(l) != 2 {
		t.Fatal(l, p[strings.Index(p, "<main>"):][:600])
	}
	// Шаблон.
	post("/policy/template", url.Values{"profile": {"p"}, "template": {"daily-limit"}, "n": {"5"}})
	if l := d.Config.Current().Profiles["p"].Limits; len(l) != 1 || l[0].PerDay != 5 {
		t.Fatalf("%+v", l)
	}
	if p := get("/policy?profile=p"); !strings.Contains(p, "не больше 5 в сутки") || !strings.Contains(p, "запрещать") {
		t.Fatal(p)
	}

	// Подтверждение → «разрешить и создать правило».
	res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "x"}})
	p = post("/approvals/"+res.ID, url.Values{"decision": {"approve_rule"}})
	if n.n != 1 || !strings.Contains(p, "Правило на основе запроса") {
		t.Fatal(n.n, p)
	}

	// Каналы: включение и статус.
	post("/channels/webpush/toggle", url.Values{"on": {"1"}})
	if !d.Config.Current().ChannelOn("webpush") {
		t.Fatal("webpush on")
	}
	if p := post("/channels/telegram/toggle", url.Values{"on": {"1"}}); !strings.Contains(p, "@BotFather") {
		t.Fatal("telegram without setup must open the wizard")
	}

	// Настройки: автозапуск, таймаут, предложения выключены.
	post("/settings/autostart", url.Values{"on": {"1"}})
	if !as.on {
		t.Fatal("autostart")
	}
	post("/settings/approvals", url.Values{"timeout": {"2h"}, "back": {"/settings"}})
	if c := d.Config.Current(); c.Approvals.Timeout.D().Hours() != 2 {
		t.Fatalf("%+v", c.Approvals)
	}
	if p := get("/settings?link=1"); !strings.Contains(p, "Нет публичного адреса") {
		t.Fatal("remote link without public address")
	}
}

// Самонастройка — обычный запрос подтверждения: решается на странице, как любой другой.
func TestChangePolicyApproval(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "append without asking", "changes": []any{map[string]any{"op": "add_rule", "action": "notes.append", "effect": "allow"}}}})
	if res.Status != pipeline.StatusPending {
		t.Fatalf("%+v", res)
	}
	if p := get("/approvals"); !strings.Contains(p, "Правило: разрешать notes.append") {
		t.Fatal(p)
	}
	post("/approvals/"+res.ID, url.Values{"decision": {"approve"}})
	if r := d.Config.Current().Profiles["p"].Rules; len(r) != 1 || r[0].Effect != config.EffectAllow {
		t.Fatalf("%+v", r)
	}
	// Шаблон запрещает самонастройку правилом.
	post("/policy/template", url.Values{"profile": {"p"}, "template": {"no-self-config"}})
	if r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "x", "changes": []any{map[string]any{"op": "remove_rule", "rule_id": "no-self-config"}}}}); r.Status != pipeline.StatusDenied {
		t.Fatalf("%+v", r)
	}
}

func TestRemoteUIIsLimited(t *testing.T) {
	u, d, _, _ := setup(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	h := u.RemoteHandler()
	l := strings.TrimPrefix(func() string { s, _ := u.RemoteLoginURL(); return s }(), "https://dev.example")
	do := func(method, path string, cookie *http.Cookie, form url.Values) *http.Response {
		var rd *strings.Reader
		if form != nil {
			rd = strings.NewReader(form.Encode())
		} else {
			rd = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, "https://dev.example"+path, rd)
		req.Host = "dev.example"
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := &recorder{h: http.Header{}}
		h.ServeHTTP(rec, req)
		return &http.Response{StatusCode: rec.code, Header: rec.h, Body: ioNop(rec.buf.String())}
	}
	r := do("GET", l, nil, nil)
	var ck *http.Cookie
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		ck = c
	}
	if ck == nil {
		t.Fatal("no remote session")
	}
	p := body(t, do("GET", "/ui/", ck, nil))
	if strings.Contains(p, `href="/ui/agents"`) || !strings.Contains(p, "Удалённый вход") {
		t.Fatal("remote nav", p)
	}
	if p := body(t, do("GET", "/ui/settings", ck, nil)); !strings.Contains(p, "Только на этом компьютере") {
		t.Fatal("settings remotely", p)
	}
	csrf := csrfRe.FindStringSubmatch(p)[1]
	if r := do("POST", "/ui/agents", ck, url.Values{"csrf": {csrf}, "name": {"evil"}, "profile": {"p"}}); r.StatusCode != 403 {
		t.Fatal("remote change", r.StatusCode)
	}
	if _, ok := d.Config.Current().Agents["evil"]; ok {
		t.Fatal("agent added remotely")
	}
}

type recorder struct {
	h    http.Header
	code int
	buf  strings.Builder
}

func (r *recorder) Header() http.Header { return r.h }
func (r *recorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = 200
	}
	return r.buf.Write(b)
}
func (r *recorder) WriteHeader(c int) { r.code = c }

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func ioNop(s string) nopCloser { return nopCloser{strings.NewReader(s)} }

// При no-referrer браузеры отправляют формы с Origin: null — проверка Origin
// отклонила бы каждое действие.
func TestReferrerPolicyKeepsOrigin(t *testing.T) {
	_, _, _, base := setup(t)
	r, err := http.Get(base + "/login")
	if err != nil {
		t.Fatal(err)
	}
	if p := r.Header.Get("Referrer-Policy"); p == "no-referrer" || p == "" {
		t.Fatal(p)
	}
}
