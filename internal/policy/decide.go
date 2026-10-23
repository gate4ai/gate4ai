// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/internal/config"
)

// Config возвращает конфигурацию, из которой собран движок.
func (e *Engine) Config() *config.Config { return e.cfg }

// Decide принимает решение по запросу. Порядок: deny > ask > allow; если не
// сработало ни одно правило — значение по умолчанию класса эффекта.
// Ошибка вычисления условия трактуется в пользу строгости (fail-closed):
// правило deny или ask считается сработавшим, правило allow — нет.
func (e *Engine) Decide(req Request, counters Counters) Decision {
	if counters == nil {
		counters = NoCounters{}
	}
	agent, ok := e.cfg.Agents[req.Agent]
	if !ok {
		return Decision{Effect: config.EffectDeny, RuleIDs: []string{"agent-unknown"}, Reason: fmt.Sprintf("unknown agent %q", req.Agent)}
	}
	prof, ok := e.profiles[agent.Profile]
	if !ok {
		return Decision{Effect: config.EffectDeny, RuleIDs: []string{"profile-missing"}, Reason: fmt.Sprintf("profile %q not found", agent.Profile)}
	}
	if !req.Nested && !prof.cfg.HasConnector(req.Connector()) && !Implicit[req.Connector()] && !Everyone[req.Connector()] {
		return Decision{Effect: config.EffectDeny, RuleIDs: []string{"connector-not-in-profile"},
			Reason: fmt.Sprintf("connector %q is not available for profile %q", req.Connector(), agent.Profile)}
	}
	vars := e.activation(req, counters)

	type hit struct {
		id, effect, reason string
		obl                []string
	}
	var hits []hit
	var errs, skipped []string
	for _, c := range prof.rules {
		if !ActionMatches(c.rule.Action, req.Action) {
			continue
		}
		if why := allowSkip(c.rule, req.Action); why != "" {
			skipped = append(skipped, why)
			continue
		}
		fired := true
		if c.prg != nil {
			out, _, err := c.prg.Eval(vars)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", c.rule.ID, err))
				fired = c.rule.Effect != config.EffectAllow
			} else if b, ok := out.Value().(bool); !ok {
				errs = append(errs, fmt.Sprintf("%s: condition is not boolean", c.rule.ID))
				fired = c.rule.Effect != config.EffectAllow
			} else {
				fired = b
			}
		}
		// Вложенный вызов не спрашивают отдельно: его покрывает подтверждение
		// родительского действия, где подвызов показан (calls).
		if fired && !(req.Nested && c.rule.Effect == config.EffectAsk) {
			hits = append(hits, hit{c.rule.ID, c.rule.Effect, c.rule.Reason, c.rule.Obligations})
		}
	}
	amount := numArg(req.Args, "amount")
	for _, l := range prof.cfg.Limits {
		if !ActionMatches(l.Action, req.Action) {
			continue
		}
		var reason string
		switch {
		case l.PerDay > 0 && counters.CountToday(req.Agent, l.Action) >= l.PerDay:
			reason = fmt.Sprintf("daily limit of %d actions %s reached", l.PerDay, l.Action)
		case l.AmountPerDay > 0 && counters.AmountToday(req.Agent, l.Action)+amount > l.AmountPerDay:
			reason = fmt.Sprintf("daily amount limit %.2f for %s exceeded", l.AmountPerDay, l.Action)
		default:
			continue
		}
		hits = append(hits, hit{l.ID, config.EffectDeny, cmp.Or(l.Reason, reason), nil})
	}

	if err := stateErr(req, counters); err != nil {
		// Счётчики или метки происхождения неизвестны: лимиты и правила по
		// provenance не проверить — в пользу строгости.
		return Decision{Effect: config.EffectDeny, RuleIDs: []string{RuleStateUnavailable}, Errors: append(errs, err.Error()),
			Reason: "state store unavailable, limits and provenance cannot be checked: " + err.Error()}
	}
	for _, eff := range []string{config.EffectDeny, config.EffectAsk, config.EffectAllow} {
		d := Decision{Effect: eff, Errors: errs}
		obl := map[string]bool{}
		for _, h := range hits {
			if h.effect != eff {
				continue
			}
			d.RuleIDs = append(d.RuleIDs, h.id)
			d.Reason = cmp.Or(d.Reason, h.reason)
			for _, o := range h.obl {
				obl[o] = true
			}
		}
		if len(d.RuleIDs) == 0 {
			continue
		}
		d.Reason = cmp.Or(d.Reason, fmt.Sprintf("rule %s: %s", d.RuleIDs[0], eff))
		d.Obligations = slices.Sorted(maps.Keys(obl))
		return d
	}
	if req.Nested {
		return Decision{Effect: config.EffectAllow, Default: true, Errors: errs, RuleIDs: []string{},
			Reason: "nested call: no rule matched; covered by the decision on the calling action"}
	}
	eff := DefaultEffect(req.Class)
	reason := fmt.Sprintf("no rule matched; default for effect class %q is %s", req.Class, eff)
	if len(skipped) > 0 {
		reason = strings.Join(skipped, "; ") + "; default for effect class " + strconv.Quote(req.Class) + " is " + eff
	}
	return Decision{Effect: eff, Default: true, Errors: errs, RuleIDs: []string{}, Reason: reason}
}

// stateErr — ошибка State Store запроса или счётчиков.
func stateErr(req Request, counters Counters) error {
	if req.StateError != nil {
		return req.StateError
	}
	if c, ok := counters.(CountersErr); ok {
		return c.Err()
	}
	return nil
}

func (e *Engine) activation(req Request, counters Counters) map[string]any {
	vars := map[string]any{}
	prov := map[string][]string{}
	for k, v := range req.Args {
		vars[k] = v
		prov[k] = []string{}
	}
	for k, v := range req.Provenance {
		prov[k] = append([]string{}, v...)
	}
	lists := map[string][]string{}
	maps.Copy(lists, e.cfg.Lists)
	now := req.Now
	vars["provenance"] = prov
	vars["counters"] = map[string]float64{
		"spent_today":   counters.SpentToday(),
		"actions_today": float64(counters.CountToday(req.Agent, req.Action)),
		"amount_today":  counters.AmountToday(req.Agent, req.Action),
	}
	vars["lists"] = lists
	vars["now"] = now
	vars["hour"] = int64(now.Hour())
	vars["weekday"] = int64(now.Weekday())
	vars["agent"] = req.Agent
	vars["action"] = req.Action
	vars["args"] = req.Args
	return vars
}

func numArg(args map[string]any, k string) float64 {
	switch v := args[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}
