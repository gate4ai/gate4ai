// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

func readExample(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseExample(t *testing.T) {
	c, err := Parse(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Agents["claude-ai"].Profile; got != "assistant" {
		t.Fatalf("profile = %q", got)
	}
	p, ok := c.ProfileOf("claude-ai")
	if !ok || len(p.Rules) != 7 || !p.HasConnector("payments") || p.HasConnector("shell") {
		t.Fatalf("bad profile: %+v", p)
	}
	if c.Approvals.Timeout.D() != 30*time.Minute || c.Approvals.OnTimeout != "deny" {
		t.Fatalf("approvals: %+v", c.Approvals)
	}
	if c.Retention.EvidenceOther.D() != 90*24*time.Hour {
		t.Fatalf("default retention not set: %v", c.Retention.EvidenceOther)
	}
	if got := c.Profiles["assistant"].Rules[5].Obligations; len(got) != 1 || got[0] != "verify_statement" {
		t.Fatalf("obligations: %v", got)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"unknown field", "agents: {}\nprofiles: {p: {connectors: [email]}}\nbogus: 1\n", "field bogus not found"},
		{"no profiles", "agents: {}\n", "profiles: at least one profile"},
		{"unknown profile", "agents: {a: {profile: nope}}\nprofiles: {p: {connectors: [email]}}\n", `line 1: agents.a.profile: unknown profile "nope"`},
		{"bad effect", "profiles:\n  p:\n    connectors: [email]\n    rules:\n      - id: r1\n        action: email.send\n        effect: permit\n", `line 7: profiles.p.rules[0].effect: unknown effect "permit"`},
		{"no effect", "profiles: {p: {connectors: [email], rules: [{id: r, action: email.send}]}}\n", "effect is required"},
		{"dup id", "profiles: {p: {connectors: [email], rules: [{id: r, action: email.send, effect: deny}, {id: r, action: email.read, effect: deny}]}}\n", `duplicate rule id "r"`},
		{"bad action", "profiles: {p: {connectors: [email], rules: [{id: r, action: 'Email Send', effect: deny}]}}\n", "invalid action"},
		{"bad obligation", "profiles: {p: {connectors: [email], rules: [{id: r, action: email.send, effect: ask, obligations: [pray]}]}}\n", `unknown obligation "pray"`},
		{"bad channel", "profiles: {p: {connectors: [email]}}\napprovals: {channels: [pigeon]}\n", `unknown channel "pigeon"`},
		{"telegram secret inline", "profiles: {p: {connectors: [email]}}\napprovals: {telegram: {token: '123:abc', owner_id: 1}}\n", "vault alias"},
		{"bad duration", "profiles: {p: {connectors: [email]}}\napprovals: {timeout: soon}\n", "invalid duration"},
		{"limit empty", "profiles: {p: {connectors: [email], limits: [{id: l, action: email.send}]}}\n", "set per_day or amount_per_day"},
		{"yaml syntax", "agents: [\n", "yaml:"},
		{"browser engine", "profiles: {p: {connectors: [email]}}\nbrowser: {engine: selenium}\n", "must be stagehand or cdp"},
		{"browser key inline", "profiles: {p: {connectors: [email]}}\nbrowser: {model_key: sk-123}\n", "vault alias"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestExtraValidatorGetsLines(t *testing.T) {
	v := func(c *Config) Errors {
		return Errors{{Path: "profiles.assistant.rules[2].when", Msg: "bad cel"}}
	}
	_, err := Parse(readExample(t), v)
	if err == nil || !strings.Contains(err.Error(), "line 25: profiles.assistant.rules[2].when: bad cel") {
		t.Fatalf("got %v", err)
	}
}

func TestDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "90d": 90 * 24 * time.Hour, "1y": 365 * 24 * time.Hour, "1h30m": 90 * time.Minute, "": 0} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if FormatDuration(90*24*time.Hour) != "90d" {
		t.Fatal("format")
	}
}

func TestStoreVersionsRollbackOwner(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	var notified int
	s.Subscribe(func(*Config) { notified++ })
	owner := auth.OwnerVia("socket")
	agent := auth.AgentVia("claude-ai", "mcp-stdio")

	v2 := []byte("agents: {a: {profile: p}}\nprofiles: {p: {connectors: [email]}}\n")
	if _, err := s.Put(agent, v2, "agent tries"); err != auth.ErrNotOwner {
		t.Fatalf("agent must not change config: %v", err)
	}
	if _, err := s.Put(owner, []byte("profiles: {}"), "invalid"); err == nil {
		t.Fatal("invalid config must be rejected")
	}
	ver, err := s.Put(owner, v2, "simplify")
	if err != nil || ver.N != 2 || ver.Parent != 1 {
		t.Fatalf("put: %+v %v", ver, err)
	}
	if _, ok := s.Current().Agents["a"]; !ok || notified != 1 {
		t.Fatal("current not updated")
	}
	if _, err := s.Rollback(agent, 1); err != auth.ErrNotOwner {
		t.Fatal("agent rollback must fail")
	}
	ver, err = s.Rollback(owner, 1)
	if err != nil || ver.N != 3 {
		t.Fatalf("rollback: %+v %v", ver, err)
	}
	if _, ok := s.Current().Agents["claude-ai"]; !ok {
		t.Fatal("rollback did not restore")
	}
	if _, err := s.Rollback(owner, 99); err != ErrNoVersion {
		t.Fatalf("missing version: %v", err)
	}
	vs, _ := s.Versions()
	if len(vs) != 3 || vs[0].N != 3 || !strings.Contains(vs[0].Comment, "rollback to 1") {
		t.Fatalf("versions: %+v", vs)
	}
	// Повторное открытие видит текущую версию.
	s2, err := OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, n := s2.CurrentRaw(); n != 3 {
		t.Fatalf("reopen n=%d", n)
	}
	fi, _ := os.Stat(filepath.Join(dir, "versions", "000003.yaml"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", fi.Mode().Perm())
	}
	if _, err := OpenStore(t.TempDir(), nil); err == nil {
		t.Fatal("empty store without initial must fail")
	}
}
