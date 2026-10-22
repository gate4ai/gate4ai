// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/state"
)

const limitPolicy = `
agents:
  claude-ai: { profile: assistant }
lists:
  contacts_trusted: ["landlord@example.com"]
profiles:
  assistant:
    connectors: [email]
    rules:
      - id: email-send-trusted
        action: email.send
        when: to.all(r, r in lists.contacts_trusted)
        effect: allow
      - { id: bad-cond, action: email.read, when: "args.nope == 1", effect: deny }
    limits:
      - { id: send-cap, action: email.send, per_day: 2 }
connectors:
  email: { host: imap.example.com }
approvals: { timeout: 1h }
`

var (
	trusted  = map[string]any{"to": "landlord@example.com", "subject": "rent"}
	stranger = map[string]any{"to": "stranger@example.org", "subject": "hi"}
)

func (e *env) setPolicy(t *testing.T, pol string) {
	t.Helper()
	cat := e.p.Registry.Catalog()
	cfg, err := config.Parse([]byte(pol), policy.Validator(cat))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.New(cfg, cat)
	if err != nil {
		t.Fatal(err)
	}
	e.p.SetEngine(eng)
}

func (e *env) sent() int {
	e.mail.mu.Lock()
	defer e.mail.mu.Unlock()
	return len(e.mail.sent)
}

// Лимит исчерпан между запросом и «Разрешить» — отказ, а не исполнение по
// старому решению.
func TestApproveRechecksLimits(t *testing.T) {
	e := newEnvPolicy(t, limitPolicy)
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	if r.Status != StatusPending {
		t.Fatal(r)
	}
	for i := 0; i < 2; i++ {
		if r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: trusted}); r.Status != StatusOK {
			t.Fatal(r)
		}
	}
	res, err := e.p.Decide(ctx, r.ID, true, "telegram", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusDenied || res.Code != CodeDenied || res.RuleIDs[0] != "send-cap" || !strings.Contains(res.Message, "changed after the request") {
		t.Fatalf("%+v", res)
	}
	if e.sent() != 2 {
		t.Fatal("must not send over the limit")
	}
	ent, _ := e.j.Find(res.Receipt)
	if ent.Receipt.Approval == nil || ent.Receipt.Approval.Result != state.Approved || ent.Receipt.Decision.Effect != config.EffectDeny {
		t.Fatalf("%+v", ent.Receipt)
	}
}

// Правило deny, добавленное после запроса, и смена профиля действуют на
// уже поставленный запрос.
func TestApproveRechecksPolicyAndProfile(t *testing.T) {
	e := newEnv(t)
	r1 := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	r2 := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	e.setPolicy(t, strings.Replace(testPolicy, "  drafts-only:", `      - { id: no-strangers, action: email.send, when: "!to.all(r, r in lists.contacts_trusted)", effect: deny }
  drafts-only:`, 1))
	res, _ := e.p.Decide(ctx, r1.ID, true, "webui", "owner")
	if res.Status != StatusDenied || res.RuleIDs[0] != "no-strangers" {
		t.Fatalf("%+v", res)
	}
	// Профиль агента больше не содержит email.
	e.setPolicy(t, `
agents:
  claude-ai: { profile: empty }
profiles:
  empty:
    connectors: []
`)
	res, _ = e.p.Decide(ctx, r2.ID, true, "webui", "owner")
	if res.Status != StatusDenied || res.RuleIDs[0] != "connector-not-in-profile" {
		t.Fatalf("%+v", res)
	}
	if e.sent() != 0 {
		t.Fatal("sent")
	}
}

// Отзыв агента останавливает уже поставленные запросы.
func TestApproveAfterRevoke(t *testing.T) {
	e := newEnv(t)
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	*e.now = e.now.Add(time.Second)
	if _, err := e.st.RevokeAgent("claude-ai"); err != nil {
		t.Fatal(err)
	}
	res, _ := e.p.Decide(ctx, r.ID, true, "webui", "owner")
	if res.Status != StatusDenied || res.RuleIDs[0] != "agent-revoked" || e.sent() != 0 {
		t.Fatalf("%+v", res)
	}
	// Запрос, созданный после отзыва (агента подключили снова), исполняется.
	*e.now = e.now.Add(time.Second)
	r = e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	if res, _ := e.p.Decide(ctx, r.ID, true, "webui", "owner"); res.Status != StatusOK {
		t.Fatalf("%+v", res)
	}
}

// Параллельные вызовы не проходят дневной лимит по одному счётчику.
func TestParallelCallsRespectLimit(t *testing.T) {
	e := newEnvPolicy(t, limitPolicy)
	e.mail.slow = 30 * time.Millisecond
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: trusted}); r.Status == StatusOK {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 2 || e.sent() != 2 {
		t.Fatalf("ok %d sent %d, limit 2", ok, e.sent())
	}
	if n := e.st.Counters().CountToday("claude-ai", "email.send"); n != 2 {
		t.Fatal("counter", n)
	}
}

// Квитанция-намерение пишется до исполнения; итог ссылается на неё.
func TestIntentBeforeExecution(t *testing.T) {
	e := newEnv(t)
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: trusted})
	if r.Status != StatusOK {
		t.Fatal(r)
	}
	es, _ := e.j.List(audit.Filter{All: true})
	if len(es) != 2 || es[0].Receipt.Execution.Status != audit.ExecStarted || es[1].Receipt.Intent != es[0].Receipt.ID || es[1].Receipt.ID != r.ID {
		t.Fatalf("%+v", es)
	}
	// Чтение — без намерения.
	e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.read", Args: map[string]any{}})
	if es, _ := e.j.List(audit.Filter{All: true}); len(es) != 3 {
		t.Fatal(len(es))
	}
}

// Журнал недоступен — действие не исполняется, резерв лимита снят, данные
// чтения не отдаются (fail-closed).
func TestJournalFailureFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.j.Close()
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: trusted})
	if r.Status != StatusError || r.Code != CodeExecution || !strings.Contains(r.Message, "NOT run") || e.sent() != 0 {
		t.Fatalf("%+v", r)
	}
	if n := e.st.Counters().CountToday("claude-ai", "email.send"); n != 0 {
		t.Fatal("reservation not released", n)
	}
	r = e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.read", Args: map[string]any{}})
	if r.Status != StatusError || r.Result != nil || r.Content != nil {
		t.Fatalf("read result released without a receipt: %+v", r)
	}
}

// Восстановление после остановки во время исполнения.
func TestRecoverInterrupted(t *testing.T) {
	e := newEnv(t)
	// Намерение без итога (daemon упал во время действия).
	in := &audit.Receipt{Agent: "claude-ai", Action: "email.send", Class: "external", Args: map[string]any{},
		Decision: audit.Decision{Effect: "allow"}, Execution: audit.Execution{Status: audit.ExecStarted}}
	if _, err := e.j.Append(in); err != nil {
		t.Fatal(err)
	}
	// Одобренный запрос без итога (упал между решением и исполнением).
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	if _, err := e.st.Decide(r.ID, state.Approved, "webui", "owner"); err != nil {
		t.Fatal(err)
	}
	n, err := e.p.Recover()
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if open, _ := e.j.OpenIntents(); len(open) != 0 {
		t.Fatal("intent left open")
	}
	es, _ := e.j.List(audit.Filter{})
	last := es[len(es)-1].Receipt
	if last.Execution.Status != audit.ExecInterrupted || last.Approval == nil {
		t.Fatalf("%+v", last)
	}
	st, done := e.p.Status(r.ID)
	if !done || st.Status != StatusError || !strings.Contains(st.Message, "NOT run") {
		t.Fatalf("%+v", st)
	}
	if n, _ := e.p.Recover(); n != 0 {
		t.Fatal("recover must be idempotent", n)
	}
}

// Ошибка State Store — отказ, а не «лимиты и метки нулевые».
func TestStateErrorFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.st.Close()
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: trusted})
	if r.Status != StatusDenied || r.RuleIDs[0] != policy.RuleStateUnavailable || e.sent() != 0 {
		t.Fatalf("%+v", r)
	}
}

// Просроченный запрос не одобряется: ErrExpired, запрос — timeout.
func TestDecideExpired(t *testing.T) {
	e := newEnv(t)
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	*e.now = e.now.Add(2 * time.Hour)
	if _, err := e.p.Decide(ctx, r.ID, true, "telegram", "owner"); !errors.Is(err, state.ErrExpired) {
		t.Fatal(err)
	}
	e.p.Wait()
	st, done := e.p.Status(r.ID)
	if !done || st.Code != CodeTimeout || e.sent() != 0 {
		t.Fatalf("%+v", st)
	}
}

// AlwaysAsk не ослабляет deny и превращает allow в ask.
func TestAlwaysAsk(t *testing.T) {
	e := newEnv(t)
	e.p.AlwaysAsk = map[string]func(map[string]any) string{"email": func(map[string]any) string { return "owner must confirm" }}
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: trusted})
	if r.Status != StatusPending || r.RuleIDs[0] != "always-ask" {
		t.Fatalf("allow must become ask: %+v", r)
	}
	r = e.p.Execute(ctx, Call{Agent: "drafts", Action: "email.send", Args: trusted})
	if r.Status != StatusDenied || r.RuleIDs[0] != "no-send" {
		t.Fatalf("deny must stay deny: %+v", r)
	}
}

// DecideAsync: решение записано сразу, действие — в фоне.
func TestDecideAsync(t *testing.T) {
	e := newEnv(t)
	e.mail.slow = 50 * time.Millisecond
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.send", Args: stranger})
	start := time.Now()
	if err := e.p.DecideAsync(ctx, r.ID, true, "telegram", "owner"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 40*time.Millisecond {
		t.Fatal("DecideAsync waited for the action")
	}
	if err := e.p.DecideAsync(ctx, r.ID, true, "telegram", "owner"); !errors.Is(err, state.ErrAlreadyDecided) {
		t.Fatal(err)
	}
	if st := e.p.WaitFor(ctx, r.ID, 3*time.Second); st.Status != StatusOK {
		t.Fatalf("%+v", st)
	}
}

// Ошибка условия правила видна в квитанции.
func TestDecisionErrorsInReceipt(t *testing.T) {
	e := newEnvPolicy(t, limitPolicy)
	r := e.p.Execute(ctx, Call{Agent: "claude-ai", Action: "email.read", Args: map[string]any{}})
	if r.Status != StatusDenied {
		t.Fatalf("%+v", r)
	}
	ent, _ := e.j.Find(r.Receipt)
	if len(ent.Receipt.Decision.Errors) != 1 || !strings.Contains(ent.Receipt.Decision.Errors[0], "bad-cond") {
		t.Fatalf("%+v", ent.Receipt.Decision)
	}
}
