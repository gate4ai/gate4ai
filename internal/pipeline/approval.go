// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/sdk-go/manifest"
)

func (p *Pipeline) requestApproval(agent, action string, act manifest.Action, args map[string]any, prov map[string][]string, dec policy.Decision) (*state.Approval, error) {
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
			a.Preview = previewText(agent, action, text, dec)
		}
	}
	if text := p.accountPreview(agent, action, act, args); text != "" {
		// Учётки, которые использует действие: человек подтверждает и вход.
		a.Preview += "\n" + text
	}
	if len(act.Calls) > 0 {
		// Подвызовы покрываются этим подтверждением: человек должен их видеть.
		a.Preview += "\nalso runs: " + strings.Join(act.Calls, ", ")
	}
	canon, _ := audit.Canonicalize(map[string]any{"id": a.ID, "agent": agent, "action": action, "args": p.maskArgs(act, args)})
	a.PreviewHash = audit.HashBytes(canon)
	if err := p.State.PutApproval(a); err != nil {
		return nil, err
	}
	if p.Notifier != nil {
		p.Notifier.Notify(a)
	}
	return a, nil
}

// Preview — что именно будет сделано и почему нужно подтверждение.
func Preview(agent, action string, args map[string]any, dec policy.Decision) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s wants to run %s\n", agent, action)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := args[k]
		var s string
		switch t := v.(type) {
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
	why := dec.Reason
	if len(dec.RuleIDs) > 0 {
		why += " [" + strings.Join(dec.RuleIDs, ", ") + "]"
	}
	fmt.Fprintf(&b, "Why approval is needed: %s", why)
	return b.String()
}

// previewText — превью с описанием действия вместо списка аргументов.
func previewText(agent, action, text string, dec policy.Decision) string {
	why := dec.Reason
	if len(dec.RuleIDs) > 0 {
		why += " [" + strings.Join(dec.RuleIDs, ", ") + "]"
	}
	return fmt.Sprintf("%s wants to run %s\n%s\nWhy approval is needed: %s", agent, action, strings.TrimRight(text, "\n"), why)
}

// wait ждёт решения до d; если решения нет — pending с id.
func (p *Pipeline) wait(ctx context.Context, id string, d time.Duration) Result {
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
	status := state.Rejected
	if approve {
		status = state.Approved
	}
	return p.decide(ctx, id, status, channel, who)
}

func (p *Pipeline) decide(ctx context.Context, id, status, channel, who string) (Result, error) {
	a, err := p.State.Decide(id, status, channel, who)
	if err != nil {
		return Result{}, err
	}
	p.running.Add(1)
	defer p.running.Done()
	appr := &audit.Approval{Channel: channel, Who: who, At: a.DecidedAt.UTC(), Result: status, PreviewHash: a.PreviewHash}
	var res Result
	cli, act, ok := p.Registry.Lookup(a.Action)
	switch {
	case !ok:
		res = Result{Status: StatusError, Code: CodeUnknownAction, Action: a.Action, ID: a.ID, Message: "connector no longer available"}
	case status == state.Approved:
		// Действие исполняется не в контексте того, кто одобрил: HTTP-запрос
		// Web UI или трея заканчивается раньше многошагового действия. Срок
		// задают лимиты коннектора (limits.timeout_s).
		res = p.run(context.WithoutCancel(ctx), a.Agent, a.Action, cli, act, a.Args, a.Decision, appr)
	default:
		res = p.deny(a.Agent, act, a.Action, a.Args, a.Decision, appr)
	}
	res.ID = a.ID
	_ = p.State.SetResult(a.ID, res)
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
	return res, nil
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

// WaitFor — ожидание итога по id до d (для `gate4ai status <id> --wait`).
func (p *Pipeline) WaitFor(ctx context.Context, id string, d time.Duration) Result {
	return p.wait(ctx, id, d)
}
