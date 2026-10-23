// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/agentid"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

func TestPickKey(t *testing.T) {
	now := time.Now()
	keys := []AgentKey{
		{Agent: "claude-code", Kind: "claude-code", Key: "k1", Created: now.Add(-time.Hour)},
		{Agent: "claude-other", Kind: "claude-code", Key: "k2", Created: now},
		{Agent: "claude-proj", Kind: "claude-code", Key: "k3", Dir: "/home/u/proj"},
		{Agent: "claude-proj-sub", Kind: "claude-code", Key: "k4", Dir: "/home/u/proj/sub"},
		{Agent: "codex", Kind: "codex", Key: "k5"},
	}
	for _, c := range []struct{ name, kind, dir, want string }{
		{"", "claude-code", "/home/u/else", "claude-code"}, // общий ключ типа: с именем типа важнее более нового
		{"", "claude-code", "/home/u/proj/x", "claude-proj"},
		{"", "claude-code", "/home/u/proj/sub/y", "claude-proj-sub"}, // самый глубокий каталог
		{"", "codex", "/home/u/proj", "codex"},
		{"claude-other", "", "", "claude-other"},
		{"codex", "claude-code", "/home/u/proj", "codex"}, // имя важнее типа
		{"", "gemini", "/", ""},
		{"", "", "/", ""},
	} {
		k, ok := PickKey(keys, c.name, c.kind, c.dir)
		if (c.want == "") == ok || k.Agent != c.want {
			t.Errorf("PickKey(%q,%q,%q) = %q, want %q", c.name, c.kind, c.dir, k.Agent, c.want)
		}
	}
}

type pairHarness struct {
	*harness
	d     *daemon.Daemon
	keys  string
	mu    sync.Mutex
	agent agentid.Caller // кто «вызывает» — и для CLI (Self), и для daemon (Callers)
}

func startPair(t *testing.T) *pairHarness {
	t.Helper()
	home, _ := os.MkdirTemp("", "g4apair")
	t.Cleanup(func() { os.RemoveAll(home) })
	c, _ := connectors.NewInProcess(mailFake{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), InitialConfig: []byte(policy), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}})
	if err != nil {
		t.Fatal(err)
	}
	p := &pairHarness{d: d, keys: filepath.Join(home, "agents")}
	d.Callers = func(int) agentid.Caller { p.mu.Lock(); defer p.mu.Unlock(); return p.agent }
	ctx, cancel := context.WithCancel(context.Background())
	p.harness = &harness{owner: filepath.Join(home, "o.sock"), agentSock: filepath.Join(home, "a.sock")}
	go d.Serve(ctx, p.owner, p.agentSock)
	t.Cleanup(func() { cancel(); d.Close() })
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(p.agentSock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return p
}

func (p *pairHarness) as(c agentid.Caller) { p.mu.Lock(); p.agent = c; p.mu.Unlock() }

// run — команда от агента (CLAUDECODE=1 в окружении, если агент — Claude Code).
func (p *pairHarness) run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	p.mu.Lock()
	who := p.agent
	p.mu.Unlock()
	env := &Env{Stdin: strings.NewReader(""), Stdout: out, Stderr: errb,
		Getenv: func(k string) string {
			if k == "CLAUDECODE" && who.Agent == "claude-code" {
				return "1"
			}
			return ""
		},
		Connect: func(tok string) *Client {
			if tok != "" {
				return NewSocketClient(p.agentSock, tok)
			}
			return NewSocketClient(p.owner, "")
		},
		Pair:   func(string) *Client { return NewSocketClient(p.agentSock, "") },
		Self:   func() agentid.Caller { return who },
		KeyDir: p.keys}
	code := Run(context.Background(), env, args)
	return code, out.String() + errb.String()
}

func TestAgentPairsItself(t *testing.T) {
	p := startPair(t)
	p.as(agentid.Caller{Agent: "claude-code", Dir: "/home/u/proj"})

	// Первый вызов агента: запрос на подключение с кодом, ключ сохранён.
	code, out := p.run(t, "tools", "--json")
	res := jsonOf(t, out)
	if code != ExitPending || res["status"] != "pending" || errOf(res)["code"] != "pairing_required" || res["code"] == "" ||
		!strings.Contains(errOf(res)["message"].(string), res["code"].(string)) {
		t.Fatalf("first call: %d %s", code, out)
	}
	pairCode := res["code"].(string)
	b, err := os.ReadFile(filepath.Join(p.keys, "claude-code.json"))
	if err != nil || !strings.Contains(string(b), `"pairing"`) {
		t.Fatalf("key file: %s %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(p.keys, "claude-code.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", fi.Mode())
	}
	// Повтор до решения — тот же код, новых запросов нет.
	if code, out := p.run(t, "mail", "read"); code != ExitPending || jsonOf(t, out)["code"] != pairCode {
		t.Fatalf("repeat: %d %s", code, out)
	}
	ps, _ := p.d.State.PendingPairings()
	if len(ps) != 1 || ps[0].Code != pairCode {
		t.Fatalf("pending: %+v", ps)
	}
	// Команда владельца от агента без ключа — не спаривание, а отказ с
	// готовой командой для человека.
	if code, out := p.run(t, "approvals", "list"); code != ExitAuth || errOf(jsonOf(t, out))["code"] != "agent_required" ||
		!strings.Contains(errOf(jsonOf(t, out))["hint"].(string), "gate4ai approvals list") {
		t.Fatalf("owner command: %d %s", code, out)
	}

	// Владелец подтвердил (Web UI): агент работает со своим ключом без перезапуска.
	if _, err := p.d.DecidePairing(context.Background(), ps[0].ID, true, "assistant", "webui"); err != nil {
		t.Fatal(err)
	}
	code, out = p.run(t, "mail", "send", "--to", "landlord@example.com", "--subject", "rent")
	if code != ExitOK {
		t.Fatalf("after approval: %d %s", code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(p.keys, "claude-code.json")); strings.Contains(string(b), `"pairing"`) {
		t.Fatalf("pairing left in the key file: %s", b)
	}
	es, _ := p.d.Journal.List(audit.Filter{Agent: "claude-code", Limit: 1, Desc: true})
	if len(es) != 1 || es[0].Receipt.Caller == nil || es[0].Receipt.Caller.Dir != "/home/u/proj" {
		t.Fatalf("journal: %+v", es)
	}

	// Ключ для каталога проекта: pair --here, подтверждение, дальше CLI сам
	// выбирает его в этом проекте.
	code, out = p.run(t, "pair", "--here")
	if code != ExitPending || jsonOf(t, out)["agent"] != "claude-code-proj" {
		t.Fatalf("pair --here: %d %s", code, out)
	}
	ps, _ = p.d.State.PendingPairings()
	if len(ps) != 1 || ps[0].Dir != "/home/u/proj" {
		t.Fatalf("pending --here: %+v", ps)
	}
	if _, err := p.d.DecidePairing(context.Background(), ps[0].ID, true, "drafts-only", "webui"); err != nil {
		t.Fatal(err)
	}
	if code, out := p.run(t, "pair", "--here"); code != ExitOK || !strings.Contains(out, "claude-code-proj") {
		t.Fatalf("pair --here after approval: %d %s", code, out)
	}
	p.as(agentid.Caller{Agent: "claude-code", Dir: "/home/u/proj/sub"})
	code, out = p.run(t, "mail", "send", "--to", "landlord@example.com", "--subject", "x")
	if code != ExitDenied || !strings.Contains(out, "drafts only") {
		t.Fatalf("bound key in the project: %d %s", code, out)
	}
	// Другой проект — общий ключ claude-code.
	p.as(agentid.Caller{Agent: "claude-code", Dir: "/home/u/other"})
	if code, out := p.run(t, "mail", "send", "--to", "landlord@example.com", "--subject", "y"); code != ExitOK {
		t.Fatalf("shared key elsewhere: %d %s", code, out)
	}
	// Чужой ключ проекта из другого каталога (GATE4AI_AGENT указывает на него) —
	// отказ daemon.
	tok := PickKeyFile(t, p.keys, "claude-code-proj")
	if code, out := p.harness.run(t, tok, "mail", "read"); code != ExitAuth || !strings.Contains(out, "works only for an agent started in /home/u/proj") {
		t.Fatalf("bound key from another directory: %d %s", code, out)
	}
}

func TestPairingRejected(t *testing.T) {
	p := startPair(t)
	p.as(agentid.Caller{Agent: "claude-code", Dir: "/x"})
	if code, _ := p.run(t, "tools"); code != ExitPending {
		t.Fatal("pending")
	}
	ps, _ := p.d.State.PendingPairings()
	if _, err := p.d.DecidePairing(context.Background(), ps[0].ID, false, "", "webui"); err != nil {
		t.Fatal(err)
	}
	code, out := p.run(t, "tools")
	if code != ExitAuth || errOf(jsonOf(t, out))["code"] != "pairing_rejected" {
		t.Fatalf("rejected: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(p.keys, "claude-code.json")); !os.IsNotExist(err) {
		t.Fatal("key of a rejected agent kept")
	}
	// Человек в терминале (агента нет) — сокет владельца, без спаривания.
	p.as(agentid.Caller{})
	if code, out := p.run(t, "tools", "--as", "claude-ai"); code != ExitOK {
		t.Fatalf("owner: %d %s", code, out)
	}
	if ps, _ := p.d.State.PendingPairings(); len(ps) != 0 {
		t.Fatal("owner started pairing")
	}
}

// PickKeyFile — ключ агента из каталога ключей.
func PickKeyFile(t *testing.T, dir, agent string) string {
	t.Helper()
	e := &Env{KeyDir: dir}
	k, ok := PickKey(e.loadKeys(), agent, "", "")
	if !ok || k.Pairing != "" {
		t.Fatalf("no key of %s", agent)
	}
	return k.Key
}
