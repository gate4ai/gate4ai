// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

// web — поставщик: open только для коннекторов, act — с классом write.
type web struct {
	mu      sync.Mutex
	callers []*abi.Caller
	fail    error
}

func (w *web) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "web", Contract: 1, Services: []string{"open", "act", "read"}, Actions: []manifest.Action{
		{Name: "open", Class: "read", ServiceOnly: true, InputSchema: manifest.MustSchema(`{ type: object, properties: { url: { type: string } }, required: [url] }`)},
		{Name: "act", Class: "write", ServiceOnly: true, InputSchema: manifest.MustSchema(`{ type: object, properties: { sel: { type: string } } }`)},
		{Name: "read", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: {} }`)},
	}}
}

func (w *web) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callers = append(w.callers, r.Caller)
	if w.fail != nil {
		return abi.ExecResult{}, w.fail
	}
	return abi.ExecResult{Structured: map[string]any{"url": r.Args["url"], "action": r.Action}}, nil
}

func (w *web) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

// bank — потребитель: вызывает действия web через ExecuteNested (как хост на service_call).
type bank struct {
	p     *Pipeline
	calls []abi.ServiceCallRequest
	rule  manifest.ServiceRule
	got   []error
	res   []abi.ExecResult
}

func (b *bank) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "bank", Capabilities: manifest.Capabilities{Services: []manifest.ServiceCap{
		{Target: "web", Contract: 1, Actions: []string{"open", "act", "read", "nope"}},
		{Target: "bank", Contract: 1, Actions: []string{"pay"}},
	}}, Actions: []manifest.Action{
		{Name: "pay", Class: "money", Verify: "feed", InputSchema: manifest.MustSchema(`{ type: object, properties: {} }`),
			Calls: []string{"web.open", "web.act", "web.read", "web.nope", "bank.pay"}},
		{Name: "look", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: {} }`), Calls: []string{"web.open", "web.act"}},
	}}
}

func (b *bank) Execute(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	b.got, b.res = nil, nil
	for _, c := range b.calls {
		res, err := b.p.ExecuteNested(ctx, "bank", b.rule, c)
		b.got = append(b.got, err)
		b.res = append(b.res, res)
	}
	return abi.ExecResult{Structured: map[string]any{"ok": true}}, nil
}

func (b *bank) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified}, nil
}

const nestedPolicy = `
agents:
  bot: { profile: payer }
profiles:
  payer:
    connectors: [bank]
    rules:
      - { id: pay-ok, action: bank.pay, effect: allow }
      - { id: look-ok, action: bank.look, effect: allow }
      - { id: no-evil, action: web.open, when: 'url.contains("evil")', effect: deny }
      - { id: ask-read, action: web.read, effect: ask }
`

func nestedEnv(t *testing.T) (*Pipeline, *bank, *web, *audit.Journal, *[]string) {
	t.Helper()
	dir := t.TempDir()
	ks := keys.NewMemory()
	w := &web{}
	b := &bank{rule: manifest.ServiceRule{Target: "web", Contract: 1, Actions: []string{"open", "act", "read"}, Params: map[string]any{"domains": []any{"bank.example"}}}}
	reg := connectors.NewRegistry()
	for _, c := range []connectors.Connector{w, b} {
		cli, err := connectors.NewInProcess(c)
		if err != nil {
			t.Fatal(err)
		}
		reg.Add(cli)
	}
	cfg, err := config.Parse([]byte(nestedPolicy), policy.Validator(reg.Catalog()))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.New(cfg, reg.Catalog())
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(filepath.Join(dir, "state.db"), ks)
	if err != nil {
		t.Fatal(err)
	}
	j, err := audit.Open(filepath.Join(dir, "journal.db"), filepath.Join(dir, "ev"), ks, audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); j.Close() })
	p := New(reg, st, j, fakeSecrets{}, &fakeNotifier{}, eng)
	var ended []string
	p.EndCall = func(root string) { ended = append(ended, root) }
	b.p = p
	return p, b, w, j, &ended
}

func TestNestedCall(t *testing.T) {
	p, b, w, j, ended := nestedEnv(t)
	b.calls = []abi.ServiceCallRequest{
		{Target: "web", Action: "open", Args: map[string]any{"url": "https://bank.example/"}}, // без правила — покрыт bank.pay
		{Target: "web", Action: "open", Args: map[string]any{"url": "https://evil.example/"}}, // правило deny
		{Target: "web", Action: "read"},                         // правило ask во вложенном не действует — покрыт bank.pay
		{Target: "web", Action: "open", Args: map[string]any{}}, // нет обязательного url
		{Target: "web", Action: "nope"},                         // нет действия
		{Target: "bank", Action: "pay"},                         // цикл и не сервис
	}
	res := p.Execute(context.Background(), Call{Agent: "bot", Action: "bank.pay"})
	if res.Status != StatusOK {
		t.Fatal(res)
	}
	if b.got[0] != nil || b.res[0].Structured.(map[string]any)["url"] != "https://bank.example/" {
		t.Fatal(b.got[0], b.res[0])
	}
	if b.got[2] != nil {
		t.Fatal("ask rule in a nested call:", b.got[2])
	}
	for i, want := range map[int]string{1: "denied by policy", 3: "url", 4: "not installed", 5: "cycle"} {
		if b.got[i] == nil || !strings.Contains(b.got[i].Error(), want) {
			t.Errorf("call %d: want %q, got %v", i, want, b.got[i])
		}
	}
	// Поставщик получил вызывающего, корень и params.
	if len(w.callers) != 2 || w.callers[0] == nil || w.callers[0].Connector != "bank" || w.callers[0].Call != res.ID ||
		w.callers[0].Params["domains"] == nil || w.callers[1].Call != res.ID {
		t.Fatalf("%+v", w.callers)
	}
	// Квитанции: вложенная ссылается на корневую; end_call — один раз, для корня.
	recs, err := j.List(audit.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var open, deny *audit.Receipt
	for i := range recs {
		r := &recs[i].Receipt
		if r.Action == "web.open" && r.Decision.Effect == config.EffectAllow {
			open = r
		}
		if r.Action == "web.open" && r.Decision.Effect == config.EffectDeny {
			deny = r
		}
	}
	if open == nil || open.Parent != res.ID || open.Agent != "bot" || deny == nil {
		t.Fatalf("receipts: %+v", recs)
	}
	if len(*ended) != 1 || (*ended)[0] != res.ID {
		t.Fatal("end_call:", *ended)
	}
	// Агент действие только для коннекторов не видит, вне действия вызов невозможен.
	if r := p.Execute(context.Background(), Call{Agent: "bot", Action: "web.open", Args: map[string]any{"url": "x"}}); r.Code != CodeUnknownAction {
		t.Fatal(r)
	}
	if _, err := p.ExecuteNested(context.Background(), "bank", b.rule, abi.ServiceCallRequest{Target: "web", Action: "open"}); err == nil {
		t.Fatal("nested call outside an action")
	}
}

func TestNestedContractAndErrors(t *testing.T) {
	p, b, w, _, _ := nestedEnv(t)
	b.rule.Contract = 2
	b.calls = []abi.ServiceCallRequest{{Target: "web", Action: "open", Args: map[string]any{"url": "https://bank.example/"}}}
	p.Execute(context.Background(), Call{Agent: "bot", Action: "bank.pay"})
	if b.got[0] == nil || !strings.Contains(b.got[0].Error(), "contract 1") {
		t.Fatal("contract mismatch:", b.got[0])
	}
	b.rule.Contract = 1
	w.fail = abi.Errorf(abi.CodeNotFound, "element not found")
	p.Execute(context.Background(), Call{Agent: "bot", Action: "bank.pay"})
	var ae *abi.Error
	if b.got[0] == nil || !asABI(b.got[0], &ae) || ae.Code != abi.CodeNotFound {
		t.Fatal("provider error code must reach the caller:", b.got[0])
	}
}

func asABI(err error, out **abi.Error) bool {
	e, ok := err.(*abi.Error)
	*out = e
	return ok
}

func TestNestedDepth(t *testing.T) {
	p, b, _, _, _ := nestedEnv(t)
	ctx := withCall(context.Background(), callInfo{Agent: "bot", ID: "r1", Root: "r1", Depth: MaxDepth - 1, Chain: []string{"a", "b", "c", "bank"}})
	if _, err := p.ExecuteNested(ctx, "bank", b.rule, abi.ServiceCallRequest{Target: "web", Action: "open", Args: map[string]any{"url": "u"}}); err == nil ||
		!strings.Contains(err.Error(), "nesting") {
		t.Fatal("depth:", err)
	}
}

// Из действия — только объявленные в calls подвызовы и не выше его класса.
func TestNestedCallsDeclaredByAction(t *testing.T) {
	p, b, _, _, _ := nestedEnv(t)
	b.calls = []abi.ServiceCallRequest{
		{Target: "web", Action: "open", Args: map[string]any{"url": "https://bank.example/"}}, // объявлен у look, read
		{Target: "web", Action: "act"},  // объявлен, но write выше read
		{Target: "web", Action: "read"}, // не объявлен у look
	}
	res := p.Execute(context.Background(), Call{Agent: "bot", Action: "bank.look"})
	if res.Status != StatusOK {
		t.Fatal(res)
	}
	if b.got[0] != nil {
		t.Fatal("declared read call:", b.got[0])
	}
	if b.got[1] == nil || !strings.Contains(b.got[1].Error(), "above the class read") {
		t.Fatal("class escalation:", b.got[1])
	}
	if b.got[2] == nil || !strings.Contains(b.got[2].Error(), "does not declare") {
		t.Fatal("undeclared call:", b.got[2])
	}
}

// Запрос подтверждения называет подвызовы: подтверждение их покрывает.
func TestApprovalShowsCalls(t *testing.T) {
	p, b, _, _, _ := nestedEnv(t)
	m := b.Manifest()
	act, _ := m.Find("look")
	act.CallReasons = map[string]string{"web.act": "раскрыть список"}
	a, err := p.requestApproval("bot", "bank.look", act, map[string]any{}, nil, policy.Decision{Effect: config.EffectAsk})
	if err != nil || !strings.Contains(a.Preview, "also runs: web.open, web.act (раскрыть список)") {
		t.Fatal(a, err)
	}
}
