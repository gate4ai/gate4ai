// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser"
	"github.com/gate4ai/gate4ai/internal/knowledge"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// siteConn — коннектор к сайту app.example.org; loginConn — коннектор входа на login.example.org.
type siteConn struct{ m manifest.Manifest }

func (c siteConn) Manifest() manifest.Manifest { return c.m }
func (siteConn) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Structured: "ok"}, nil
}
func (siteConn) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func TestSitesLookup(t *testing.T) {
	site := siteConn{manifest.Manifest{Connector: "example", Version: "1", Sites: []access.Site{{Domain: "app.example.org", Access: access.Allow}},
		Actions: []manifest.Action{{Name: "summary", Class: "read", Description: "Summary"}, {Name: "internal", Class: "read", ServiceOnly: true}}, Services: []string{"internal"}, Contract: 1}}
	login := siteConn{manifest.Manifest{Connector: "example_login", Version: "1", Sites: []access.Site{{Domain: "login.example.org", Access: access.Allow}},
		Credentials: []manifest.CredentialSlot{{Slot: "login", Bind: []string{"https://login.example.org"}}},
		Actions:     []manifest.Action{{Name: "login", Class: "write", InputSchema: manifest.MustSchema(`{ type: object, properties: { account: { type: string } }, required: [account] }`)}}}}
	c1, err := connectors.NewInProcess(site)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := connectors.NewInProcess(login)
	if err != nil {
		t.Fatal(err)
	}
	know := t.TempDir()
	conf := "agents:\n  bot: {profile: p}\n  other: {profile: q}\nprofiles:\n  p:\n    connectors: [example, example_login]\n  q:\n    connectors: []\n" +
		"credentials:\n  \"@ex\":\n    bind: [\"https://login.example.org\"]\n    form: { fields: [{selector: \"#p\", secret: \"@ex.password\"}], submit: {selector: button} }\n    use: [p]\n" +
		"knowledge: { dir: " + know + " }\n"
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c1, c2}, InitialConfig: []byte(conf)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Папка знаний: рецепт к другому сайту и к этому.
	for name, body := range map[string]string{
		"gate4ai-shop": "---\nsite: shop.example.org\ntasks:\n  - { id: buy, status: draft, verified_runs: 0 }\n---\n## buy\n",
		"gate4ai-rzd":  "---\nsite: rzd.ru\ndomains: [ticket.rzd.ru, pass.rzd.ru]\nlogin: { bind: https://pass.rzd.ru, kind: form+sms }\ntasks:\n  - { id: buy_ticket, status: draft }\n---\n## buy_ticket\n",
	} {
		os.MkdirAll(filepath.Join(know, name), 0o755)
		os.WriteFile(filepath.Join(know, name, "RECIPE.md"), []byte(body), 0o644)
	}
	if d.KnowledgeDir() != know {
		t.Fatal(d.KnowledgeDir())
	}

	call := func(agent, dom string) map[string]any {
		r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: agent, Action: "gate4ai.sites_lookup", Args: map[string]any{"domain": dom}})
		if r.Status != "ok" {
			t.Fatalf("%+v", r)
		}
		b, _ := json.Marshal(r.Result)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return out
	}
	// Поставленный коннектор к сайту: он и подсказка про его действия; service_only не показывается.
	out := call("bot", "https://app.example.org/page?x=1")
	if out["domain"] != "app.example.org" {
		t.Fatal(out["domain"])
	}
	inst := out["installed"].([]any)
	if len(inst) != 1 || inst[0].(map[string]any)["connector"] != "example" || len(inst[0].(map[string]any)["actions"].([]any)) != 1 {
		t.Fatalf("%+v", inst)
	}
	if h := out["hint"].(string); !strings.Contains(h, "gate4ai example <action>") {
		t.Fatal(h)
	}
	// Origin входа: учётка владельца и коннектор входа.
	out = call("bot", "login.example.org")
	login2 := out["login"].([]any)
	if len(login2) != 2 {
		t.Fatalf("%+v", login2)
	}
	if h := out["hint"].(string); !strings.Contains(h, "--account @ex") {
		t.Fatal(h)
	}
	// Агент другого профиля видит коннектор, но с пометкой «не в профиле»; учётка ему недоступна.
	out = call("other", "app.example.org")
	row := out["installed"].([]any)[0].(map[string]any)
	if row["visible"] != false || !strings.Contains(row["hint"].(string), "add_connector") {
		t.Fatalf("%+v", row)
	}
	// Папка знаний: рецепт к rzd.ru находится по поддомену; кода коннектора нет —
	// подсказка читать рецепт, сеанс дописывает его сам.
	out = call("bot", "ticket.rzd.ru")
	kn := out["knowledge"].([]any)
	if len(kn) != 1 || kn[0].(map[string]any)["folder"] != "gate4ai-rzd" || kn[0].(map[string]any)["status"] != "draft" || kn[0].(map[string]any)["sessions"] != 0.0 {
		t.Fatalf("%+v", kn)
	}
	if h := out["hint"].(string); !strings.Contains(h, "RECIPE.md") || !strings.Contains(h, "knowledge note ticket.rzd.ru") {
		t.Fatal(h)
	}
	// Папка с манифестом — коннектор есть: подсказка про install_dev_connector.
	os.WriteFile(filepath.Join(know, "gate4ai-shop", "manifest.yaml"), []byte("connector: shop\nversion: \"0.1.0\"\nruntime: wasm\nsites: [{domain: shop.example.org, access: allow}]\nactions:\n  - {name: buy, class: write}\n"), 0o644)
	out = call("bot", "shop.example.org")
	if h := out["hint"].(string); !strings.Contains(h, "install_dev_connector") {
		t.Fatal(h)
	}
	// Ничего нет: подсказка — сеанс браузера; рецепт напишет daemon сам, после close — knowledge note.
	out = call("bot", "https://www.ozon.ru/cart")
	if h := out["hint"].(string); !strings.Contains(h, "browser open") || !strings.Contains(h, "knowledge note") || !strings.Contains(h, "add_credential") || strings.Contains(h, "save_steps") {
		t.Fatal(h)
	}
	if len(out["installed"].([]any)) != 0 || len(out["store"].([]any)) != 0 {
		t.Fatalf("%+v", out)
	}
	// Без domain — invalid_args.
	if r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.sites_lookup", Args: map[string]any{"domain": " "}}); r.Code != pipeline.CodeInvalidArgs {
		t.Fatalf("%+v", r)
	}

	// API владельца: папки знаний и проверка.
	mux := http.NewServeMux()
	d.knowledgeRoutes(mux)
	do := func(method, path, body string, owner bool) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		p := auth.AgentVia("bot", "agent-socket")
		if owner {
			p = auth.OwnerVia("socket")
		}
		req = req.WithContext(WithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := do("GET", "/v1/knowledge", "", false); code != 403 {
		t.Fatal(code)
	}
	code, out := do("GET", "/v1/knowledge", "", true)
	if code != 200 || out["dir"] != know || len(out["folders"].([]any)) != 2 {
		t.Fatalf("%d %+v", code, out)
	}
	f := out["folders"].([]any)[0].(map[string]any)
	if f["state"] != "files" || f["publish"].(map[string]any)["kind"] != "scratch" {
		t.Fatalf("%+v", f)
	}
	code, out = do("POST", "/v1/knowledge/check", `{"name":"rzd"}`, true)
	rep := out["report"].(map[string]any)
	if code != 200 || rep["ok"] != false || len(rep["errors"].([]any)) == 0 {
		t.Fatalf("%d %+v", code, out)
	}
	if code, _ = do("POST", "/v1/knowledge/check", `{"name":"nope"}`, true); code != 404 {
		t.Fatal(code)
	}
	if knowledge.ConnectorNameFor("ticket.rzd.ru") != "rzd" || knowledge.ConnectorNameFor("www.ozon.ru") != "ozon" || knowledge.ConnectorNameFor("localhost") != "localhost" || knowledge.ConnectorNameFor("1c.ru") != "site_1c" {
		t.Fatal("names")
	}
}

// add_credential: ответ содержит, какие секреты задать и где; агент ждёт их
// через /v1/vault/status (только да/нет, учётки своего профиля).
func TestAddCredentialHintAndVaultStatus(t *testing.T) {
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(),
		InitialConfig: []byte("agents:\n  bot: {profile: p}\n  other: {profile: q}\nprofiles:\n  p:\n    connectors: []\n  q:\n    connectors: []\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	cred := `{"bind":["https://pass.example.org"],"form":{"fields":[{"selector":"#login","secret":"@ex.login","type":"text"},{"selector":"#password","secret":"@ex.password"}],"submit":{"selector":"button"}},"use":["p"]}`
	r := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{"summary": "account for example.org",
		"changes": []any{map[string]any{"op": "add_credential", "account": "@ex", "credential_json": cred}}}})
	if r.Status != pipeline.StatusPending {
		t.Fatalf("%+v", r)
	}
	res, err := d.Pipeline.Decide(ctx, r.ID, true, "webui", "owner")
	if err != nil || res.Status != "ok" {
		t.Fatalf("%+v %v", res, err)
	}
	out := res.Result.(map[string]any)
	if out["account"] != "@ex" || out["vault_url"] != d.VaultURL() || !strings.Contains(out["hint"].(string), "vault status @ex --wait") {
		t.Fatalf("%+v", out)
	}
	if ts := fmt.Sprint(out["secrets_to_set"]); !strings.Contains(ts, "@ex.login") || !strings.Contains(ts, "@ex.password") {
		t.Fatalf("%+v", out["secrets_to_set"])
	}
	mux := http.NewServeMux()
	anyp := func(h func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, PrincipalFrom(r.Context())) }
	}
	d.agentRoutes(mux, anyp)
	status := func(p auth.Principal, account string) (int, map[string]any) {
		req := httptest.NewRequest("GET", "/v1/vault/status?account="+account, nil)
		req = req.WithContext(WithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	code, m := status(auth.AgentVia("bot", "agent-socket"), "@ex")
	if code != 200 || m["ready"] != false || len(m["secrets"].([]any)) != 2 {
		t.Fatalf("%d %+v", code, m)
	}
	if code, _ = status(auth.AgentVia("other", "agent-socket"), "@ex"); code != 404 {
		t.Fatal("another profile sees the account", code)
	}
	if code, _ = status(auth.AgentVia("bot", "agent-socket"), "@nope"); code != 404 {
		t.Fatal(code)
	}
	for _, a := range []string{"@ex.login", "@ex.password"} {
		if err := d.Vault.Set(a, "v-"+a); err != nil {
			t.Fatal(err)
		}
	}
	if code, m = status(auth.OwnerVia("socket"), "ex"); code != 200 || m["ready"] != true {
		t.Fatalf("%d %+v", code, m)
	}
}

// Авто-рецепт: сеанс open → get → close без воли агента оставляет RECIPE.md
// в папке знаний (её ещё нет), ответ close несёт knowledge с подсказкой про
// knowledge note; второй сеанс дописывает, не перетирает; заметка агента
// попадает в рецепт; lookup показывает сеансы.
func TestAutoRecipeFromSession(t *testing.T) {
	know := filepath.Join(t.TempDir(), "knowledge")
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(),
		InitialConfig: []byte("agents:\n  bot: {profile: p}\nprofiles:\n  p:\n    connectors: []\nknowledge: { dir: " + know + " }\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	steps := []map[string]any{
		{"action": "open", "args": map[string]any{"url": "https://habr.com/ru/"}, "result": map[string]any{"url": "https://habr.com/ru/", "title": "Хабр"}},
		{"action": "get", "args": map[string]any{"url": "https://habr.com/kek/v2/me"}, "result": map[string]any{"status": 200.0, "content_type": "application/json",
			"json": map[string]any{"alias": "ivan_petrov", "email": "ivan@mail.ru", "id": 1234567890.0, "karma": 12.0}}},
		{"action": "get", "args": map[string]any{"url": "https://habr.com/kek/v2/users/ivan_petrov/card"}, "result": map[string]any{"status": 200.0, "json": map[string]any{"fullname": "Иван Петров"}}},
		{"action": "open", "args": map[string]any{"url": "https://habr.com/ru/article/new/"}, "result": map[string]any{"url": "https://habr.com/ru/article/new/", "title": "Новая публикация"}},
		{"action": "act", "args": map[string]any{"selector": "#missing", "method": "click"}, "error": map[string]any{"code": "not_found", "message": "no #missing"}},
		{"action": "close", "args": map[string]any{}, "result": map[string]any{"closed": true}},
	}
	k := d.sessionClosed(browser.Closed{Session: "s-7f3a", Agent: "bot", Reason: "closed by browser.close", Steps: steps, StepsFile: "/data/browser-steps/s-7f3a.json"})
	if k == nil || k["site"] != "habr.com" || k["status"] != "auto" || k["sessions"] != 1 {
		t.Fatalf("%+v", k)
	}
	path := filepath.Join(know, "gate4ai-habr", "RECIPE.md")
	if k["recipe"] != path || !strings.Contains(k["hint"].(string), "gate4ai knowledge note habr.com --text") || strings.Contains(k["hint"].(string), "connectors new") {
		t.Fatalf("%+v", k)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"status: auto", "session: s-7f3a", "agent: bot", "steps: 6", "## Session ", "Open `https://habr.com/ru/`", "«Хабр»",
		"GET `https://habr.com/kek/v2/me` → 200 application/json", "Pitfalls:", "`act`", "not_found", "/ru/article/new/"} {
		if !strings.Contains(text, want) {
			t.Errorf("recipe lacks %q:\n%s", want, text)
		}
	}
	// Личные данные из ответов API в рецепт не попадают: alias, почта, имя, длинный id.
	for _, secret := range []string{"ivan_petrov", "ivan@mail.ru", "Иван Петров", "1234567890"} {
		if strings.Contains(text, secret) {
			t.Errorf("recipe has personal data %q:\n%s", secret, text)
		}
	}
	// Второй сеанс дописывает раздел; шаги первого остаются.
	k = d.sessionClosed(browser.Closed{Session: "s-9b", Agent: "bot", Steps: steps[:1], StepsFile: "/data/browser-steps/s-9b.json"})
	if k["sessions"] != 2 {
		t.Fatalf("%+v", k)
	}
	raw, _ = os.ReadFile(path)
	if n := strings.Count(string(raw), "## Session "); n != 2 || !strings.Contains(string(raw), "s-7f3a") {
		t.Fatalf("sessions appended: %d\n%s", n, raw)
	}
	// Третий — подсказка про коннектор с файлом записи.
	k = d.sessionClosed(browser.Closed{Session: "s-c1", Agent: "bot", Steps: steps[:1], StepsFile: "/data/browser-steps/s-c1.json"})
	if h := k["hint"].(string); !strings.Contains(h, "connectors new habr --site habr.com --from-session /data/browser-steps/s-c1.json") {
		t.Fatal(h)
	}
	// Сеанс без страниц рецепта не оставляет.
	if k := d.sessionClosed(browser.Closed{Session: "s-x", Agent: "bot", Steps: []map[string]any{{"action": "close", "args": map[string]any{}}}}); k != nil {
		t.Fatalf("%+v", k)
	}
	// Заметка агента — действие gate4ai.knowledge_note (класс read, без вопроса владельцу).
	r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.knowledge_note",
		Args: map[string]any{"domain": "habr.com", "text": "Checked the account status; the editor at /ru/article/new/ needs a click on «Публикация»"}})
	if r.Status != "ok" {
		t.Fatalf("%+v", r)
	}
	raw, _ = os.ReadFile(path)
	rec, body, err := knowledge.ParseRecipe(raw)
	if err != nil || len(rec.Sessions) != 3 || rec.Sessions[2].Note == "" || rec.Sessions[1].Note != "" || !strings.Contains(body, "> Note (bot, ") || !strings.Contains(body, "Checked the account status") {
		t.Fatalf("%v %+v\n%s", err, rec.Sessions, body)
	}
	if r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.knowledge_note", Args: map[string]any{"domain": "nowhere.example", "text": "x"}}); r.Status == "ok" {
		t.Fatalf("%+v", r)
	}
	// Lookup: сеансы, последний, рецепт, сколько без заметки.
	out := d.SitesLookup("habr.com", "bot")
	kn := out["knowledge"].([]map[string]any)
	if len(kn) != 1 || kn[0]["sessions"] != 3 || kn[0]["sessions_without_note"] != 2 || kn[0]["status"] != "auto" || kn[0]["recipe"] != path || kn[0]["last"] == nil {
		t.Fatalf("%+v", kn)
	}
	if h := out["hint"].(string); !strings.Contains(h, "from 3 browser session(s)") || !strings.Contains(h, "connectors new habr") {
		t.Fatal(h)
	}
	// API владельца: заметка без автора.
	mux := http.NewServeMux()
	d.knowledgeRoutes(mux)
	req := httptest.NewRequest("POST", "/v1/knowledge/note", strings.NewReader(`{"domain":"habr.com","text":"owner's note"}`))
	req = req.WithContext(WithPrincipal(req.Context(), auth.OwnerVia("socket")))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)
	if rec2.Code != 200 {
		t.Fatal(rec2.Code, rec2.Body.String())
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "> Note (the owner, ") {
		t.Fatalf("%s", raw)
	}
	// Поставленный коннектор к сайту: рецепт не пишется, ответ зовёт коннектор.
	site := siteConn{manifest.Manifest{Connector: "shop", Version: "1", Sites: []access.Site{{Domain: "shop.example.org", Access: access.Allow}},
		Actions: []manifest.Action{{Name: "buy", Class: "write"}}, Contract: 1}}
	c, _ := connectors.NewInProcess(site)
	d.Registry.Add(c)
	k = d.sessionClosed(browser.Closed{Session: "s-s", Agent: "bot", Steps: []map[string]any{{"action": "open", "args": map[string]any{"url": "https://shop.example.org/"}, "result": map[string]any{"url": "https://shop.example.org/"}}}})
	if k["connector"] != "shop" || k["recipe"] != nil {
		t.Fatalf("%+v", k)
	}
	if _, err := os.Stat(filepath.Join(know, "gate4ai-shop")); err == nil {
		t.Fatal("recipe written although a connector is installed")
	}
}
