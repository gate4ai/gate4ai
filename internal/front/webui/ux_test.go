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

	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/relayclient"
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
		"/policy/rule", "/policy/rule?profile=p&action=notes.append", "/policy/templates", "/policy/yaml", "/journal", "/vault", "/settings", "/settings?link=1", "/settings/relay"} {
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
	// Блок «для нового профиля» помечен для показа только при profile=_new.
	if p := get("/agents/new?client=cli"); !strings.Contains(p, `data-show-when="profile=_new"`) || !strings.Contains(p, `value="cursor">существующий: cursor`) {
		t.Fatal("new-profile block", p)
	}
	// С существующим профилем поля нового профиля не приходят и не нужны.
	if p := post("/agents", url.Values{"name": {"cli2"}, "client": {"cli"}, "profile": {"cursor"}}); !strings.Contains(p, "Агент cli2 добавлен") {
		t.Fatal(p)
	}
	if cfg := d.Config.Current(); cfg.Agents["cli2"].Profile != "cursor" || len(cfg.Profiles["cursor"].Rules) != 1 {
		t.Fatalf("%+v", cfg.Agents["cli2"])
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
	// Карточка Telegram: до настройки — инструкция и «Подключить», после — статус «подключён».
	if p := get("/approvals"); !strings.Contains(p, "Подключить Telegram") || strings.Contains(p, "Telegram подключён") {
		t.Fatal("telegram card before setup", p)
	}
	if err := u.edit("test", func(c *config.Config) error {
		c.Approvals.Telegram = &config.Telegram{Token: "@telegram.bot", OwnerID: 42}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if p := get("/approvals"); strings.Contains(p, "Подключить Telegram") || strings.Contains(p, "@BotFather") || !strings.Contains(p, "Telegram подключён") {
		t.Fatal("telegram card after setup", p)
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
	// Relay включён: один адрес для облачных агентов, детали relay — в раскрывающемся блоке.
	u.Relay = func() relayclient.Status {
		return relayclient.Status{Configured: true, Online: true, URL: "https://relay.test", DeviceID: "dev1", Host: "dev1.relay.test", MCPURL: "https://dev1.relay.test"}
	}
	p = get("/settings")
	if !strings.Contains(p, `<code id="mcp">https://dev1.relay.test</code>`) || !strings.Contains(p, "custom connector") ||
		!strings.Contains(p, "<summary>Технические детали</summary>") || strings.Contains(p, "Адрес устройства") {
		t.Fatal("relay block", p)
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

// Блок relay: одна кнопка и ссылки; после подключения — состояние и MCP без формы;
// повторная регистрация — только с подтверждением; с --public формы нет.
func TestRelaySettings(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)

	p := get("/settings")
	for _, want := range []string{"Зарегистрироваться на relay.gate4.ai", `value="https://relay.gate4.ai"`, "/settings/relay#custom", "/settings/relay#public"} {
		if !strings.Contains(p, want) {
			t.Fatal(want, p)
		}
	}
	if strings.Contains(p, "Зарегистрировать заново") || strings.Contains(p, "data-live-relay") {
		t.Fatal("not configured", p)
	}
	if p := get("/settings/relay"); !strings.Contains(p, `id="custom"`) || !strings.Contains(p, "--public-url") || strings.Contains(p, `id="again"`) {
		t.Fatal("relay page", p)
	}

	// Устройство зарегистрировано, сертификат выпускается: этап виден, страница обновляется сама.
	st := relayclient.Status{Configured: true, Phase: relayclient.PhaseIssuing, URL: "https://relay.gate4.ai", DeviceID: "abc",
		Host: "abc.relay.gate4.ai", MCPURL: "https://abc.relay.gate4.ai"}
	u.Relay = func() relayclient.Status { return st }
	p = get("/settings")
	for _, want := range []string{`data-live-relay="issuing"`, "выпускаем сертификат", `<code id="mcp">https://abc.relay.gate4.ai</code>`, "Сменить relay или адрес устройства"} {
		if !strings.Contains(p, want) {
			t.Fatal(want, p)
		}
	}
	for _, bad := range []string{"Зарегистрировать заново", "Зарегистрироваться на", "нет связи"} {
		if strings.Contains(p, bad) {
			t.Fatal(bad, p)
		}
	}
	// Адрес relay — только в свёрнутых «Технических деталях».
	if main, _, _ := strings.Cut(p, "<details>"); strings.Contains(main, "Relay: <code>") {
		t.Fatal("relay url in main view", p)
	}
	st.Phase, st.Online = relayclient.PhaseOnline, true
	if p := get("/settings"); !strings.Contains(p, "подключён") || strings.Contains(p, "data-live-relay") {
		t.Fatal("online", p)
	}
	st.Phase, st.Online, st.Error = relayclient.PhaseFailed, false, "acme: boom"
	if p := get("/settings"); !strings.Contains(p, "не удалось подключиться") || !strings.Contains(p, "acme: boom") || strings.Contains(p, "data-live-relay") {
		t.Fatal("failed", p)
	}
	if p := get("/settings/relay"); !strings.Contains(p, `id="again"`) || !strings.Contains(p, `name="confirm"`) || strings.Contains(p, `id="custom"`) {
		t.Fatal("relay page configured", p)
	}

	// Повторная регистрация без подтверждения не уходит на relay.
	if err := u.edit("relay", func(c *config.Config) error {
		c.Relay.URL, c.Relay.DeviceID = "https://relay.gate4.ai", "abc"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if p := post("/settings/relay", url.Values{"url": {"https://relay.gate4.ai"}, "form": {"again"}}); !strings.Contains(p, "подтвердите") {
		t.Fatal("re-register without confirm", p)
	}
	if d.Config.Current().Relay.DeviceID != "abc" {
		t.Fatal("device changed")
	}

	// Daemon с --public: адрес и MCP, без формы регистрации.
	d.AddPublicURL(func() string { return "https://gate.example.com" })
	for _, path := range []string{"/settings", "/settings/relay"} {
		p := get(path)
		if !strings.Contains(p, `<code id="mcp">https://gate.example.com</code>`) || !strings.Contains(p, "Relay не нужен") ||
			strings.Contains(p, `action="/settings/relay"`) || strings.Contains(p, "data-live-relay") {
			t.Fatal(path, p)
		}
	}
}

// Сообщение после действия — в запросе, а не во фрагменте «#…».
func TestRedirectKeepsFragment(t *testing.T) {
	for to, want := range map[string]string{
		"/settings#relay":    "/settings?flash=ok#relay",
		"/settings?link=1#x": "/settings?link=1&flash=ok#x",
		"/settings":          "/settings?flash=ok",
	} {
		if got := redirect(to, "ok")["redirect"]; got != want {
			t.Errorf("%s: %v, want %s", to, got, want)
		}
	}
}

// Relay настроен, но туннеля нет: ссылка не выдаётся (она бы зависла до таймаута),
// вместо неё — причина; появилась связь — ссылка есть.
func TestRemoteLinkRelayOffline(t *testing.T) {
	u, d, _, base := setup(t)
	st := relayclient.Status{Configured: true, Phase: relayclient.PhaseIssuing, Host: "abc.relay.test", MCPURL: "https://abc.relay.test",
		Error: "obtaining certificate: no NS"}
	u.Relay = func() relayclient.Status { return st }
	d.AddPublicURL(func() string { return "https://abc.relay.test" })
	if _, err := u.RemoteLoginURL(); !errors.Is(err, errRelayOffline) || !strings.Contains(err.Error(), "no NS") {
		t.Fatal(err)
	}
	_, _, _, get := loginAs(t, u, base)
	if p := get("/settings?link=1"); !strings.Contains(p, "не подключено к relay") || !strings.Contains(p, "Последняя ошибка: obtaining certificate: no NS") ||
		strings.Contains(p, "/ui/login?t=") {
		t.Fatal(p)
	}
	st.Online, st.Phase, st.Error = true, relayclient.PhaseOnline, ""
	if l, err := u.RemoteLoginURL(); err != nil || !strings.HasPrefix(l, "https://abc.relay.test/ui/login?t=") {
		t.Fatal(l, err)
	}
}

// «Разрешать всегда» не создаёт мёртвых правил: правило, которое перебьёт
// более сильное, сохраняется с предупреждением, дубль не добавляется.
func TestRuleOverriddenAndDuplicate(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, _ := loginAs(t, u, base)
	ask := url.Values{"profile": {"p"}, "action": {"notes.append"}, "effect": {"ask"}}
	if p := post("/policy/rule", ask); !strings.Contains(p, "Правило сохранено") {
		t.Fatal(p)
	}
	if p := post("/policy/rule", ask); !strings.Contains(p, "Такое правило уже есть") {
		t.Fatal("duplicate:", p)
	}
	if n := len(d.Config.Current().Profiles["p"].Rules); n != 1 {
		t.Fatal("rules:", n)
	}
	// Запрос подтверждения → правило allow по нему: ask всё равно сильнее.
	res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "x"}})
	p := post("/policy/rule", url.Values{"profile": {"p"}, "action": {"notes.append"}, "effect": {"allow"}, "approval": {res.ID}})
	if !strings.Contains(p, "не сработает") || !strings.Contains(p, "notes-append-ask") {
		t.Fatal("overridden by ask:", p[strings.Index(p, "<main>"):][:800])
	}
	// Без запроса — по правилам профиля.
	p = post("/policy/rule", url.Values{"profile": {"p"}, "action": {"notes.append"}, "effect": {"allow"},
		"cond_kind": {"contains"}, "cond_arg": {"text"}, "cond_value": {"hello"}})
	if !strings.Contains(p, "не сработает") {
		t.Fatal(p[strings.Index(p, "<main>"):][:800])
	}
}

func TestSuggestCondsOnlyForAmounts(t *testing.T) {
	tool := daemon.Tool{Schema: manifest.MustSchema(`{ type: object, properties: { amount: { type: number }, timeout_ms: { type: integer } } }`)}
	conds := suggestConds(tool, map[string]any{"amount": 10.0, "timeout_ms": 0.0})
	if len(conds) != 1 || conds[0].Arg != "amount" {
		t.Fatalf("%+v", conds)
	}
}
