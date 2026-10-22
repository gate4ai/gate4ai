// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"errors"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/state"
)

// Recover закрывает то, что прервала остановка daemon (вызывается при старте,
// до приёма вызовов): квитанция-намерение без итога получает итоговую
// квитанцию «interrupted», одобренный запрос без итога — итог для
// `gate4ai status` (иначе он вечно «pending»). Возвращает число закрытых.
func (p *Pipeline) Recover() (int, error) {
	n := 0
	intents, err := p.Journal.OpenIntents()
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, e := range intents {
		r := e.Receipt
		fin := &audit.Receipt{Agent: r.Agent, Parent: r.Parent, Intent: r.ID, Action: r.Action, Class: r.Class, Args: r.Args,
			Decision: r.Decision, Approval: r.Approval,
			Execution:    audit.Execution{Status: audit.ExecInterrupted, Error: "gate4ai stopped while the action ran; it may or may not have taken effect"},
			Verification: audit.Verification{Status: "unverified"}}
		if _, err := p.Journal.Append(fin); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	unfinished, err := p.State.Unfinished()
	if err != nil {
		return n, errors.Join(append(errs, err)...)
	}
	for _, a := range unfinished {
		res := Result{Status: StatusError, Code: CodeExecution, Action: a.Action, ID: a.ID}
		if e, err := p.Journal.Find(a.PreviewHash); err == nil {
			// Исполнение начиналось (есть намерение и его итог).
			res.Receipt = audit.Short(e.Hash)
			res.Message = "gate4ai stopped while the approved action ran; it may or may not have taken effect: check the result and tell the user"
		} else {
			// Намерения нет — до исполнения не дошло.
			_, act, _ := p.Registry.Lookup(a.Action)
			r := &audit.Receipt{Agent: a.Agent, Action: a.Action, Class: a.Class, Args: p.maskArgs(act, a.Args),
				Decision:     receiptDecision(a.Decision.Effect, a.Decision),
				Approval:     &audit.Approval{Channel: a.Channel, Who: a.Who, At: a.DecidedAt.UTC(), Result: state.Approved, PreviewHash: a.PreviewHash},
				Execution:    audit.Execution{Status: audit.ExecInterrupted, Error: "gate4ai stopped before the approved action started; it was not run"},
				Verification: audit.Verification{Status: "not_applicable"}}
			hash, err := p.Journal.Append(r)
			if err != nil {
				errs = append(errs, err)
			}
			res.Receipt = audit.Short(hash)
			res.Message = "gate4ai stopped before the approved action started; it was NOT run: tell the user"
		}
		if err := p.State.SetResult(a.ID, res); err != nil {
			errs = append(errs, err)
			continue
		}
		_ = p.State.MarkDone(a.ID)
		n++
	}
	return n, errors.Join(errs...)
}
