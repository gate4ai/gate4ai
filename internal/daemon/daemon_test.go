// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/sdk-go/manifest"
)

type notes struct{ lines []string }

func (n *notes) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "notes", Actions: []manifest.Action{
		{Name: "read", Class: "read", Description: "read notes"},
		{Name: "append", Class: "write", InputSchema: manifest.MustSchema(`{ type: object, properties: { text: { type: string } }, required: [text] }`)},
	}}
}
func (n *notes) Execute(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	if r.Action == "append" {
		n.lines = append(n.lines, r.Args["text"].(string))
	}
	return abi.ExecResult{Structured: n.lines}, nil
}
func (n *notes) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified}, nil
}

type other struct{}

func (other) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "bank", Actions: []manifest.Action{{Name: "pay", Class: "money", Verify: "payment_status"}}}
}
func (other) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{}, nil
}
func (other) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

const cfg = `
agents:
  bot: { profile: p }
profiles:
  p:
    connectors: [notes]
    rules:
      - { id: no-secrets, action: notes.append, when: "text.contains('password')", effect: deny, reason: no secrets in notes }
`

type client struct {
	c     *http.Client
	token string
}

func (c client) do(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, "http://gate4ai"+path, rd)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestDaemonSockets(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4ad")
	defer os.RemoveAll(home)
	n1, _ := connectors.NewInProcess(&notes{})
	n2, _ := connectors.NewInProcess(other{})
	d, err := New(Options{Home: filepath.Join(home, "data"), InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Connectors: []connectors.Client{n1, n2}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ownerSock, agentSock := filepath.Join(home, "run", "o.sock"), filepath.Join(home, "run", "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, ownerSock, agentSock)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(agentSock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner := client{c: ipc.HTTPClient(ownerSock)}
	anon := client{c: ipc.HTTPClient(agentSock)}

	// Агентский сокет без токена — отказ.
	if code := anon.do(t, "GET", "/v1/tools", nil, nil); code != 401 {
		t.Fatal("agent socket without token", code)
	}
	// Владелец выдаёт токен.
	var tok struct{ Token string }
	if code := owner.do(t, "POST", "/v1/agents/bot/token", map[string]string{"label": "test"}, &tok); code != 200 || !strings.HasPrefix(tok.Token, "g4a_") {
		t.Fatal(code, tok)
	}
	if code := owner.do(t, "POST", "/v1/agents/ghost/token", nil, nil); code != 404 {
		t.Fatal("unknown agent token", code)
	}
	agent := client{c: ipc.HTTPClient(agentSock), token: tok.Token}

	// Каталог агента — только коннекторы профиля.
	var tools []Tool
	agent.do(t, "GET", "/v1/tools", nil, &tools)
	if len(tools) != 2 || tools[0].Connector != "notes" {
		t.Fatalf("%+v", tools)
	}
	var all []Tool
	owner.do(t, "GET", "/v1/tools", nil, &all)
	if len(all) != 3 {
		t.Fatal("owner sees all", len(all))
	}

	// Права владельца агенту не даются ни на каком сокете.
	if code := agent.do(t, "GET", "/v1/log", nil, nil); code != 403 {
		t.Fatal("agent read log", code)
	}
	if code := agent.do(t, "PUT", "/v1/policy", map[string]string{"yaml": "x"}, nil); code != 403 {
		t.Fatal("agent changed policy", code)
	}
	agentOnOwnerSock := client{c: ipc.HTTPClient(ownerSock), token: tok.Token}
	if code := agentOnOwnerSock.do(t, "GET", "/v1/approvals", nil, nil); code != 403 {
		t.Fatal("token on owner socket must still be agent", code)
	}

	// write → ask по умолчанию; подтверждение владельцем.
	var res map[string]any
	agent.do(t, "POST", "/v1/call", map[string]any{"action": "notes.append", "args": map[string]any{"text": "hello"}}, &res)
	if res["status"] != "pending" || res["code"] != "pending_approval" {
		t.Fatal(res)
	}
	id := res["id"].(string)
	var pend []map[string]any
	owner.do(t, "GET", "/v1/approvals?status=pending", nil, &pend)
	if len(pend) != 1 || !strings.Contains(pend[0]["preview"].(string), "hello") {
		t.Fatal(pend)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		owner.do(t, "POST", "/v1/approvals/"+id+"/decide", map[string]any{"approve": true}, nil)
	}()
	agent.do(t, "GET", "/v1/status/"+id+"?wait=5s", nil, &res)
	if res["status"] != "ok" {
		t.Fatal(res)
	}
	// deny по правилу.
	agent.do(t, "POST", "/v1/call", map[string]any{"action": "notes.append", "args": map[string]any{"text": "my password"}}, &res)
	if res["code"] != "denied_by_policy" || !strings.Contains(res["message"].(string), "no secrets in notes") {
		t.Fatal(res)
	}
	// Действие вне профиля не видно.
	agent.do(t, "POST", "/v1/call", map[string]any{"action": "bank.pay", "args": map[string]any{}}, &res)
	if res["code"] != "unknown_action" {
		t.Fatal(res)
	}
	// Владелец видит журнал и проверяет цепочку.
	var log []map[string]any
	owner.do(t, "GET", "/v1/log", nil, &log)
	if len(log) != 2 {
		t.Fatal("log", len(log))
	}
	var ver map[string]any
	owner.do(t, "POST", "/v1/journal/verify", nil, &ver)
	if ver["ok"] != true {
		t.Fatal(ver)
	}
	// Симулятор политики.
	var dec map[string]any
	owner.do(t, "POST", "/v1/policy/test", map[string]any{"agent": "bot", "action": "notes.read", "args": map[string]any{}}, &dec)
	if dec["effect"] != "allow" {
		t.Fatal(dec)
	}
	// Изменение политики и откат.
	var bad map[string]map[string]string
	if code := owner.do(t, "PUT", "/v1/policy", map[string]string{"yaml": "agents: {}\nprofiles: {p: {connectors: [notes], rules: [{id: x, action: notes.read, when: 'nope ==', effect: deny}]}}"}, &bad); code != 400 || !strings.Contains(bad["error"]["message"], "CEL") {
		t.Fatal(code, bad)
	}
	newCfg := cfg + "      - { id: no-read, action: notes.read, effect: deny }\n"
	if code := owner.do(t, "PUT", "/v1/policy", map[string]string{"yaml": newCfg, "comment": "lock reads"}, nil); code != 200 {
		t.Fatal(code)
	}
	agent.do(t, "POST", "/v1/call", map[string]any{"action": "notes.read"}, &res)
	if res["code"] != "denied_by_policy" {
		t.Fatal("new policy not applied", res)
	}
	owner.do(t, "POST", "/v1/policy/rollback", map[string]int{"version": 1}, nil)
	agent.do(t, "POST", "/v1/call", map[string]any{"action": "notes.read"}, &res)
	if res["status"] != "ok" {
		t.Fatal("rollback not applied", res)
	}
	// Отзыв токена.
	owner.do(t, "POST", "/v1/agents/bot/revoke", nil, nil)
	if code := agent.do(t, "GET", "/v1/tools", nil, nil); code != 401 {
		t.Fatal("revoked token works", code)
	}
}
