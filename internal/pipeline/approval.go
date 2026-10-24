// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

func (p *Pipeline) requestApproval(ctx context.Context, agent, action string, act manifest.Action, args map[string]any, prov map[string][]string, dec policy.Decision) (*state.Approval, error) {
	now := p.Now()
	timeout := p.cfg().Approvals.Timeout.D()
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	a := &state.Approval{ID: state.NewApprovalID(), Created: now, Expires: now.Add(timeout), Agent: agent, Action: action,
		Class: act.Class, Status: state.Pending, Decision: dec, Args: args, Provenance: prov}
	a.Preview = Preview(agent, action, p.maskArgs(act, args), dec)
	if f := p.Describe[strings.SplitN(action, ".", 2)[0]]; f != nil {
		if text, err := f(agent, args); err == nil && text != "" {
			a.Preview = previewText(agent, action, strings.TrimRight(text, "\n")+"\n", dec)
		}
	}
	if text := p.accountPreview(agent, action, act, args); text != "" {
		// Учётки, которые использует действие: человек подтверждает и вход.
		a.Preview += "\n" + text
	}
	if len(act.Calls) > 0 {
		// Подвызовы покрываются этим подтверждением: человек должен их видеть.
		a.Preview += "\nalso runs: " + act.CallsText()
	}
	caller := CallerFrom(ctx)
	if caller != nil && caller.Dir != "" {
		// Откуда пришёл запрос: человек видит каталог агента.
		a.Preview += "\nfrom: " + caller.Dir
		if caller.Source != "peer" {
			a.Preview += " (as reported by the agent)"
		}
	}
	canon, _ := audit.Canonicalize(map[string]any{"id": a.ID, "agent": agent, "action": action, "args": p.maskArgs(act, args)})
	a.PreviewHash = audit.HashBytes(canon)
	if err := p.State.PutApproval(a); err != nil {
		return nil, err
	}
	if caller != nil {
		b, _ := json.Marshal(caller)
		if err := p.State.SetApprovalCaller(a.ID, b); err != nil {
			p.Log.Warn("approval caller", "id", a.ID, "err", err)
		}
	}
	if p.Notifier != nil {
		p.Notifier.Notify(a)
	}
	return a, nil
}

// Preview — что именно будет сделано и почему нужно подтверждение.
func Preview(agent, action string, args map[string]any, dec policy.Decision) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(args)) {
		var s string
		switch t := args[k].(type) {
		case string:
			s = t
		case []any:
			parts := make([]string, len(t))
			for i, x := range t {
				parts[i] = fmt.Sprint(x)
			}
			s = strings.Join(parts, ", ")
		default:
			bb, _ := json.Marshal(t)
			s = string(bb)
		}
		if s == "" {
			continue
		}
		if r := []rune(s); len(r) > 600 {
			s = string(r[:600]) + "…"
		}
		fmt.Fprintf(&b, "  %s: %s\n", k, s)
	}
	return previewText(agent, action, b.String(), dec)
}

// previewText — превью с описанием действия (body — строки с \n в конце).
func previewText(agent, action, body string, dec policy.Decision) string {
	why := dec.Reason
	if len(dec.RuleIDs) > 0 {
		why += " [" + strings.Join(dec.RuleIDs, ", ") + "]"
	}
	return fmt.Sprintf("%s wants to run %s\n%sWhy approval is needed: %s", agent, action, body, why)
}

// WaitFor ждёт решения по id до d (и для `gate4ai status <id> --wait`); если
// решения нет — pending с id.
func (p *Pipeline) WaitFor(ctx context.Context, id string, d time.Duration) Result {
	if d > 0 {
		ch := make(chan struct{})
		p.mu.Lock()
		p.waiters[id] = append(p.waiters[id], ch)
		p.mu.Unlock()
		t := time.NewTimer(d)
		defer t.Stop()
		if r, done := p.Status(id); done {
			return r
		}
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
		}
	}
	r, _ := p.Status(id)
	return r
}

// Status — состояние вызова по id подтверждения: итог или pending.
func (p *Pipeline) Status(id string) (Result, bool) {
	a, err := p.State.GetApproval(id)
	if err != nil {
		return Result{Status: StatusError, Code: "not_found", ID: id, Message: "unknown id"}, true
	}
	if len(a.Result) > 0 {
		var r Result
		if json.Unmarshal(a.Result, &r) == nil {
			return r, true
		}
	}
	return Result{Status: StatusPending, Code: CodePending, Action: a.Action, ID: a.ID,
		Reason: a.Decision.Reason, RuleIDs: a.Decision.RuleIDs,
		Message: fmt.Sprintf("%s: waiting for the user's decision (expires %s). Poll with `gate4ai status %s` or call again with --wait; do not work around it.",
			CodePending, a.Expires.UTC().Format(time.RFC3339), a.ID)}, false
}

// Decide — решение человека по запросу (из любого привязанного канала).
// Запрос одноразовый: повторное решение отклоняется.
func (p *Pipeline) Decide(ctx context.Context, id string, approve bool, channel, who string) (Result, error) {
	a, status, err := p.transition(id, approve, channel, who)
	if err != nil {
		return Result{}, err
	}
	p.running.Add(1)
	defer p.running.Done()
	return p.finish(ctx, a, status, channel, who), nil
}

// DecideAsync — то же, но действие исполняется в фоне: решение записано, когда
// метод вернулся без ошибки. Для каналов, которые не должны ждать исполнения
// (цикл опроса Telegram: пока идёт действие, бот должен принимать ответы,
// например код для входа на сайт). Итог — Status / WaitFor.
func (p *Pipeline) DecideAsync(ctx context.Context, id string, approve bool, channel, who string) error {
	a, status, err := p.transition(id, approve, channel, who)
	if err != nil {
		return err
	}
	p.running.Add(1)
	go func() {
		defer p.running.Done()
		p.finish(context.WithoutCancel(ctx), a, status, channel, who)
	}()
	return nil
}

// transition — решение человека в State Store (атомарно, одноразово).
// Просроченный запрос не одобряется: он сразу переводится в timeout (deny).
func (p *Pipeline) transition(id string, approve bool, channel, who string) (*state.Approval, string, error) {
	status := state.Rejected
	if approve {
		status = state.Approved
	}
	a, err := p.State.Decide(id, status, channel, who)
	if errors.Is(err, state.ErrExpired) {
		if a, terr := p.State.Decide(id, state.Timeout, "timeout", "system"); terr == nil {
			p.running.Add(1)
			go func() {
				defer p.running.Done()
				p.finish(context.Background(), a, state.Timeout, "timeout", "system")
			}()
		}
		return nil, "", err
	}
	if err != nil {
		return nil, "", err
	}
	return a, status, nil
}

func (p *Pipeline) decide(ctx context.Context, id, status, channel, who string) (Result, error) {
	a, err := p.State.Decide(id, status, channel, who)
	if err != nil {
		return Result{}, err
	}
	p.running.Add(1)
	defer p.running.Done()
	return p.finish(ctx, a, status, channel, who), nil
}

// finish исполняет или отклоняет запрос после решения и сохраняет итог.
func (p *Pipeline) finish(ctx context.Context, a *state.Approval, status, channel, who string) Result {
	// Квитанция — с вызывающим запроса, а не того, кто одобрил.
	ctx = WithCaller(ctx, p.approvalCaller(a.ID))
	appr := &audit.Approval{Channel: channel, Who: who, At: a.DecidedAt.UTC(), Result: status, PreviewHash: a.PreviewHash}
	var res Result
	cli, act, ok := p.Registry.Lookup(a.Action)
	switch {
	case !ok:
		res = Result{Status: StatusError, Code: CodeUnknownAction, Action: a.Action, ID: a.ID, Message: "connector no longer available"}
	case status == state.Approved:
		if dec, revoked := p.revoked(a); revoked {
			// Доступ агента отозван после запроса: «отзыв в один клик»
			// останавливает и поставленные в очередь действия.
			res = p.deny(ctx, a.Agent, act, a.Action, a.Args, dec, appr, "")
			break
		}
		// Действие исполняется не в контексте того, кто одобрил: HTTP-запрос
		// Web UI или трея заканчивается раньше многошагового действия. Срок
		// задают лимиты коннектора (limits.timeout_s). Политика, лимиты и
		// профиль проверяются заново перед исполнением (execute → admit).
		p.mu.Lock()
		nested := p.nestedRuns[a.ID]
		delete(p.nestedRuns, a.ID)
		p.mu.Unlock()
		if nested != nil {
			// Вложенный вызов коннектора: исполняется в контексте вызывающего
			// действия (его страница, срок и квитанция), которое ждёт решения.
			res = nested(appr)
			break
		}
		res = p.run(context.WithoutCancel(ctx), a.Agent, a.Action, cli, act, a.Args, a.Decision, appr)
	default:
		res = p.deny(ctx, a.Agent, act, a.Action, a.Args, a.Decision, appr, "")
	}
	res.ID = a.ID
	if err := p.State.SetResult(a.ID, res); err != nil {
		p.Log.Error("approval result", "id", a.ID, "err", err)
	}
	if status == state.Approved {
		_ = p.State.MarkDone(a.ID)
	}
	a.Status = status
	if p.Notifier != nil {
		p.Notifier.Resolved(a)
	}
	p.mu.Lock()
	for _, ch := range p.waiters[a.ID] {
		close(ch)
	}
	delete(p.waiters, a.ID)
	p.mu.Unlock()
	return res
}

// approvalCaller — вызывающий, сохранённый с запросом подтверждения.
func (p *Pipeline) approvalCaller(id string) *audit.Caller {
	raw := p.State.ApprovalCaller(id)
	if raw == nil {
		return nil
	}
	var c audit.Caller
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return &c
}

// revoked — владелец отозвал доступ агента после того, как запрос был создан
// (или State Store не ответил — тогда тоже отказ).
func (p *Pipeline) revoked(a *state.Approval) (policy.Decision, bool) {
	at, err := p.State.AgentRevokedAt(a.Agent)
	switch {
	case err != nil:
		return policy.Decision{Effect: config.EffectDeny, RuleIDs: []string{policy.RuleStateUnavailable},
			Reason: "state store unavailable, agent access cannot be checked: " + err.Error()}, true
	case !at.IsZero() && !at.Before(a.Created):
		return policy.Decision{Effect: config.EffectDeny, RuleIDs: []string{"agent-revoked"},
			Reason: fmt.Sprintf("access of agent %q was revoked after the request", a.Agent)}, true
	}
	return policy.Decision{}, false
}

// ExpireDue переводит просроченные запросы в timeout → deny (on_timeout: deny).
func (p *Pipeline) ExpireDue(ctx context.Context) int {
	ex, err := p.State.Expired()
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range ex {
		if _, err := p.decide(ctx, a.ID, state.Timeout, "timeout", "system"); err == nil {
			n++
		}
	}
	return n
}

// Wait дожидается завершения фоновых решений (для остановки и тестов).
func (p *Pipeline) Wait() { p.running.Wait() }
