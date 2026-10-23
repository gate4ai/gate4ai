// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/platform/agentid"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

// fakeCallers — кто стоит за собеседником сокета (собеседник в тесте — сам
// тест, поэтому подменяется ответ).
type fakeCallers struct {
	mu sync.Mutex
	c  agentid.Caller
}

func (f *fakeCallers) set(c agentid.Caller) { f.mu.Lock(); f.c = c; f.mu.Unlock() }
func (f *fakeCallers) get(int) agentid.Caller {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c
}

func startPairing(t *testing.T) (*Daemon, *fakeCallers, client, client) {
	t.Helper()
	home, _ := os.MkdirTemp("", "g4ap")
	t.Cleanup(func() { os.RemoveAll(home) })
	n1, _ := connectors.NewInProcess(&notes{})
	d, err := New(Options{Home: filepath.Join(home, "data"), InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Connectors: []connectors.Client{n1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	fc := &fakeCallers{}
	d.Callers = fc.get
	ownerSock, agentSock := filepath.Join(home, "run", "o.sock"), filepath.Join(home, "run", "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go d.Serve(ctx, ownerSock, agentSock)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(agentSock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return d, fc, client{c: ipc.HTTPClient(ownerSock)}, client{c: ipc.HTTPClient(agentSock)}
}

var claudeIn = func(dir string) agentid.Caller { return agentid.Caller{Agent: "claude-code", Dir: dir, Session: "s1"} }

// Сокет владельца отвечает агенту agent_required, а не правами владельца.
func TestOwnerSocketRefusesAgents(t *testing.T) {
	_, fc, owner, _ := startPairing(t)
	var res map[string]any
	if code := owner.do(t, "GET", "/v1/agents", nil, &res); code != 200 {
		t.Fatalf("owner: %d %v", code, res)
	}
	fc.set(claudeIn("/home/u/proj"))
	if code := owner.do(t, "GET", "/v1/agents", nil, &res); code != 403 || errOf(res)["code"] != "agent_required" ||
		!strings.Contains(errOf(res)["message"].(string), "claude-code in /home/u/proj") {
		t.Fatalf("agent on the owner socket: %d %v", code, res)
	}
	if code := owner.do(t, "POST", "/v1/shutdown", nil, &res); code != 403 {
		t.Fatalf("agent stops the daemon: %d", code)
	}
	// health — без опознания (daemon status работает у всех).
	if code := owner.do(t, "GET", "/v1/health", nil, nil); code != 200 {
		t.Fatal("health", code)
	}
}

func pair(t *testing.T, anon client, body map[string]any) map[string]any {
	t.Helper()
	var res map[string]any
	if code := anon.do(t, "POST", "/v1/pair", body, &res); code != 200 || res["status"] != "pending" || res["code"] == "" {
		t.Fatalf("pair: %d %v", code, res)
	}
	return res
}

func TestPairingFlow(t *testing.T) {
	d, fc, _, anon := startPairing(t)
	fc.set(claudeIn("/home/u/proj"))
	key := "g4a_madebytheagent"
	res := pair(t, anon, map[string]any{"agent": "claude-code", "key_hash": state.HashToken(key)})
	id := res["id"].(string)
	if !strings.Contains(res["message"].(string), res["code"].(string)) || res["dir"] != nil {
		t.Fatalf("answer: %v", res)
	}
	// До решения ключ не работает.
	if code := (client{c: anon.c, token: key}).do(t, "GET", "/v1/whoami", nil, nil); code != 401 {
		t.Fatal("pending key works", code)
	}
	ps, _ := d.State.PendingPairings()
	if len(ps) != 1 || ps[0].Kind != "claude-code" || !strings.Contains(string(ps[0].Caller), "/home/u/proj") {
		t.Fatalf("owner sees: %+v", ps)
	}
	// Ожидание решения долгим опросом.
	done := make(chan map[string]any)
	go func() {
		var st map[string]any
		anon.do(t, "GET", "/v1/pair/"+id+"?wait=5s", nil, &st)
		done <- st
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := d.DecidePairing(context.Background(), id, true, "", "webui"); err != nil {
		t.Fatal(err)
	}
	if st := <-done; st["status"] != "approved" {
		t.Fatalf("wait: %v", st)
	}
	// Новый агент — в конфигурации, со своим пустым профилем.
	if a, ok := d.Config.Current().Agents["claude-code"]; !ok || a.Profile != "claude-code" {
		t.Fatalf("agent: %+v", d.Config.Current().Agents)
	}
	agent := client{c: anon.c, token: key}
	var who map[string]any
	if code := agent.do(t, "GET", "/v1/whoami", nil, &who); code != 200 || who["agent"] != "claude-code" {
		t.Fatalf("whoami: %d %v", code, who)
	}
	if _, err := d.DecidePairing(context.Background(), id, true, "", "webui"); err == nil {
		t.Fatal("decided twice")
	}

	// Ключ для каталога: агент того же типа наследует профиль claude-code.
	if _, err := d.Config.Edit(ownerP(), "p", func(c *configT) error { return c.SetAgent("claude-code", "p", "") }); err != nil {
		t.Fatal(err)
	}
	k2 := "g4a_proj_key"
	res = pair(t, anon, map[string]any{"agent": "claude-code-proj", "key_hash": state.HashToken(k2), "here": true})
	if _, err := d.DecidePairing(context.Background(), res["id"].(string), true, "", "telegram"); err != nil {
		t.Fatal(err)
	}
	if a := d.Config.Current().Agents["claude-code-proj"]; a.Profile != "p" || !strings.Contains(a.Description, "/home/u/proj") {
		t.Fatalf("bound agent: %+v", a)
	}
	bound := client{c: anon.c, token: k2}
	fc.set(claudeIn("/home/u/proj/sub")) // подкаталог проекта
	var out map[string]any
	if code := bound.do(t, "POST", "/v1/call", map[string]any{"action": "notes.append", "args": map[string]any{"text": "hi"}}, &out); code != 200 || out["status"] != "pending" {
		t.Fatalf("bound key in its directory: %d %v", code, out)
	}
	// Запрос подтверждения — с каталогом агента.
	if a, _ := d.State.GetApproval(out["id"].(string)); a == nil || !strings.Contains(a.Preview, "from: /home/u/proj/sub") {
		t.Fatalf("approval: %+v", a)
	}
	// Отказ политики — квитанция сразу.
	bound.do(t, "POST", "/v1/call", map[string]any{"action": "notes.append", "args": map[string]any{"text": "my password"}}, &out)
	// Каталог агента — в квитанции.
	es, _ := d.Journal.List(audit.Filter{Agent: "claude-code-proj", Limit: 1, Desc: true})
	if len(es) != 1 || es[0].Receipt.Caller == nil || es[0].Receipt.Caller.Dir != "/home/u/proj/sub" || es[0].Receipt.Caller.Source != "peer" {
		t.Fatalf("receipt caller: %+v", es)
	}
	// Агент другого проекта с тем же ключом — отказ.
	fc.set(claudeIn("/home/u/other"))
	if code := bound.do(t, "GET", "/v1/whoami", nil, &out); code != 401 || !strings.Contains(errOf(out)["message"].(string), "works only for an agent started in /home/u/proj") {
		t.Fatalf("bound key elsewhere: %d %v", code, out)
	}
	// Ключ без привязки работает откуда угодно.
	if code := agent.do(t, "GET", "/v1/whoami", nil, nil); code != 200 {
		t.Fatal("unbound key", code)
	}

	// Отклонённый запрос: ключ — не токен, агент узнаёт об отказе.
	k3 := "g4a_rejected"
	res = pair(t, anon, map[string]any{"agent": "codex", "key_hash": state.HashToken(k3)})
	if _, err := d.DecidePairing(context.Background(), res["id"].(string), false, "", "webui"); err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	anon.do(t, "GET", "/v1/pair/"+res["id"].(string), nil, &st)
	if st["status"] != "rejected" || !strings.Contains(st["message"].(string), "do not retry") {
		t.Fatalf("rejected: %v", st)
	}
	if _, ok := d.Config.Current().Agents["codex"]; ok {
		t.Fatal("rejected agent added")
	}
}

func TestPairValidation(t *testing.T) {
	_, fc, _, anon := startPairing(t)
	for _, body := range []map[string]any{
		{"agent": "Bad Name", "key_hash": state.HashToken("k")},
		{"agent": "ok", "key_hash": "short"},
	} {
		if code := anon.do(t, "POST", "/v1/pair", body, nil); code != 400 {
			t.Fatalf("%v: %d", body, code)
		}
	}
	// Каталог неизвестен — ключ для каталога не выдаётся.
	fc.set(agentid.Caller{Agent: "claude-code"})
	var res map[string]any
	if code := anon.do(t, "POST", "/v1/pair", map[string]any{"agent": "x", "key_hash": state.HashToken("k"), "here": true}, &res); code != 400 || errOf(res)["code"] != "dir_unknown" {
		t.Fatalf("here without dir: %d %v", code, res)
	}
}

type configT = config.Config

func ownerP() auth.Principal { return auth.OwnerVia("test") }
