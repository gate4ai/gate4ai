// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

const fakePassword = "fake-pass-9876"

// fakeBrowser — нативный browser: запоминает запросы хоста (учётки, форма, значения).
type fakeBrowser struct {
	runtime string
	mu      sync.Mutex
	reqs    []abi.ExecRequest
}

func (b *fakeBrowser) Manifest() manifest.Manifest {
	obj := manifest.MustSchema(`{ type: object, properties: { session: { type: string }, account: { type: string }, field: { type: string }, selector: { type: string }, source: { type: string }, prompt: { type: string } } }`)
	m := manifest.Manifest{Connector: "browser", Runtime: b.runtime, Contract: 1, Services: []string{"open", "observe", "login", "fill_secret"}, Actions: []manifest.Action{
		{Name: "open", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { url: { type: string }, session: { type: string } } }`)},
		{Name: "observe", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { session: { type: string } } }`)},
		{Name: "login", Class: "write", InputSchema: obj,
			OutputSchema: ptr(manifest.MustSchema(`{ type: object, properties: { url: { type: string }, status: { type: string }, origin: { type: string } }, required: [status] }`))},
		{Name: "fill_secret", Class: "write", InputSchema: obj},
		{Name: "handoff", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { session: { type: string }, message: { type: string } } }`)},
	}}
	if b.runtime == manifest.RuntimeNative {
		m.Platforms = []string{"linux/amd64"}
	}
	return m
}

func ptr[T any](v T) *T { return &v }

func (b *fakeBrowser) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	b.mu.Lock()
	b.reqs = append(b.reqs, r)
	b.mu.Unlock()
	switch r.Action {
	case "login":
		if r.Login == nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeSecretDenied, "no account")
		}
		if r.Login.Alias == "@broken" {
			return abi.ExecResult{}, abi.Errorf("form_mismatch", "#password: not found")
		}
		return abi.ExecResult{Structured: map[string]any{"url": "https://lk.example/", "status": "logged_in", "origin": "https://login.example"}, ResultRef: "logged_in"}, nil
	case "handoff":
		if r.Args["session"] == "s-closed" {
			return abi.ExecResult{}, abi.Errorf(abi.CodeNotFound, "browser session s-closed is closed or unknown")
		}
		return abi.ExecResult{Structured: map[string]any{"url": "https://lk.example/", "status": "done"}}, nil
	case "fill_secret":
		if r.Args["selector"] == "#elsewhere" {
			return abi.ExecResult{}, abi.Errorf("wrong_origin", "the page is not at the sign-in site")
		}
		return abi.ExecResult{Structured: map[string]any{"url": "https://login.example/", "origin": "https://login.example"}}, nil
	}
	return abi.ExecResult{Structured: map[string]any{"url": "https://login.example/"}}, nil
}

func (b *fakeBrowser) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func (b *fakeBrowser) last() abi.ExecRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reqs[len(b.reqs)-1]
}

// site — коннектор, который входит через browser (service_call).
type site struct {
	name  string
	p     *Pipeline
	calls []abi.ServiceCallRequest
	got   []error
	res   []abi.ExecResult
	creds []manifest.CredentialSlot
	out   map[string]any
}

func (s *site) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: s.name, Credentials: s.creds,
		Capabilities: manifest.Capabilities{Services: []manifest.ServiceCap{{Target: "browser", Contract: 1, Actions: []string{"open", "observe", "login", "fill_secret"}}}},
		Actions: []manifest.Action{{Name: "login", Class: "write", Calls: []string{"browser.open", "browser.observe", "browser.login", "browser.fill_secret"},
			InputSchema: manifest.MustSchema(`{ type: object, properties: { account: { type: string } } }`)}}}
}

func (s *site) Execute(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	s.got, s.res = nil, nil
	for _, c := range s.calls {
		res, err := s.p.ExecuteNested(ctx, s.name, manifest.ServiceRule{Target: "browser", Contract: 1, Actions: []string{"open", "observe", "login", "fill_secret"}}, c)
		s.got = append(s.got, err)
		s.res = append(s.res, res)
	}
	out := s.out
	if out == nil {
		out = map[string]any{"status": "logged_in", "account": r.Args["account"]}
	}
	return abi.ExecResult{Structured: out}, nil
}

func (s *site) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

type fakeVault map[string]string

func (v fakeVault) Get(alias string) (string, error) {
	if x, ok := v[alias]; ok {
		return x, nil
	}
	return "", fmt.Errorf("secret not set: %s", alias)
}

const credPolicy = `
agents:
  dev: { profile: connector-dev }
  other: { profile: other }
profiles:
  connector-dev:
    connectors: [browser, lk_emias, slotted, stranger, bank_login]
    rules:
      - { id: login-ok, action: "browser.*", effect: allow }
      - { id: login-named, action: browser.login, effect: allow }
      - { id: fill-named, action: browser.fill_secret, effect: allow }
      - { id: lk-ok, action: "lk_emias.*", effect: allow }
      - { id: slotted-ok, action: "slotted.*", effect: allow }
      - { id: stranger-ok, action: "stranger.*", effect: allow }
  other:
    connectors: [browser]
    rules:
      - { id: login-ok, action: "browser.*", effect: allow }
      - { id: login-named, action: browser.login, effect: allow }
      - { id: fill-named, action: browser.fill_secret, effect: allow }
connectors:
  slotted: { credentials: { login: "@emias" } }
credentials:
  "@emias":
    bind: ["https://login.example"]
    form:
      fields:
        - { selector: "#login", value: "+70000000000" }
        - { selector: "#password", secret: "@emias.password" }
      submit: { selector: "#bind" }
      success: { url_prefix: "https://lk.example/" }
    use: [connector-dev]
    connectors: [lk_emias]
    prompts: { sms: "Код из SMS" }
  "@broken":
    bind: ["https://login.example"]
    form:
      fields: [{ selector: "#password", secret: "@emias.password" }]
      submit: { press: Enter }
    use: [connector-dev]
  "@bank":
    bind: ["https://id.bank.example"]
    via:
      connector: bank_login
      action: login
      fields: [{ name: password, secret: "@emias.password" }]
    use: [connector-dev]
`

type credEnv struct {
	p                     *Pipeline
	br                    *fakeBrowser
	lk, slotted, stranger *site
	via                   *site
	j                     *audit.Journal
	asked                 []Question
}

func newCredEnv(t *testing.T, runtime string) *credEnv {
	t.Helper()
	dir := t.TempDir()
	ks := keys.NewMemory()
	e := &credEnv{br: &fakeBrowser{runtime: runtime}, lk: &site{name: "lk_emias"}, stranger: &site{name: "stranger"},
		slotted: &site{name: "slotted", creds: []manifest.CredentialSlot{{Slot: "login", Bind: []string{"https://login.example"}}}},
		via:     &site{name: "bank_login"}}
	reg := connectors.NewRegistry()
	for _, c := range []connectors.Connector{e.br, e.lk, e.slotted, e.stranger, e.via} {
		cli, err := connectors.NewInProcess(c)
		if err != nil {
			t.Fatal(err)
		}
		reg.Add(cli)
	}
	cfg, err := config.Parse([]byte(credPolicy), policy.Validator(reg.Catalog()))
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
	e.j, err = audit.Open(filepath.Join(dir, "journal.db"), filepath.Join(dir, "ev"), ks, audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); e.j.Close() })
	e.p = New(reg, st, e.j, fakeSecrets{}, &fakeNotifier{}, eng)
	e.p.Vault = fakeVault{"@emias.password": fakePassword}
	e.p.Ask = func(_ context.Context, q Question) (string, error) {
		e.asked = append(e.asked, q)
		return "135790", nil
	}
	for _, s := range []*site{e.lk, e.slotted, e.stranger, e.via} {
		s.p = e.p
	}
	return e
}

// journalHas — весь журнал без значения секрета; события secret.use.
func (e *credEnv) journal(t *testing.T) (all string, uses []audit.Receipt) {
	t.Helper()
	recs, err := e.j.List(audit.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(recs)
	for _, r := range recs {
		if r.Receipt.Action == "secret.use" {
			uses = append(uses, r.Receipt)
		}
	}
	return string(b), uses
}

func TestAgentLoginGetsValuesOnlyForLogin(t *testing.T) {
	e := newCredEnv(t, manifest.RuntimeNative)
	ctx := context.Background()
	// Другие действия: учётки агента (без форм и значений).
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.open", Args: map[string]any{"url": "https://lk.example/"}}); r.Status != StatusOK {
		t.Fatal(r)
	}
	q := e.br.last()
	if q.Login != nil || q.Revealed != nil || len(q.Accounts) != 3 || q.Accounts[0].Alias != "@bank" || q.Accounts[0].Via != "bank_login.login" ||
		q.Accounts[2].Bind[0] != "https://login.example" || q.Accounts[2].Fields != nil {
		t.Fatalf("%+v", q)
	}
	r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"session": "s-1", "account": "emias"}})
	if r.Status != StatusOK || r.Result.(map[string]any)["status"] != "logged_in" {
		t.Fatal(r)
	}
	q = e.br.last()
	if q.Login == nil || q.Login.Alias != "@emias" || len(q.Login.Fields) != 2 || q.Login.Fields[1].Name != "@emias.password" ||
		q.Login.Submit.Selector != "#bind" || q.Login.Success != "https://lk.example/" || q.Revealed["@emias.password"] != fakePassword ||
		q.Args["account"] != "@emias" || len(q.Revealed) != 1 {
		t.Fatalf("%+v", q)
	}
	all, uses := e.journal(t)
	if strings.Contains(all, fakePassword) {
		t.Fatal("secret value in the journal")
	}
	if len(uses) != 1 || uses[0].Parent != r.ID || uses[0].Args["account"] != "@emias" || uses[0].Args["field"] != "@emias.password" ||
		uses[0].Args["origin"] != "https://login.example" || uses[0].Agent != "dev" {
		t.Fatalf("secret.use: %+v", uses)
	}
	// Ошибка до ввода (form_mismatch) — без secret.use.
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"account": "@broken"}}); r.Status != StatusError || !strings.Contains(r.Message, "form_mismatch") {
		t.Fatal(r)
	}
	if _, uses := e.journal(t); len(uses) != 1 {
		t.Fatal("secret.use after a pre-input error:", uses)
	}
	// fill_secret: только значение запрошенного поля.
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.fill_secret", Args: map[string]any{"account": "@emias", "field": "password"}}); r.Status != StatusOK {
		t.Fatal(r)
	}
	if q := e.br.last(); q.Revealed["@emias.password"] != fakePassword || len(q.Revealed) != 1 {
		t.Fatalf("%+v", q.Revealed)
	}
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.fill_secret", Args: map[string]any{"account": "@emias", "field": "login"}}); r.Status != StatusOK || e.br.last().Revealed != nil {
		t.Fatal("constant field: no values", r, e.br.last().Revealed)
	}
}

func TestLoginAccess(t *testing.T) {
	e := newCredEnv(t, manifest.RuntimeNative)
	ctx := context.Background()
	n := len(e.br.reqs)
	// Профиль агента не в use — secret_denied, browser не вызывается, отказ в журнале.
	r := e.p.Execute(ctx, Call{Agent: "other", Action: "browser.login", Args: map[string]any{"account": "@emias"}})
	if r.Status != StatusError || r.Code != CodeSecretDenied || len(e.br.reqs) != n {
		t.Fatal(r)
	}
	if r := e.p.Execute(ctx, Call{Agent: "other", Action: "browser.open", Args: map[string]any{"url": "x"}}); r.Status != StatusOK || len(e.br.last().Accounts) != 0 {
		t.Fatal("no accounts for the profile:", e.br.last().Accounts)
	}
	all, _ := e.journal(t)
	if !strings.Contains(all, "secret_denied") {
		t.Fatal("denial is not in the journal")
	}
	// Коннекторы: из connectors учётки, по слоту — да; чужой — нет.
	login := []abi.ServiceCallRequest{{Target: "browser", Action: "login", Args: map[string]any{"account": "@emias"}}}
	e.lk.calls, e.stranger.calls = login, login
	e.slotted.calls = []abi.ServiceCallRequest{{Target: "browser", Action: "login", Args: map[string]any{"account": "login"}}}
	for _, s := range []*site{e.lk, e.slotted} {
		if r := e.p.Execute(ctx, Call{Agent: "dev", Action: s.name + ".login"}); r.Status != StatusOK || s.got[0] != nil {
			t.Fatal(s.name, r, s.got)
		}
		q := e.br.last()
		if q.Caller == nil || q.Caller.Connector != s.name || q.Revealed["@emias.password"] != fakePassword || len(q.Accounts) != 1 {
			t.Fatalf("%s: %+v", s.name, q)
		}
	}
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "stranger.login"}); r.Status != StatusOK || e.stranger.got[0] == nil ||
		!strings.Contains(e.stranger.got[0].Error(), abi.CodeSecretDenied) {
		t.Fatal(r, e.stranger.got)
	}
	// Слот ждёт коннектор входа (via), а у учётки форма — доступа нет.
	e.slotted.creds[0].Types = []string{manifest.SlotVia}
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "slotted.login"}); r.Status != StatusOK || e.slotted.got[0] == nil ||
		!strings.Contains(e.slotted.got[0].Error(), abi.CodeSecretDenied) {
		t.Fatal("slot type does not match the account:", e.slotted.got)
	}
	e.slotted.creds[0].Types = nil
	// Слот, привязанный шире манифеста, доступа не даёт.
	e.slotted.creds[0].Bind = []string{"https://other.example"}
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "slotted.login"}); r.Status != StatusOK || e.slotted.got[0] == nil {
		t.Fatal("slot bind does not cover the account:", e.slotted.got)
	}
	_, uses := e.journal(t)
	if len(uses) != 2 || uses[0].Args["connector"] == nil {
		t.Fatalf("secret.use: %+v", uses)
	}
}

func TestLoginViaConnector(t *testing.T) {
	e := newCredEnv(t, manifest.RuntimeNative)
	ctx := context.Background()
	e.via.calls = []abi.ServiceCallRequest{{Target: "browser", Action: "fill_secret", Args: map[string]any{"account": "@bank", "field": "sms", "selector": "#sms", "source": "human"}}}
	e.via.out = map[string]any{"status": "submitted", "extra": "dropped"}
	r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"session": "s-7", "account": "@bank"}})
	if r.Status != StatusOK || r.Result.(map[string]any)["status"] != "submitted" || r.Result.(map[string]any)["extra"] != nil {
		t.Fatal(r)
	}
	if e.via.got[0] != nil {
		t.Fatal(e.via.got)
	}
	// Порядок у браузера: проверка без значения → (вопрос человеку) → ввод → шаг login в записи сеанса.
	n := len(e.br.reqs)
	pre, q, rec := e.br.reqs[n-3], e.br.reqs[n-2], e.br.reqs[n-1]
	if pre.Action != "fill_secret" || pre.Args[ArgPrecheck] != true || pre.Revealed[abi.HumanValue] != "" {
		t.Fatalf("precheck: %+v", pre)
	}
	if q.Action != "fill_secret" || q.Caller.Connector != "bank_login" || q.Caller.Session != "s-7" || q.Revealed[abi.HumanValue] != "135790" {
		t.Fatalf("%+v %+v", q, q.Caller)
	}
	if rec.Action != "login" || rec.Args[ArgRecordOnly] != true || rec.Args["result"].(map[string]any)["status"] != "submitted" {
		t.Fatalf("record_only: %+v", rec)
	}
	if len(e.asked) != 1 || e.asked[0].Caller != "bank_login" || e.asked[0].Account != "@bank" || e.asked[0].Text != "a one-time code" {
		t.Fatalf("%+v", e.asked)
	}
	// Агент получил одну «тулзу входа»: в журнале вызов коннектора входа — подвызов browser.login.
	recs, _ := e.j.List(audit.Filter{Limit: 50})
	var viaRec *audit.Receipt
	for i := range recs {
		if recs[i].Receipt.Action == "bank_login.login" {
			viaRec = &recs[i].Receipt
		}
	}
	if viaRec == nil || viaRec.Parent != r.ID {
		t.Fatalf("%+v", recs)
	}
	// Коннектор входа не может сам вызвать browser.login той же учётки.
	e.via.calls = []abi.ServiceCallRequest{{Target: "browser", Action: "login", Args: map[string]any{"account": "@bank"}}}
	e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"account": "@bank"}})
	if e.via.got[0] == nil || !strings.Contains(e.via.got[0].Error(), "fill_secret") {
		t.Fatal(e.via.got)
	}
	// Вопрос владельца из учётки важнее вопроса коннектора.
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.fill_secret", Args: map[string]any{"account": "@emias", "field": "sms", "source": "human", "prompt": "code?", "selector": "#c"}}); r.Status != StatusOK {
		t.Fatal(r)
	}
	if e.asked[len(e.asked)-1].Text != "Код из SMS" {
		t.Fatal(e.asked)
	}
}

func TestAccountsOnlyForNativeBrowser(t *testing.T) {
	e := newCredEnv(t, manifest.RuntimeWasm)
	r := e.p.Execute(context.Background(), Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"account": "@emias"}})
	q := e.br.last()
	if q.Login != nil || q.Revealed != nil || q.Accounts != nil || r.Status == StatusOK {
		t.Fatalf("a WASM connector named browser got the account: %+v %+v", q, r)
	}
}

// Сеансы браузера — у экземпляра агента: сессия Claude Code, иначе процесс-корень
// агента; удалённый вызов и вызов не из агента экземпляра не знают.
func TestBrowserAgentInstance(t *testing.T) {
	chain := []audit.CallerProc{{PID: 10, Name: "gate4ai"}, {PID: 7, Name: "codex"}}
	for _, tc := range []struct {
		c    *audit.Caller
		want string
	}{
		{&audit.Caller{Kind: "claude-code", Source: "peer", Session: "f170", Chain: chain}, "session:f170"},
		{&audit.Caller{Kind: "codex", Source: "peer", Chain: chain}, "pid:7"},
		{&audit.Caller{Source: "peer", Chain: chain[:1]}, ""},
		{&audit.Caller{Kind: "claude-code", Source: "reported", Session: "f170"}, ""},
		{nil, ""},
	} {
		e := newCredEnv(t, manifest.RuntimeNative)
		e.p.Execute(WithCaller(context.Background(), tc.c), Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"account": "@emias"}})
		if q := e.br.last(); q.Agent != "dev" || q.AgentInstance != tc.want {
			t.Fatalf("%+v: agent %q instance %q, want %q", tc.c, q.Agent, q.AgentInstance, tc.want)
		}
	}
}

func TestApprovalShowsAccount(t *testing.T) {
	e := newCredEnv(t, manifest.RuntimeNative)
	cfg := e.p.cfg()
	bm := e.br.Manifest()
	act, _ := bm.Find("login")
	if s := e.p.accountPreview("dev", "browser.login", act, map[string]any{"account": "@emias"}); s != "signs in on https://login.example with account @emias" {
		t.Fatal(s)
	}
	sm := e.slotted.Manifest()
	sl, _ := sm.Find("login")
	if s := e.p.accountPreview("dev", "slotted.login", sl, nil); !strings.Contains(s, "account @emias (slot login)") {
		t.Fatal(s, cfg.Connectors)
	}
}

// Учётка с коннектором входа: он вводит её поля по имени; браузер проверяет
// страницу до вопроса человеку — при отказе человека не спрашивают.
func TestViaFieldsAndPrecheck(t *testing.T) {
	e := newCredEnv(t, manifest.RuntimeNative)
	ctx := context.Background()
	e.via.calls = []abi.ServiceCallRequest{{Target: "browser", Action: "fill_secret", Args: map[string]any{"account": "@bank", "field": "password", "selector": "#pw"}}}
	e.via.out = map[string]any{"status": "logged_in"}
	if r := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"account": "@bank"}}); r.Status != StatusOK || e.via.got[0] != nil {
		t.Fatal(r, e.via.got)
	}
	var fill abi.ExecRequest
	for _, q := range e.br.reqs {
		if q.Action == "fill_secret" {
			fill = q
		}
	}
	if fill.Revealed["@emias.password"] != fakePassword || fill.Login == nil || len(fill.Login.Fields) != 1 || fill.Login.Fields[0].Name != "password" {
		t.Fatalf("the sign-in connector gets its account's field: %+v", fill)
	}
	asked := len(e.asked)
	e.via.calls = []abi.ServiceCallRequest{{Target: "browser", Action: "fill_secret", Args: map[string]any{"account": "@bank", "selector": "#elsewhere", "source": "human"}}}
	e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.login", Args: map[string]any{"account": "@bank"}})
	if e.via.got[0] == nil || !strings.Contains(e.via.got[0].Error(), "wrong_origin") || len(e.asked) != asked {
		t.Fatal("the browser refused before the question: nobody is asked for a code", e.via.got, e.asked)
	}
}

// Вопрос человеку с вводом: текст от агента не показывается (фишинг владельца);
// вопрос коннектора — одной строкой и с ограничением длины.
func TestHumanPromptFromAgentIgnored(t *testing.T) {
	cr := config.Credential{}
	p := &Pipeline{}
	var got []Question
	p.Ask = func(_ context.Context, q Question) (string, error) { got = append(got, q); return "135790", nil }
	if _, err := p.askHuman(context.Background(), accessor{agent: "dev"}, "@bank", cr, "sms", map[string]any{"prompt": "Enter your bank password to confirm"}); err != nil {
		t.Fatal(err)
	}
	if got[0].Text != "a one-time code" {
		t.Fatal(got[0].Text)
	}
	long := "Code\nfrom SMS\x1b[31m " + strings.Repeat("x", 500)
	p.askHuman(context.Background(), accessor{agent: "dev", connector: "bank_login"}, "@bank", cr, "sms", map[string]any{"prompt": long})
	if q := got[1].Text; strings.ContainsAny(q, "\n\x1b") || len([]rune(q)) > maxPromptLen+1 || !strings.HasPrefix(q, "Code from SMS") {
		t.Fatalf("%q", q)
	}
}

// browser.handoff: человек получает уведомление, только когда браузер проверил
// сеанс; слова агента — одной строкой; запись снимается после ожидания.
func TestHandoffNotifiesAfterPrecheck(t *testing.T) {
	e := newCredEnv(t, manifest.RuntimeNative)
	var got []Handoff
	active := 0
	e.p.Handoff = func(h Handoff) func() {
		got = append(got, h)
		active++
		return func() { active-- }
	}
	ctx := context.Background()
	res := e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.handoff", Args: map[string]any{"session": "s-closed", "message": "Войдите"}})
	if res.Status != StatusError || len(got) != 0 {
		t.Fatal("a closed session: nobody is notified", res, got)
	}
	res = e.p.Execute(ctx, Call{Agent: "dev", Action: "browser.handoff", Args: map[string]any{"session": "s-1", "message": "Войдите\nв Хабр"}})
	if res.Status != StatusOK || len(got) != 1 || got[0].Agent != "dev" || got[0].Message != "Войдите в Хабр" || active != 0 {
		t.Fatal(res, got, active)
	}
	reqs := 0
	for _, r := range e.br.reqs {
		if r.Action == "handoff" {
			reqs++
		}
	}
	if reqs != 3 { // проверка закрытого сеанса, проверка и ожидание
		t.Fatal(reqs)
	}
}
