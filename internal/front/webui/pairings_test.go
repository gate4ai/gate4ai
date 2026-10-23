// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/state"
)

// Запрос агента на подключение: код, каталог и процессы видны владельцу;
// подтверждение с выбором прав добавляет агента и делает его ключ токеном.
func TestPairingApproval(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	caller, _ := json.Marshal(audit.Caller{Kind: "claude-code", Dir: "/home/u/proj", Source: "peer", Session: "s-42",
		Chain: []audit.CallerProc{{PID: 3, Name: "gate4ai"}, {PID: 2, Name: "bash"}, {PID: 1, Name: "claude"}}})
	p, err := d.State.PutPairing(&state.Pairing{Agent: "claude-code-proj", Kind: "claude-code", KeyHash: state.HashToken("g4a_k"),
		Dir: "/home/u/proj", Caller: caller, Via: "agent-socket"})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"/", "/agents", "/approvals"} {
		h := get(page)
		if !strings.Contains(h, p.Code) || !strings.Contains(h, "просит подключиться") || !strings.Contains(h, "<code>/home/u/proj</code>") ||
			!strings.Contains(h, "claude → bash → gate4ai") || !strings.Contains(h, "s-42") {
			t.Fatalf("%s: no pairing request:\n%s", page, h)
		}
	}
	h := post("/pairings/"+p.ID, url.Values{"decision": {"approve"}, "profile": {"_new"}, "back": {"/agents"}})
	if !strings.Contains(h, "claude-code-proj подключён") {
		t.Fatalf("approve: %s", h)
	}
	if a, ok := d.Config.Current().Agents["claude-code-proj"]; !ok || a.Profile != "claude-code-proj" {
		t.Fatalf("agent: %+v", d.Config.Current().Agents)
	}
	if ag, err := d.State.AgentByToken("g4a_k"); err != nil || ag != "claude-code-proj" || d.State.TokenDir("g4a_k") != "/home/u/proj" {
		t.Fatalf("key: %q %v", ag, err)
	}
	if h := post("/pairings/"+p.ID, url.Values{"decision": {"approve"}}); !strings.Contains(h, "уже") && !strings.Contains(h, "Уже") {
		t.Fatalf("second decision: %s", h)
	}
	// Отклонение.
	p2, _ := d.State.PutPairing(&state.Pairing{Agent: "codex", KeyHash: state.HashToken("g4a_k2"), Via: "agent-socket"})
	if h := post("/pairings/"+p2.ID, url.Values{"decision": {"reject"}}); !strings.Contains(h, "Подключение codex отклонено") {
		t.Fatalf("reject: %s", h)
	}
	if _, err := d.State.AgentByToken("g4a_k2"); err == nil {
		t.Fatal("rejected key works")
	}
}
