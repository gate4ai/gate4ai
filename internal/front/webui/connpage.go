// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
)

// decisionMixed — у действия в профилях разные правила или правила с условием:
// одним списком на странице коннектора это не изменить.
const decisionMixed = "mixed"

// ActionView — строка таблицы «Действия» на странице коннектора.
type ActionView struct {
	manifest.Action
	Full     string // lk_emias.appointments
	Default  string // решение без правил по классу
	Decision string // "" — без правил (по классу); allow | ask | deny — правило без условия; mixed
	RuleIDs  []string
	Calls    []CallView
	Args     []argInfo
}

// CallView — подвызов действия другого коннектора (browser.open) для чипа и панели.
type CallView struct {
	Name, Description, Reason string
	Sites                     []string // сайты, которыми вызывающий ограничил поставщика
	UsedBy                    []string // действия этого коннектора с этим подвызовом
}

// callSites — сайты из params подвызова поставщика (domains), с подставленными настройками.
func callSites(m manifest.Manifest, target string, settings map[string]any) []string {
	var out []string
	for _, sc := range m.Capabilities.Services {
		if sc.Target != target {
			continue
		}
		ds, _ := sc.Params["domains"].([]any)
		for _, d := range ds {
			s, _ := d.(string)
			for _, x := range manifest.Expand(s, settings) {
				if !slices.Contains(out, x) {
					out = append(out, x)
				}
			}
		}
	}
	return out
}

// providerAction — описание действия поставщика (browser.open) из его манифеста.
func (u *UI) providerAction(call string) string {
	target, name, ok := strings.Cut(call, ".")
	if !ok {
		return ""
	}
	c, ok := u.D.Registry.Get(target)
	if !ok {
		return ""
	}
	for _, a := range c.Manifest().Actions {
		if a.Name == name {
			return a.Description
		}
	}
	return ""
}

// exactRules — правила профиля, под которые попадает действие (точное имя или glob).
func exactRules(p config.Profile, action string) []config.Rule {
	var out []config.Rule
	for _, r := range p.Rules {
		if ok, _ := path.Match(r.Action, action); ok || r.Action == action || r.Action == "*" {
			out = append(out, r)
		}
	}
	return out
}

// accessProfiles — профили с доступом к коннектору.
func accessProfiles(cfg *config.Config, conn string) []string {
	var out []string
	for n, p := range cfg.Profiles {
		if p.HasConnector(conn) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// actionDecision — что задано для действия в профиле: "" — правил нет (решает класс),
// effect — одно правило без условия на это действие, mixed — правила с условием или по маске.
func actionDecision(p config.Profile, action string) (string, []string) {
	rs := exactRules(p, action)
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	switch {
	case len(rs) == 0:
		return "", nil
	case len(rs) == 1 && rs[0].Action == action && strings.TrimSpace(rs[0].When) == "":
		return rs[0].Effect, ids
	}
	return decisionMixed, ids
}

// connectorProfile — профиль, для которого страница коннектора показывает решения:
// из запроса, иначе default или первый с доступом.
func connectorProfile(access []string, want string) string {
	if slices.Contains(access, want) {
		return want
	}
	if slices.Contains(access, "default") {
		return "default"
	}
	if len(access) > 0 {
		return access[0]
	}
	return ""
}

// actionViews — действия коннектора для его страницы и чипы подвызовов (каждый один раз).
func (u *UI) actionViews(name string, m manifest.Manifest, cfg *config.Config, profile string) ([]ActionView, []CallView) {
	settings := cfg.Connectors[name]
	tools := u.tools()
	var acts []ActionView
	var calls []CallView
	at := map[string]int{}
	for _, a := range m.Actions {
		if a.ServiceOnly {
			continue
		}
		v := ActionView{Action: a, Full: name + "." + a.Name, Default: defaultEffect(a.Class)}
		if profile != "" {
			v.Decision, v.RuleIDs = actionDecision(cfg.Profiles[profile], v.Full)
		}
		if t, ok := toolSchema(tools, v.Full); ok {
			v.Args = actionArgs(t)
		}
		for _, c := range a.Calls {
			target, _, _ := strings.Cut(c, ".")
			cv := CallView{Name: c, Description: u.providerAction(c), Reason: a.CallReasons[c], Sites: callSites(m, target, settings)}
			v.Calls = append(v.Calls, cv)
			if i, ok := at[c]; ok {
				calls[i].UsedBy = append(calls[i].UsedBy, a.Name)
				continue
			}
			at[c] = len(calls)
			cv.Reason, cv.UsedBy = "", []string{a.Name}
			calls = append(calls, cv)
		}
		acts = append(acts, v)
	}
	// «Используют все действия» — короче, чем перечислять.
	for i := range calls {
		if len(calls[i].UsedBy) == len(acts) && len(acts) > 1 {
			calls[i].UsedBy = nil
		}
	}
	return acts, calls
}

// saveActionDecision — решение для действия в профиле: правило без условия;
// "" (без правил) — убрать его, тогда решает класс действия.
func (u *UI) saveActionDecision(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	prof := r.PostForm.Get("profile")
	back := "/connectors/" + url.PathEscape(name) + "?profile=" + url.QueryEscape(prof) + "#actions"
	act, effect := r.PostForm.Get("action"), r.PostForm.Get("effect")
	if effect != "" && effect != config.EffectAllow && effect != config.EffectAsk && effect != config.EffectDeny {
		return redirectErr(back, fmt.Errorf("unknown decision %q", effect))
	}
	full := name + "." + act
	if _, ok := toolSchema(u.tools(), full); !ok {
		return redirectErr(back, fmt.Errorf("connector %s has no action %s", name, act))
	}
	cfg := u.D.Config.Current()
	if !slices.Contains(accessProfiles(cfg, name), prof) {
		return redirectErr(back, fmt.Errorf("у профиля %q нет доступа к коннектору: отметьте его в «Кому доступен»", prof))
	}
	if d, _ := actionDecision(cfg.Profiles[prof], full); d == decisionMixed {
		return redirectErr(back, fmt.Errorf("для %s в профиле %s есть правила с условием или по маске — измените их в «Правилах»", full, prof))
	}
	err := u.edit("decision for "+full+" in "+prof, func(c *config.Config) error {
		p := c.Profiles[prof]
		p.Rules = slices.DeleteFunc(p.Rules, func(x config.Rule) bool { return x.Action == full && strings.TrimSpace(x.When) == "" })
		c.Profiles[prof] = p
		if effect == "" {
			return nil
		}
		return c.PutRule(prof, config.Rule{ID: c.Profiles[prof].UniqueRuleID(strings.ReplaceAll(full, ".", "-") + "-" + effect), Action: full, Effect: effect})
	})
	if err != nil {
		return redirectErr(back, err)
	}
	what := "без правил — " + effectWord(defaultEffect(classOf(u, full)))
	if effect != "" {
		what = effectWord(effect)
	}
	return redirect(back, fmt.Sprintf("%s в профиле %s: %s.", full, prof, what))
}

func classOf(u *UI, action string) string {
	if t, ok := toolSchema(u.tools(), action); ok {
		return t.Class
	}
	return ""
}
