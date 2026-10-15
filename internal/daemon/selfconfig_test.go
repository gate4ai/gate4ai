// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

type proposalSink struct{ got []*state.Proposal }

func (p *proposalSink) Notify(*state.Approval)        {}
func (p *proposalSink) Resolved(*state.Approval)      {}
func (p *proposalSink) Denied(string, string, string) {}
func (p *proposalSink) Proposed(x *state.Proposal)    { p.got = append(p.got, x) }

func TestSelfConfigProposal(t *testing.T) {
	n := &notes{}
	c, _ := connectors.NewInProcess(n)
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte(cfg + "  other:\n    connectors: []\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sink := &proposalSink{}
	d.AddNotifier(sink)
	ctx := context.Background()
	call := func(action string, args map[string]any) pipeline.Result {
		return d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: action, Args: args})
	}
	// Агент видит, что ему разрешено.
	if r := call("gate4ai.policy_summary", map[string]any{}); r.Status != "ok" {
		t.Fatalf("%+v", r)
	}
	// Секреты предлагать нельзя.
	r := call("gate4ai.propose_change", map[string]any{"summary": "x", "changes": []any{
		map[string]any{"op": "connector_settings", "connector": "notes", "settings_json": `{"password":"@mail.work"}`}}})
	if r.Status == "ok" {
		t.Fatalf("secret proposal accepted: %+v", r)
	}
	// Неверный CEL не превращается в предложение.
	r = call("gate4ai.propose_change", map[string]any{"summary": "x", "changes": []any{
		map[string]any{"op": "add_rule", "action": "notes.append", "when": "nosuch(", "effect": "allow"}}})
	if r.Status == "ok" {
		t.Fatalf("invalid rule accepted: %+v", r)
	}
	v0 := d.Config.Current()
	r = call("gate4ai.propose_change", map[string]any{"summary": "append without asking", "reason": "daily log", "changes": []any{
		map[string]any{"op": "add_rule", "action": "notes.append", "effect": "allow", "rule_reason": "log"}}})
	if r.Status != "ok" {
		t.Fatalf("%+v", r)
	}
	id := r.Result.(map[string]any)["id"].(string)
	if d.Config.Current() != v0 || len(sink.got) != 1 || sink.got[0].Agent != "bot" {
		t.Fatal("proposal must not change the configuration by itself")
	}
	// Чужое предложение агент не видит.
	if r := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.proposal_status", Args: map[string]any{"id": "p_nope"}}); r.Status == "ok" {
		t.Fatal("unknown proposal")
	}
	v, err := d.ApplyProposal(ctx, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rules := d.Config.Current().Profiles["p"].Rules; len(rules) != 2 || rules[1].Action != "notes.append" || rules[1].Effect != "allow" {
		t.Fatalf("%+v", rules)
	}
	if _, err := d.ApplyProposal(ctx, id, nil); err == nil {
		t.Fatal("applied twice")
	}
	p, _ := d.State.GetProposal(id)
	if p.Status != state.ProposalApplied || p.Version != v.N {
		t.Fatalf("%+v", p)
	}
	// Предложения выключены владельцем.
	if _, err := d.Config.Edit(auth.OwnerVia("test"), "off", func(c *config.Config) error { c.Proposals.Disabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if r := call("gate4ai.policy_summary", map[string]any{}); r.Status == "ok" {
		t.Fatal("disabled")
	}
	for _, tl := range d.Tools(auth.AgentVia("bot", "socket")) {
		if tl.Connector == SelfConnector {
			t.Fatal("hidden when disabled")
		}
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
