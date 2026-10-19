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
	if !strings.Contains(a.Preview, "Правило: разрешать notes.append") || !strings.Contains(a.Preview, "(профиль other)") {
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
	if _, err := devManifest("/nonexistent"); err == nil {
		t.Fatal("no manifest")
	}
}

// Своя сборка — новый код в daemon: даже при правиле allow на change_policy
// её ставит только человек; обычные правки allow пропускает.
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
	if r.Status != pipeline.StatusOK {
		t.Fatalf("allow still allows ordinary changes: %+v", r)
	}
}
