// SPDX-License-Identifier: FSL-1.1-ALv2

package config

import (
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

func TestEdit(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, []byte("# empty\nprofiles:\n  default: { connectors: [] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	own := auth.OwnerVia("test")
	v, err := s.Edit(own, "add agent", func(c *Config) error {
		if err := c.EnsureProfile("notes"); err != nil {
			return err
		}
		if err := c.SetProfileConnector("notes", "files", true); err != nil {
			return err
		}
		if err := c.PutRule("notes", Rule{ID: "r1", Action: "files.read", Effect: EffectAllow}); err != nil {
			return err
		}
		if err := c.PutRule("notes", Rule{ID: "r2", Action: "files.write", Effect: EffectDeny}); err != nil {
			return err
		}
		c.SetChannel("telegram", true)
		return c.SetAgent("claude-code", "notes", "")
	})
	if err != nil || v.N != 2 {
		t.Fatal(v, err)
	}
	c := s.Current()
	if c.Agents["claude-code"].Profile != "notes" || len(c.Profiles["notes"].Rules) != 2 {
		t.Fatalf("%+v", c)
	}
	if !c.ChannelOn("desktop") || !c.ChannelOn("telegram") {
		t.Fatal(c.Approvals.Channels)
	}
	raw, _ := s.CurrentRaw()
	// Значения по умолчанию не записываются в YAML.
	if strings.Contains(string(raw), "checkpoint_each") || strings.Contains(string(raw), "evidence_money") {
		t.Fatal(string(raw))
	}
	if _, err := s.Edit(own, "move", func(c *Config) error { return c.MoveRule("notes", "r2", -1) }); err != nil {
		t.Fatal(err)
	}
	if s.Current().Profiles["notes"].Rules[0].ID != "r2" {
		t.Fatal("move")
	}
	if _, err := s.Edit(own, "rm", func(c *Config) error { return c.RemoveRule("notes", "r9") }); err == nil {
		t.Fatal("unknown rule removed")
	}
	if _, err := s.Edit(own, "rm profile", func(c *Config) error { return c.RemoveProfile("notes") }); err == nil {
		t.Fatal("profile in use removed")
	}
	if _, err := s.Edit(own, "bad", func(c *Config) error { return c.SetAgent("Bad Name", "notes", "") }); err == nil {
		t.Fatal("bad name")
	}
	// Агенту правки недоступны.
	if _, err := s.Edit(auth.AgentVia("x", "socket"), "x", func(*Config) error { return nil }); err == nil {
		t.Fatal("agent edited config")
	}
	if p := (Profile{Rules: []Rule{{ID: "a"}, {ID: "a-2"}}}).UniqueRuleID("a"); p != "a-3" {
		t.Fatal(p)
	}
}

func TestProposalChanges(t *testing.T) {
	c := &Config{Profiles: map[string]Profile{"p": {Connectors: []string{}}}, Agents: map[string]Agent{"a": {Profile: "p"}}}
	for _, ch := range []Change{
		{Op: "drop_all"},
		{Op: OpAddRule, Action: "files.read"},
		{Op: OpAddRule, Action: "files.read", Effect: "maybe"},
		{Op: OpAddListItems, List: "trusted", Items: []string{"@mail.work"}},
		{Op: OpConnectorSetting, Connector: "email", SettingsJSON: `{"imap":{"password":"x"}}`},
		{Op: OpConnectorSetting, Connector: "email", SettingsJSON: `{"pw":"@mail.work"}`},
		{Op: OpConnectorSetting, Connector: "email", SettingsJSON: `[]`},
	} {
		if err := CheckChange(ch); err == nil {
			t.Fatalf("accepted %+v", ch)
		}
	}
	for _, ch := range []Change{
		{Op: OpAddRule, Action: "files.append", When: `glob(path, "~/n/**")`, Effect: EffectAllow, RuleReason: "notes"},
		{Op: OpAddListItems, List: "trusted", Items: []string{"a@x.org", "b@x.org"}},
		{Op: OpRemoveListItems, List: "trusted", Items: []string{"a@x.org"}},
		{Op: OpAddConnector, Connector: "files"},
		{Op: OpConnectorSetting, Connector: "files", SettingsJSON: `{"roots":["~/n"]}`},
	} {
		if err := c.ApplyChange("p", ch); err != nil {
			t.Fatal(ch, err)
		}
		if ch.Describe() == "" {
			t.Fatal("describe")
		}
	}
	p := c.Profiles["p"]
	if len(p.Rules) != 1 || p.Rules[0].ID != "agent-files-append" || !p.HasConnector("files") {
		t.Fatalf("%+v", p)
	}
	if len(c.Lists["trusted"]) != 1 || c.Connectors["files"]["roots"] == nil {
		t.Fatalf("%+v %+v", c.Lists, c.Connectors)
	}
}
