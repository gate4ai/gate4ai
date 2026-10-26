// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"fmt"
	"encoding/json"
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
	// Папка знаний: рецепт к rzd.ru находится по поддомену; коннектора нет — подсказка про install_dev_connector.
	out = call("bot", "ticket.rzd.ru")
	kn := out["knowledge"].([]any)
	if len(kn) != 1 || kn[0].(map[string]any)["folder"] != "gate4ai-rzd" {
		t.Fatalf("%+v", kn)
	}
	if h := out["hint"].(string); !strings.Contains(h, "install_dev_connector") || !strings.Contains(h, "RECIPE.md") {
		t.Fatal(h)
	}
	// Ничего нет: подсказка — сеанс браузера и connectors new --from-session с именем по сайту.
	out = call("bot", "https://www.ozon.ru/cart")
	if h := out["hint"].(string); !strings.Contains(h, "connectors new ozon --site www.ozon.ru --from-session") || !strings.Contains(h, "add_credential") {
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
	if connectorNameFor("ticket.rzd.ru") != "rzd" || connectorNameFor("www.ozon.ru") != "ozon" || connectorNameFor("localhost") != "localhost" || connectorNameFor("1c.ru") != "site_1c" {
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
