// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"fmt"
	"sort"

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
	var errs []string
	for _, c := range prof.rules {
		if !ActionMatches(c.rule.Action, req.Action) {
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
		if fired && req.Nested && c.rule.Effect == config.EffectAsk {
			// Вложенный вызов не спрашивают отдельно: его покрывает подтверждение
			// родительского действия, где подвызов показан (calls).
			continue
		}
		if fired {
			hits = append(hits, hit{c.rule.ID, c.rule.Effect, c.rule.Reason, c.rule.Obligations})
		}
	}
	amount := numArg(req.Args, "amount")
	for _, l := range prof.cfg.Limits {
		if !ActionMatches(l.Action, req.Action) {
			continue
		}
		reason := l.Reason
		if l.PerDay > 0 && counters.CountToday(req.Agent, l.Action) >= l.PerDay {
			if reason == "" {
				reason = fmt.Sprintf("daily limit of %d actions %s reached", l.PerDay, l.Action)
			}
			hits = append(hits, hit{l.ID, config.EffectDeny, reason, nil})
		} else if l.AmountPerDay > 0 && counters.AmountToday(req.Agent, l.Action)+amount > l.AmountPerDay {
			if reason == "" {
				reason = fmt.Sprintf("daily amount limit %.2f for %s exceeded", l.AmountPerDay, l.Action)
			}
			hits = append(hits, hit{l.ID, config.EffectDeny, reason, nil})
		}
	}

	for _, eff := range []string{config.EffectDeny, config.EffectAsk, config.EffectAllow} {
		d := Decision{Effect: eff, Errors: errs}
		obl := map[string]bool{}
		for _, h := range hits {
			if h.effect != eff {
				continue
			}
			d.RuleIDs = append(d.RuleIDs, h.id)
			if d.Reason == "" && h.reason != "" {
				d.Reason = h.reason
			}
			for _, o := range h.obl {
				obl[o] = true
			}
		}
		if len(d.RuleIDs) == 0 {
			continue
		}
		if d.Reason == "" {
			d.Reason = fmt.Sprintf("rule %s: %s", d.RuleIDs[0], eff)
		}
		for o := range obl {
			d.Obligations = append(d.Obligations, o)
		}
		sort.Strings(d.Obligations)
		return d
	}
	if req.Nested {
		return Decision{Effect: config.EffectAllow, Default: true, Errors: errs, RuleIDs: []string{},
			Reason: "nested call: no rule matched; covered by the decision on the calling action"}
	}
	eff := DefaultEffect(req.Class)
	return Decision{Effect: eff, Default: true, Errors: errs, RuleIDs: []string{},
		Reason: fmt.Sprintf("no rule matched; default for effect class %q is %s", req.Class, eff)}
}

// Tighten применяет консультативный сигнал (например, от LLM «похоже на фишинг»):
// он может только ужесточить решение allow → ask, но не ослабить.
func Tighten(d Decision, signal string) Decision {
	if d.Effect == config.EffectAllow {
		d.Effect = config.EffectAsk
		d.RuleIDs = append(append([]string{}, d.RuleIDs...), "advisory")
		d.Reason = "advisory signal: " + signal
		d.Default = false
	}
	return d
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
	for k, v := range e.cfg.Lists {
		lists[k] = v
	}
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
