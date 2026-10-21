// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/internal/config"
)

// RuleGroup — строка таблицы правил: одно правило или группа правил, которые
// отличаются только последней частью действия (files.read, files.list) при
// одинаковых решении и условии. Группа — только вид: в политике правила
// остаются отдельными (glob files.* покрыл бы и новые действия).
type RuleGroup struct {
	Label  string // files.{list, read}; files.* — если в группе все действия коннектора
	Effect string
	When   string
	Reason string // общая причина; "" при Mixed
	Mixed  bool   // у правил группы разные причины
	Rules  []config.Rule
}

// Single — строка из одного правила.
func (g RuleGroup) Single() bool { return len(g.Rules) == 1 }

// groupRules собирает группы в порядке первого правила каждой; actions —
// все действия профиля (чтобы назвать полную группу files.*).
func groupRules(rules []config.Rule, actions []string) []RuleGroup {
	type key struct{ prefix, effect, when string }
	var out []RuleGroup
	at := map[key]int{}
	for _, r := range rules {
		prefix, _, ok := cutLast(r.Action)
		if !ok || strings.ContainsAny(r.Action, "*?[") {
			out = append(out, RuleGroup{Label: r.Action, Effect: r.Effect, When: r.When, Reason: r.Reason, Rules: []config.Rule{r}})
			continue
		}
		k := key{prefix, r.Effect, strings.TrimSpace(r.When)}
		if i, ok := at[k]; ok {
			g := &out[i]
			g.Rules = append(g.Rules, r)
			if g.Reason != r.Reason {
				g.Mixed, g.Reason = true, ""
			}
			continue
		}
		at[k] = len(out)
		out = append(out, RuleGroup{Label: r.Action, Effect: r.Effect, When: r.When, Reason: r.Reason, Rules: []config.Rule{r}})
	}
	for i, g := range out {
		if g.Single() {
			continue
		}
		prefix, _, _ := cutLast(g.Rules[0].Action)
		var names []string
		for _, r := range g.Rules {
			_, n, _ := cutLast(r.Action)
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		all := 0
		for _, a := range actions {
			if p, n, ok := cutLast(a); ok && p == prefix {
				all++
				if !slices.Contains(names, n) {
					all = -1 << 30
				}
			}
		}
		if all == len(names) {
			out[i].Label = prefix + ".*"
		} else {
			out[i].Label = prefix + ".{" + strings.Join(names, ", ") + "}"
		}
	}
	return out
}

// cutLast — «files.read» → «files», «read».
func cutLast(action string) (string, string, bool) {
	i := strings.LastIndexByte(action, '.')
	if i <= 0 || i == len(action)-1 {
		return "", "", false
	}
	return action[:i], action[i+1:], true
}
