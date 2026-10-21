// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
)

func TestGroupRules(t *testing.T) {
	rules := []config.Rule{
		{ID: "r1", Action: "files.read", Effect: "ask", Reason: "a"},
		{ID: "w1", Action: "email.send", Effect: "ask"},
		{ID: "r2", Action: "files.list", Effect: "ask", Reason: "b"},
		{ID: "c1", Action: "files.write", Effect: "ask", When: `glob(path, "~/x/**")`},
		{ID: "d1", Action: "files.append", Effect: "deny"},
		{ID: "g1", Action: "payments.*", Effect: "deny"},
		{ID: "g2", Action: "payments.pay", Effect: "deny"},
		{ID: "e2", Action: "email.read", Effect: "ask"},
	}
	got := groupRules(rules, []string{"files.read", "files.list", "files.write", "files.append", "email.send", "email.read", "payments.pay"})
	var labels []string
	for _, g := range got {
		labels = append(labels, g.Label)
	}
	want := []string{"files.{list, read}", "email.*", "files.write", "files.append", "payments.*", "payments.pay"}
	if len(labels) != len(want) {
		t.Fatalf("labels %q, want %q", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("labels %q, want %q", labels, want)
		}
	}
	if g := got[0]; !g.Mixed || g.Reason != "" || len(g.Rules) != 2 || g.Rules[0].ID != "r1" {
		t.Fatalf("group: %+v", g)
	}
	if g := got[1]; g.Mixed || len(g.Rules) != 2 {
		t.Fatalf("email group: %+v", g)
	}
	if !got[2].Single() {
		t.Fatal("rule with a condition is grouped with unconditional ones")
	}
}
