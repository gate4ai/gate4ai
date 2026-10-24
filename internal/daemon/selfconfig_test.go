// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/sdk-go/access"
)

func TestSelfConfigIsAnOrdinaryAction(t *testing.T) {
	n := &notes{}
	c, _ := connectors.NewInProcess(n)
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte(cfg + "  other:\n    connectors: []\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	call := func(action string, args map[string]any) pipeline.Result {
		return d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: action, Args: args})
	}
	if r := call("gate4ai.policy_summary", map[string]any{}); r.Status != "ok" {
		t.Fatalf("%+v", r)
	}
	// Секреты задать нельзя, неверный CEL отклоняется ещё до запроса подтверждения.
	for _, ch := range []map[string]any{
		{"op": "connector_settings", "connector": "notes", "settings_json": `{"password":"@mail.work"}`},
		{"op": "add_rule", "action": "notes.append", "when": "nosuch(", "effect": "allow"},
	} {
		if r := call("gate4ai.change_policy", map[string]any{"summary": "x", "changes": []any{ch}}); r.Code != pipeline.CodeInvalidArgs {
			t.Fatalf("%+v", r)
		}
	}
	// Класс write: по умолчанию — запрос подтверждения с понятным описанием.
	r := call("gate4ai.change_policy", map[string]any{"summary": "append without asking", "reason": "daily log", "changes": []any{
		map[string]any{"op": "add_rule", "action": "notes.append", "effect": "allow"},
		map[string]any{"op": "add_rule", "profile": "other", "action": "notes.read", "effect": "deny"}}})
	if r.Status != pipeline.StatusPending {
		t.Fatalf("%+v", r)
	}
	a, _ := d.State.GetApproval(r.ID)
	if !strings.Contains(a.Preview, "Rule: allow notes.append") || !strings.Contains(a.Preview, "(profile other)") {
		t.Fatal(a.Preview)
	}
	if len(d.Config.Current().Profiles["p"].Rules) != 1 {
		t.Fatal("nothing changes before approval")
	}
	// Подтверждение в любом канале (здесь — Telegram) применяет изменение.
	res, err := d.Pipeline.Decide(ctx, r.ID, true, "telegram", "owner")
	if err != nil || res.Status != "ok" {
		t.Fatalf("%+v %v", res, err)
	}
	cur := d.Config.Current()
	if rules := cur.Profiles["p"].Rules; len(rules) != 2 || rules[1].Effect != "allow" {
		t.Fatalf("%+v", rules)
	}
	if rules := cur.Profiles["other"].Rules; len(rules) != 1 || rules[0].Effect != "deny" {
		t.Fatalf("%+v", rules)
	}
	// Правило может запретить самонастройку, как любое действие.
	if _, err := d.Config.Edit(auth.OwnerVia("test"), "deny", func(c *config.Config) error {
		return c.PutRule("p", config.Rule{ID: "no-self", Action: "gate4ai.change_policy", Effect: "deny"})
	}); err != nil {
		t.Fatal(err)
	}
	if r := call("gate4ai.change_policy", map[string]any{"summary": "x", "changes": []any{map[string]any{"op": "add_connector", "connector": "notes"}}}); r.Status != pipeline.StatusDenied {
		t.Fatalf("%+v", r)
	}
}

func TestLastSeen(t *testing.T) {
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), InitialConfig: []byte("agents:\n  bot: { profile: p }\nprofiles:\n  p: { connectors: [] }\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok, _ := d.State.IssueToken("bot", "", "agent")
	d.State.Seen("bot", "mcp", tok)
	seen, _ := d.State.LastSeen()
	toks, _ := d.State.Tokens()
	if seen["bot"].Via != "mcp" || toks[0].LastUsed.IsZero() {
		t.Fatal(seen, toks)
	}
}

// remove_rule принимает id в том же виде, как его показывает policy_summary.
func TestChangeRuleIDAlias(t *testing.T) {
	ch, err := changesOf(map[string]any{"changes": []any{map[string]any{"op": "remove_rule", "id": "agent-lk-all"}}})
	if err != nil || ch[0].RuleID != "agent-lk-all" {
		t.Fatal(ch, err)
	}
	m := (selfConnector{}).Manifest()
	a, _ := m.Find("change_policy")
	if _, err := a.Normalize(map[string]any{"summary": "x", "changes": []any{map[string]any{"op": "remove_rule", "id": "r1"}}}); err != nil {
		t.Fatal("id is a known field of a change:", err)
	}
}

// Агент, который пишет коннектор, ставит свою сборку из папки через
// change_policy (подтверждение владельца) — без сокета владельца.
func TestInstallDevConnectorByAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a WASM module")
	}
	c, _ := connectors.NewInProcess(&notes{})
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	folder, _ := filepath.Abs("../connectors/wasm/testdata/probe")
	r := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "install my connector", "changes": []any{map[string]any{"op": "install_dev_connector", "folder": folder}}}})
	if r.Status != pipeline.StatusPending {
		t.Fatalf("%+v", r)
	}
	a, _ := d.State.GetApproval(r.ID)
	if !strings.Contains(a.Preview, "own build") || !strings.Contains(a.Preview, "probe") || !strings.Contains(a.Preview, "127.0.0.1") {
		t.Fatal(a.Preview)
	}
	res, err := d.Pipeline.Decide(ctx, r.ID, true, "webui", "owner")
	if err != nil || res.Status != "ok" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, ok := d.Registry.Get("probe"); !ok || !d.Config.Current().Profiles["p"].HasConnector("probe") {
		t.Fatal("installed and given to the agent's profile")
	}
	// Нативный код из папки агент поставить не может.
	if _, _, err := devManifest("/nonexistent"); err == nil {
		t.Fatal("no manifest")
	}
}

// Своя сборка — новый код в daemon: её ставит только человек; правило allow
// на change_policy не действует и на обычные правки (policy.OwnerOnly).
func TestInstallDevAlwaysAsks(t *testing.T) {
	c, _ := connectors.NewInProcess(&notes{})
	conf := cfg + "      - { id: self-allow, action: gate4ai.change_policy, effect: allow }\n"
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(conf)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	folder, _ := filepath.Abs("../connectors/wasm/testdata/probe")
	r := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "install", "changes": []any{map[string]any{"op": "install_dev_connector", "folder": folder}}}})
	if r.Status != pipeline.StatusPending || !strings.Contains(r.Reason, "always needs the owner") {
		t.Fatalf("%+v", r)
	}
	r = d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "list", "changes": []any{map[string]any{"op": "add_list_items", "list": "x", "items": []any{"a"}}}}})
	if r.Status != pipeline.StatusPending || !strings.Contains(r.Reason, "self-allow (allow) does not apply") {
		t.Fatalf("allow on change_policy has no effect: %+v", r)
	}
	if w := PolicySummary(d.Config.Current(), "bot")["warnings"]; w == nil {
		t.Fatal("policy_summary warns about the rule")
	}
}

// Сайты агента и учётки — без правки YAML: владелец — gate4ai sites, агент —
// add_setting_items и add_credential (всегда с подтверждением владельца).
func TestSitesAndCredentialChanges(t *testing.T) {
	c, _ := connectors.NewInProcess(&notes{})
	conf := cfg + "      - { id: self-allow, action: gate4ai.change_policy, effect: allow }\nconnectors:\n  browser: { model_key: \"@llm.key\" }\n  notes: { root: \"~/n\" }\n"
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(conf)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	call := func(changes ...map[string]any) pipeline.Result {
		ch := make([]any, len(changes))
		for i, x := range changes {
			ch[i] = x
		}
		return d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{"summary": "x", "changes": ch}})
	}
	// Сводка показывает несекретные настройки коннекторов профиля.
	sum := PolicySummary(d.Config.Current(), "bot")
	if st := sum["settings"].(map[string]map[string]any); st["notes"]["root"] != "~/n" {
		t.Fatalf("%+v", sum["settings"])
	}
	r := call(map[string]any{"op": "set_site_access", "domain": "login.example", "access": "read"})
	if r.Status != pipeline.StatusPending {
		t.Fatalf("sites: the owner approves: %+v", r)
	}
	if res, err := d.Pipeline.Decide(ctx, r.ID, true, "webui", "owner"); err != nil || res.Status != "ok" {
		t.Fatal(res, err)
	}
	if st, ok := access.MatchSite(d.Config.Current().Access.Sites, "https://login.example/"); !ok || st.Access != access.Read {
		t.Fatal(d.Config.Current().Access.Sites)
	}
	if r := call(map[string]any{"op": "set_site_access", "domain": "bad host", "access": "read"}); r.Code != pipeline.CodeInvalidArgs {
		t.Fatalf("bad domain: %+v", r)
	}
	if r := call(map[string]any{"op": "add_setting_items", "connector": "browser", "key": "model_key", "items": []any{"@x"}}); r.Code != pipeline.CodeInvalidArgs {
		t.Fatalf("secrets via settings: %+v", r)
	}
	cred := `{"bind":["https://login.example"],"form":{"fields":[{"selector":"#p","secret":"@site.password"}],"submit":{"press":"Enter"}},"use":["p"]}`
	r = call(map[string]any{"op": "add_credential", "account": "@site", "credential_json": cred})
	if r.Status != pipeline.StatusPending || !strings.Contains(r.Reason, "always needs the owner") {
		t.Fatalf("add_credential always asks: %+v", r)
	}
	if res, err := d.Pipeline.Decide(ctx, r.ID, true, "webui", "owner"); err != nil || res.Status != "ok" {
		t.Fatal(res, err)
	}
	if cr, ok := d.Config.Current().Credentials["@site"]; !ok || cr.Form == nil || cr.Form.Fields[0].Secret != "@site.password" {
		t.Fatalf("%+v", d.Config.Current().Credentials)
	}
}
