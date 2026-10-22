// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// ---- Правила ----

func (u *UI) policyPage(r *http.Request) (string, map[string]any, error) {
	l := u.lang(r)
	cfg := u.D.Config.Current()
	// Права с агентами — первыми: их и правят; профили без агентов (default) — в конце.
	var profiles []string
	for _, g := range grants(cfg) {
		profiles = append(profiles, g.Profile)
	}
	pa := map[string][]string{}
	for _, p := range profiles {
		pa[p] = profileAgents(cfg, p)
	}
	prof := r.URL.Query().Get("profile")
	if _, ok := cfg.Profiles[prof]; !ok && len(profiles) > 0 {
		prof = profiles[0]
	}
	p := cfg.Profiles[prof]
	agents := pa[prof]
	// Что будет по умолчанию с каждым действием профиля.
	type actRow struct {
		Tool    daemon.Tool
		Title   string
		Default string
		Rules   []string
	}
	var acts []actRow
	for _, t := range u.tools() {
		if !p.HasConnector(t.Connector) {
			continue
		}
		title, _ := u.actionText(l, t.Name)
		row := actRow{Tool: t, Title: title, Default: defaultEffect(t.Class)}
		for _, rl := range p.Rules {
			if ok, _ := path.Match(rl.Action, t.Name); ok || rl.Action == t.Name || rl.Action == "*" {
				row.Rules = append(row.Rules, rl.ID)
			}
		}
		acts = append(acts, row)
	}
	names := make([]string, len(acts))
	for i, a := range acts {
		names[i] = a.Tool.Name
	}
	var conns []connRef
	for _, c := range u.connectorNames() {
		conns = append(conns, connRef{c, u.connTitle(l, c)})
	}
	_, n := u.D.Config.CurrentRaw()
	vs, _ := u.D.Config.Versions()
	if len(vs) > 15 {
		vs = vs[len(vs)-15:]
	}
	slices.Reverse(vs)
	data := map[string]any{"Profiles": profiles, "ProfileAgents": pa, "Profile": prof, "P": p, "Groups": groupRules(p.Rules, names), "Agents": agents, "Acts": acts,
		"Connectors": conns, "Lists": cfg.Lists, "Version": n, "Versions": vs, "AllAgents": cfg.Agents, "Tools": u.tools()}
	u.simData(r.URL.Query().Get("sim"), data)
	return "policy", data, nil
}

func (u *UI) simData(action string, data map[string]any) {
	tools := u.tools()
	if action == "" && len(tools) > 0 {
		action = tools[0].Name
	}
	if t, ok := toolSchema(tools, action); ok {
		data["SimTool"] = t
	}
	data["SimAction"] = action
}

func (u *UI) yamlPage(r *http.Request) (string, map[string]any, error) {
	return "policy_yaml", u.yamlData(""), nil
}

func (u *UI) yamlData(y string) map[string]any {
	raw, n := u.D.Config.CurrentRaw()
	if y == "" {
		y = string(raw)
	}
	vs, _ := u.D.Config.Versions()
	slices.Reverse(vs)
	return map[string]any{"YAML": y, "Version": n, "Base": n, "Versions": vs, "page": "policy_yaml"}
}

func (u *UI) savePolicy(w http.ResponseWriter, r *http.Request) map[string]any {
	y := r.PostForm.Get("yaml")
	// base — версия, открытая в редакторе: если её сменили (одобренный
	// change_policy, другая вкладка), сохранение не откатывает чужую правку.
	base, _ := strconv.Atoi(r.PostForm.Get("base"))
	v, err := u.D.Config.PutIf(owner, []byte(y), r.PostForm.Get("comment"), base)
	if err != nil {
		d := u.yamlData(y)
		d["error"] = err
		if errors.Is(err, config.ErrConflict) {
			d["error"] = M("The policy was changed while you were editing it (it is version %d now). Your text below is not saved: move your edit into the current version (open the page again) and save.", d["Version"].(int))
			d["Base"] = base
		}
		return d
	}
	return redirect("/policy/yaml", M("Saved as version %d", v.N))
}

func (u *UI) rollback(w http.ResponseWriter, r *http.Request) map[string]any {
	n, _ := strconv.Atoi(r.PostForm.Get("version"))
	v, err := u.D.Config.Rollback(owner, n)
	back := or(r.PostForm.Get("back"), "/policy")
	if err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, M("Version %d is back (saved as %d)", n, v.N))
}

// simulate — «что будет, если агент попробует…».
func (u *UI) simulate(w http.ResponseWriter, r *http.Request) map[string]any {
	req := &http.Request{URL: &url.URL{RawQuery: url.Values{"profile": {r.PostForm.Get("profile")}, "sim": {r.PostForm.Get("action")}}.Encode()}, Header: r.Header}
	_, d, _ := u.policyPage(req)
	d["page"] = "policy"
	agent, action := r.PostForm.Get("agent"), r.PostForm.Get("action")
	t, ok := toolSchema(u.tools(), action)
	if !ok {
		d["error"] = M("Unknown action %s", action)
		return d
	}
	var args map[string]any
	var err error
	if raw := strings.TrimSpace(r.PostForm.Get("args")); raw != "" {
		err = json.Unmarshal([]byte(raw), &args)
	} else {
		args, err = argsFromForm(t, r.PostForm.Get)
	}
	if err != nil {
		d["error"] = M("Arguments: %s", err)
		return d
	}
	_, act, _ := u.D.Registry.Lookup(action)
	nargs, err := act.Normalize(args)
	if err != nil {
		d["error"] = err
		return d
	}
	dec := u.D.Pipeline.Engine().Decide(policy.Request{Agent: agent, Action: action, Class: act.Class, Args: nargs, Now: u.Now()}, u.D.State.Counters())
	sa := map[string]string{}
	for k, v := range r.PostForm {
		if strings.HasPrefix(k, "arg.") && len(v) > 0 {
			sa[k] = v[0]
		}
	}
	d["Sim"], d["SimAgent"], d["SimArgs"] = dec, agent, sa
	return d
}

// ruleForm — данные формы правила.
type ruleForm struct {
	Profile, ID, Action, Effect, Reason, When string
	WhenParts                                 []string // условие CEL по частям «&&» (чипы)
	Conds                                     []Cond   // блоки условия (чипы)
	New                                       Cond     // недобавленное условие с ошибкой
	Lang                                      string   // язык страницы (чипы условий)
}

func (u *UI) rulePage(r *http.Request) (string, map[string]any, error) {
	q := r.URL.Query()
	cfg := u.D.Config.Current()
	f := ruleForm{Profile: q.Get("profile"), ID: q.Get("id"), Action: q.Get("action"), Effect: or(q.Get("effect"), "allow"), Lang: u.lang(r)}
	var note Msg
	// Правка существующего правила (если в форме не выбрано другое действие —
	// тогда условия прежнего действия к нему не относятся).
	if f.ID != "" {
		for _, rl := range cfg.Profiles[f.Profile].Rules {
			if rl.ID == f.ID && (f.Action == "" || f.Action == rl.Action) {
				f.Action, f.Effect, f.Reason, f.When = rl.Action, rl.Effect, rl.Reason, rl.When
			}
		}
	}
	f.WhenParts = splitAnd(f.When)
	// На основе запроса подтверждения или квитанции журнала.
	var agent string
	var args map[string]any
	approvalID := ""
	if id := q.Get("approval"); id != "" {
		if a, err := u.D.State.GetApproval(id); err == nil {
			approvalID = a.ID
			agent, f.Action, args = a.Agent, a.Action, a.Args
			note = M("A rule based on the request %s → %s. Narrow the conditions so nothing extra is allowed.", a.Agent, a.Action)
		}
	}
	if h := q.Get("receipt"); h != "" {
		if e, err := u.D.Journal.Find(h); err == nil {
			agent, f.Action, args = e.Receipt.Agent, e.Receipt.Action, e.Receipt.Args
			note = M("A rule based on journal entry #%s. Narrow the conditions so nothing extra is allowed.", audit.Short(e.Hash))
		}
	}
	if agent != "" && f.Profile == "" {
		f.Profile = cfg.Agents[agent].Profile
	}
	if f.Profile == "" {
		for p := range cfg.Profiles {
			f.Profile = p
			break
		}
	}
	if t, ok := toolSchema(u.tools(), f.Action); args != nil && ok {
		f.Conds = suggestConds(t, args)
	}
	return "rule", u.ruleData(f, note, approvalID), nil
}

// ruleData — данные страницы правила для формы f.
func (u *UI) ruleData(f ruleForm, note Msg, approvalID string) map[string]any {
	cfg := u.D.Config.Current()
	tools := u.tools()
	t, hasTool := toolSchema(tools, f.Action)
	profiles := slices.Sorted(maps.Keys(cfg.Profiles))
	data := map[string]any{"F": f, "Chips": ruleChips(f.Lang, f), "Tools": tools, "Profiles": profiles, "Kinds": condKinds, "Lists": cfg.Lists,
		"Note": note, "Approval": approvalID}
	if hasTool {
		data["Tool"], data["Args"] = t, actionArgs(t)
	}
	return data
}

// amountArg — числовые аргументы, для которых «не больше значения» осмысленно.
var amountArg = regexp.MustCompile(`(?i)amount|sum|total|price|cost|qty|quantity|count|limit`)

// suggestConds — условия по аргументам конкретного вызова.
func suggestConds(t daemon.Tool, args map[string]any) []Cond {
	var out []Cond
	for _, a := range actionArgs(t) {
		v, ok := args[a.Name]
		if !ok {
			continue
		}
		s := fmt.Sprint(v)
		switch {
		case a.Format == "path":
			out = append(out, Cond{Kind: "in_folder", Arg: a.Name, Value: path.Dir(strings.ReplaceAll(s, `\`, "/"))})
		case a.Format == "url":
			if pu, err := url.Parse(s); err == nil && pu.Host != "" {
				out = append(out, Cond{Kind: "domain", Arg: a.Name, Value: pu.Hostname()})
			}
		case a.Array && a.Format == "email":
			out = append(out, Cond{Kind: "in_list", Arg: a.Name, Value: "contacts_trusted"})
		case (a.Type == "number" || a.Type == "integer") && amountArg.MatchString(a.Name):
			// «не больше» — для сумм и количеств; у технических чисел
			// (timeout_ms, max_chars) условие ничего не значит.
			out = append(out, Cond{Kind: "lte", Arg: a.Name, Value: s})
		}
	}
	return out
}

func (u *UI) saveRule(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	form := parseRuleForm(f)
	form.Lang = u.lang(r)
	back := "/policy/rule?" + url.Values{"profile": {form.Profile}, "action": {form.Action}, "id": {form.ID}, "effect": {form.Effect}}.Encode()
	t, ok := toolSchema(u.tools(), form.Action)
	var args []argInfo
	if ok {
		args = actionArgs(t)
	}
	// «+ условие» и «×» — та же страница с изменёнными условиями, без сохранения.
	if f.Has("add_cond") || f.Get("drop") != "" {
		err := editRuleForm(&form, f, args)
		data := u.ruleData(form, Msg{}, f.Get("approval"))
		data["page"] = "rule"
		if err != nil {
			data["error"] = err
		}
		return data
	}
	conds := form.Conds
	when, err := BuildWhen(conds, args)
	if err != nil {
		return redirectErr(back, err)
	}
	// Новые списки из блоков «из списка»: элементы — в поле list_items_<i>.
	newLists := map[string][]string{}
	for _, c := range conds {
		if c.Kind == "in_list" {
			if items := splitItems(c.Items); len(items) > 0 {
				newLists[c.Value] = items
			}
		}
	}
	parts := append([]string{}, form.WhenParts...)
	if raw := strings.TrimSpace(f.Get("when")); raw != "" {
		parts = append(parts, raw)
	}
	when = joinAnd(append(parts, when))
	err = u.edit("rule in "+form.Profile, func(c *config.Config) error {
		for name, items := range newLists {
			if err := c.SetList(name, append(c.Lists[name], items...)); err != nil {
				return err
			}
		}
		id := form.ID
		if id == "" {
			// Такое же правило уже есть — второе ничего не изменит.
			for _, r := range c.Profiles[form.Profile].Rules {
				if r.Action == form.Action && r.When == when && r.Effect == form.Effect {
					return &duplicateRule{id: r.ID}
				}
			}
			id = c.Profiles[form.Profile].UniqueRuleID(strings.NewReplacer(".", "-", "*", "all").Replace(form.Action) + "-" + form.Effect)
		}
		// Действие могло ещё не быть доступно профилю.
		if conn := strings.SplitN(form.Action, ".", 2)[0]; conn != "*" {
			_ = c.SetProfileConnector(form.Profile, conn, true)
		}
		return c.PutRule(form.Profile, config.Rule{ID: id, Action: form.Action, When: when, Effect: form.Effect, Reason: form.Reason})
	})
	var dup *duplicateRule
	if errors.As(err, &dup) {
		return redirect("/policy?profile="+url.QueryEscape(form.Profile)+"#rule-"+dup.id, M("This rule already exists: %s. Nothing was added.", dup.id))
	}
	if err != nil {
		return redirectErr(back, err)
	}
	if warn := u.overridden(form.Profile, form.Action, form.Effect, f.Get("approval")); warn != nil {
		return redirectErr("/policy?profile="+url.QueryEscape(form.Profile), M("The rule is saved but will not take effect: %s", warn))
	}
	return redirect("/policy?profile="+url.QueryEscape(form.Profile), "The rule is saved")
}

type duplicateRule struct{ id string }

func (d *duplicateRule) Error() string { return "duplicate of rule " + d.id }

// overridden — почему правило с эффектом effect не изменит решение: по
// исходному запросу (approval) — решение симулятора по новой политике, без
// него — правило сильнее (deny > ask > allow) на то же действие без условия.
func (u *UI) overridden(profile, action, effect, approval string) error {
	rank := map[string]int{config.EffectAllow: 0, config.EffectAsk: 1, config.EffectDeny: 2}
	cfg := u.D.Config.Current()
	if approval != "" {
		if a, err := u.D.State.GetApproval(approval); err == nil && a.Action == action {
			eng, err := policy.New(cfg, policy.Catalog(u.D.Registry.Catalog()))
			if err == nil {
				_, act, _ := u.D.Registry.Lookup(action)
				dec := eng.Decide(policy.Request{Agent: a.Agent, Action: action, Class: act.Class, Args: a.Args, Now: time.Now()}, u.D.State.Counters())
				if dec.Effect != effect && rank[dec.Effect] > rank[effect] {
					return M("for this request the decision is still “%s” by rule %s — it is stronger, the order of rules does not matter. Remove or narrow %s.",
						Word(effectWords[dec.Effect]), strings.Join(dec.RuleIDs, ", "), strings.Join(dec.RuleIDs, ", "))
				}
				return nil
			}
		}
	}
	for _, r := range cfg.Profiles[profile].Rules {
		if r.When == "" && rank[r.Effect] > rank[effect] && policy.ActionMatches(r.Action, action) {
			return M("rule %s (%s, no condition) on %s is stronger — the order of rules does not matter. Remove or narrow %s.",
				r.ID, Word(effectWords[r.Effect]), r.Action, r.ID)
		}
	}
	return nil
}

func (u *UI) deleteRule(w http.ResponseWriter, r *http.Request) map[string]any {
	p, id := r.PostForm.Get("profile"), r.PostForm.Get("id")
	back := "/policy?profile=" + url.QueryEscape(p)
	if err := u.edit("remove rule "+id, func(c *config.Config) error { return c.RemoveRule(p, id) }); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, M("Rule %s removed", id))
}

func (u *UI) moveRule(w http.ResponseWriter, r *http.Request) map[string]any {
	p, id := r.PostForm.Get("profile"), r.PostForm.Get("id")
	delta, _ := strconv.Atoi(r.PostForm.Get("delta"))
	back := "/policy?profile=" + url.QueryEscape(p)
	if err := u.edit("move rule "+id, func(c *config.Config) error { return c.MoveRule(p, id, delta) }); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "")
}

// templateView — шаблон с доступностью для профиля.
type templateView struct {
	Template
	Missing []string
}

func (u *UI) templatesPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	prof := r.URL.Query().Get("profile")
	tools := u.tools()
	var ready, missing []templateView
	for _, t := range Templates {
		v := templateView{Template: t}
		for _, need := range t.Needs {
			if _, ok := toolSchema(tools, need); !ok {
				v.Missing = append(v.Missing, strings.SplitN(need, ".", 2)[0])
			}
		}
		// Деньги — только когда есть платёжные действия.
		if t.ID == "money-ask" && !slices.ContainsFunc(tools, func(x daemon.Tool) bool { return x.Class == "money" }) {
			v.Missing = append(v.Missing, "money")
		}
		if len(v.Missing) > 0 {
			missing = append(missing, v)
		} else {
			ready = append(ready, v)
		}
	}
	if prof == "" {
		for _, g := range grants(cfg) {
			if len(g.Agents) > 0 {
				prof = g.Profile
				break
			}
		}
	}
	return "templates", map[string]any{"Templates": ready, "Missing": missing, "Profile": prof, "Grants": grants(cfg)}, nil
}

// ruleChange — что изменит шаблон в правилах профиля (предпросмотр).
type ruleChange struct {
	Kind string // add | replace | limit
	Rule config.Rule
}

// templatePreview — применение шаблона к копии конфигурации: какие правила появятся.
func (u *UI) templatePreview(t Template, prof string, params map[string]string) ([]ruleChange, error) {
	raw, _ := u.D.Config.CurrentRaw()
	before, err := config.Decode(raw)
	if err != nil {
		return nil, err
	}
	after, err := config.Decode(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := after.Profiles[prof]; !ok {
		return nil, M("Agent permissions %q not found", prof)
	}
	if err := t.Apply(after, prof, params, u.tools()); err != nil {
		return nil, err
	}
	old := map[string]config.Rule{}
	for _, r := range before.Profiles[prof].Rules {
		old[r.ID] = r
	}
	var out []ruleChange
	for _, r := range after.Profiles[prof].Rules {
		o, ok := old[r.ID]
		switch {
		case !ok:
			out = append(out, ruleChange{"add", r})
		case o.Action != r.Action || o.When != r.When || o.Effect != r.Effect:
			out = append(out, ruleChange{"replace", r})
		}
	}
	oldLim := map[string]bool{}
	for _, l := range before.Profiles[prof].Limits {
		oldLim[l.ID] = true
	}
	for _, l := range after.Profiles[prof].Limits {
		if !oldLim[l.ID] {
			out = append(out, ruleChange{Kind: "limit", Rule: config.Rule{ID: l.ID, Action: l.Action, Reason: l.Reason}})
		}
	}
	return out, nil
}

// applyTemplate — сначала предпросмотр (что добавится), затем применение (confirm=1).
func (u *UI) applyTemplate(w http.ResponseWriter, r *http.Request) map[string]any {
	prof, id := r.PostForm.Get("profile"), r.PostForm.Get("template")
	t, ok := findTemplate(id)
	if !ok {
		return redirectErr("/policy/templates", M("Unknown ready-made rules %q", id))
	}
	params := map[string]string{}
	for _, p := range t.Params {
		params[p.Name] = r.PostForm.Get(p.Name)
	}
	back := "/policy/templates?profile=" + url.QueryEscape(prof)
	if r.PostForm.Get("confirm") != "1" {
		changes, err := u.templatePreview(t, prof, params)
		if err != nil {
			return redirectErr(back, err)
		}
		req := &http.Request{URL: &url.URL{RawQuery: url.Values{"profile": {prof}}.Encode()}, Header: r.Header}
		_, d, _ := u.templatesPage(req)
		d["page"] = "templates"
		d["Preview"], d["PreviewT"], d["PreviewParams"], d["PreviewProfile"] = changes, t, params, prof
		return d
	}
	if err := u.edit("template "+id+" for "+prof, func(c *config.Config) error {
		if _, ok := c.Profiles[prof]; !ok {
			return M("Agent permissions %q not found", prof)
		}
		return t.Apply(c, prof, params, u.tools())
	}); err != nil {
		return redirectErr(back, err)
	}
	return redirect("/policy?profile="+url.QueryEscape(prof), M("“%s” applied", Word(t.Title)))
}

func (u *UI) createProfile(w http.ResponseWriter, r *http.Request) map[string]any {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if err := u.edit("add profile "+name, func(c *config.Config) error {
		if _, ok := c.Profiles[name]; ok {
			return M("Agent permissions %q already exist", name)
		}
		return c.EnsureProfile(name)
	}); err != nil {
		return redirectErr("/policy", err)
	}
	return redirect("/policy?profile="+url.QueryEscape(name), M("Permissions %s created", name))
}

func (u *UI) saveProfileConnectors(w http.ResponseWriter, r *http.Request) map[string]any {
	prof := r.PostForm.Get("profile")
	on := r.PostForm["connectors"]
	back := "/policy?profile=" + url.QueryEscape(prof)
	if err := u.edit("profile "+prof+" connectors", func(c *config.Config) error {
		for _, conn := range u.connectorNames() {
			if err := c.SetProfileConnector(prof, conn, slices.Contains(on, conn)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Access saved")
}

func (u *UI) saveList(w http.ResponseWriter, r *http.Request) map[string]any {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	items := splitItems(r.PostForm.Get("items"))
	back := or(r.PostForm.Get("back"), "/policy") + "#lists"
	if err := u.edit("list "+name, func(c *config.Config) error { return c.SetList(name, items) }); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, M("List “%s” saved", name))
}
