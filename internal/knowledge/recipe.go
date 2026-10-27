// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
)

// Draft — черновик знаний о сайте из записи сеанса браузера (steps_file
// browser.close, уже после fixture scrub): шаги для рецепта, подвызовы и класс
// действия для манифеста, фикстура для тестов и код действия для коннектора.
type Draft struct {
	Connector string
	Task      string
	Site      string
	Domains   []string
	// Steps — шаги сеанса, которые воспроизводит коннектор (без шагов человека,
	// неудачных и служебных); Inputs — значения, которые агент вводил на
	// страницах: аргументы действия (input_1, input_2, …).
	Steps  []hosttest.Step
	Inputs []Input
	// Pitfalls — что пошло не так в сеансе (ошибки шагов): раздел «Ловушки».
	Pitfalls []string
	// Human — шаги человека: вход с кодом из SMS, handoff (оплата, капча).
	Human []string
	Login *RecipeLogin
	Class string   // read | write
	Calls []string // browser.open, browser.act, …
	// Notes — что в коннектор не попало (запросы агента к API, network).
	Notes []string
}

// Input — значение, введённое в поле страницы: становится аргументом действия.
type Input struct {
	Name     string `json:"name"`     // input_1
	Selector string `json:"selector"` // куда вводилось
	Value    string `json:"value"`    // записанное (очищенное) значение — пример
	Step     int    // индекс в Steps
}

// agentOnly — действия сеанса, которых у коннектора нет (сеть — только у агента).
var agentOnly = map[string]bool{"network": true, "get": true, "head": true, "post": true, "put": true, "patch": true, "delete": true, "read": true, "screenshot": true}

// writeActions — подвызовы класса write.
var writeActions = map[string]bool{"act": true, "login": true, "fill_secret": true, "download": true, "dialog": true}

// FromSteps разбирает запись сеанса: что делал агент, что — человек, что
// сломалось. site пустой — хост первого open.
func FromSteps(steps []hosttest.Step, connector, task, site string) Draft {
	d := Draft{Connector: connector, Task: task, Site: strings.ToLower(site), Class: "read"}
	seen := map[string]bool{}
	addDomain := func(raw string) {
		h := domain.HostOf(raw)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		d.Domains = append(d.Domains, h)
		if d.Site == "" {
			d.Site = h
		}
	}
	lastURL := ""
	for _, st := range steps {
		if st.Via != "" {
			continue // шаги коннектора входа: его дело
		}
		if u, _ := st.Args["url"].(string); u != "" && !agentOnly[st.Action] {
			addDomain(u)
		}
		if u, _ := st.Result["url"].(string); u != "" {
			lastURL = u
			if !agentOnly[st.Action] {
				addDomain(u)
			}
		}
		if st.Error != nil {
			d.Pitfalls = append(d.Pitfalls, fmt.Sprintf("`%s` %s → %s: %s", st.Action, briefArgs(st.Args), st.Error.Code, st.Error.Message))
			continue
		}
		switch st.Action {
		case "close":
			continue
		case "handoff":
			msg, _ := st.Args["message"].(string)
			d.Human = append(d.Human, "handoff: "+msg)
			continue
		case "login":
			acc, _ := st.Args["account"].(string)
			status, _ := st.Result["status"].(string)
			if d.Login == nil {
				d.Login = &RecipeLogin{Kind: "form"}
				if o := domain.OriginOf(lastURL); o != "" {
					d.Login.Bind = o
				}
			}
			if status == "still_on_login" {
				d.Login.Kind = "form+sms"
				d.Human = append(d.Human, "sms_code")
			}
			_ = acc
		}
		if agentOnly[st.Action] {
			d.Notes = append(d.Notes, fmt.Sprintf("`%s` %s — a session-only action: connectors have no network, use the page instead", st.Action, briefArgs(st.Args)))
			continue
		}
		if lr, ok := st.Result["login_required"].(map[string]any); ok && d.Login == nil {
			d.Login = &RecipeLogin{Kind: "form"}
			if acc, _ := lr["account"].(string); acc != "" {
				d.Login.Kind = "form"
			}
			if u, _ := st.Result["url"].(string); u != "" {
				d.Login.Bind = domain.OriginOf(u)
			}
		}
		if writeActions[st.Action] {
			d.Class = "write"
		}
		call := "browser." + st.Action
		if !slices.Contains(d.Calls, call) {
			d.Calls = append(d.Calls, call)
		}
		if st.Action == "act" {
			if v, ok := st.Args["value"].(string); ok && v != "" {
				in := Input{Name: fmt.Sprintf("input_%d", len(d.Inputs)+1), Value: v, Step: len(d.Steps)}
				in.Selector, _ = st.Args["selector"].(string)
				d.Inputs = append(d.Inputs, in)
			}
		}
		d.Steps = append(d.Steps, st)
	}
	if d.Login != nil && !slices.Contains(d.Calls, "browser.login") {
		d.Calls = append(d.Calls, "browser.login")
		d.Class = "write"
	}
	sort.Strings(d.Calls)
	return d
}

// briefArgs — аргументы шага одной строкой, длинные значения обрезаны.
func briefArgs(a map[string]any) string {
	if len(a) == 0 {
		return ""
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(a)) {
		v := fmt.Sprint(a[k])
		if r := []rune(v); len(r) > 60 {
			v = string(r[:60]) + "…"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

// Recipe — шапка RECIPE.md черновика.
func (d Draft) Recipe(version string, now time.Time) *Recipe {
	r := &Recipe{Site: d.Site, Domains: d.Domains, Login: d.Login,
		Tasks: []Task{{ID: d.Task, Title: strings.ReplaceAll(d.Task, "_", " "), Class: d.Class, Status: StatusDraft, LastVerified: now.Format("2006-01-02"), Gate4ai: version}}}
	for _, h := range d.Human {
		k, _, _ := strings.Cut(h, ":")
		if !slices.Contains(r.HumanSteps, k) {
			r.HumanSteps = append(r.HumanSteps, k)
		}
	}
	return r
}

// Body — тело RECIPE.md: шаги человеческим языком, что делает человек, ловушки.
func (d Draft) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", d.Task)
	b.WriteString("Recorded from a browser session; personal data replaced (gate4ai fixture scrub). Numbered steps are what the connector replays.\n\n")
	n := 0
	for i, st := range d.Steps {
		line := describeStep(st, d.inputAt(i))
		if line == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d. %s\n", n, line)
	}
	if len(d.Inputs) > 0 {
		b.WriteString("\n### Arguments\n\n")
		for _, in := range d.Inputs {
			fmt.Fprintf(&b, "- `%s` — typed into `%s` (recorded example: %q)\n", in.Name, in.Selector, in.Value)
		}
	}
	if len(d.Human) > 0 {
		b.WriteString("\n### The human does\n\n")
		for _, h := range d.Human {
			switch {
			case h == "sms_code":
				b.WriteString("- Enters the code from SMS in the live view of the session (browser.login answers still_on_login).\n")
			case strings.HasPrefix(h, "handoff:"):
				fmt.Fprintf(&b, "- Takes over the page (browser.handoff): %s\n", strings.TrimSpace(strings.TrimPrefix(h, "handoff:")))
			default:
				fmt.Fprintf(&b, "- %s\n", h)
			}
		}
	}
	if d.Login != nil {
		b.WriteString("\n### Sign-in\n\n")
		fmt.Fprintf(&b, "- Sign-in page: `%s`; kind: %s. The connector calls browser.login by its credential slot `site` and never sees the password.\n", d.Login.Bind, d.Login.Kind)
	}
	b.WriteString("\n### Pitfalls\n\n")
	if len(d.Pitfalls) == 0 {
		b.WriteString("- (none recorded — add what broke and how you got around it)\n")
	}
	for _, p := range d.Pitfalls {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	if len(d.Notes) > 0 {
		b.WriteString("\n### Not in the connector\n\n")
		for _, nn := range d.Notes {
			fmt.Fprintf(&b, "- %s\n", nn)
		}
	}
	return b.String()
}

// inputAt — поле ввода шага i, если есть.
func (d Draft) inputAt(i int) *Input {
	for j := range d.Inputs {
		if d.Inputs[j].Step == i {
			return &d.Inputs[j]
		}
	}
	return nil
}

// describeStep — шаг словами для рецепта; "" — не описывается.
func describeStep(st hosttest.Step, in *Input) string {
	a := st.Args
	s := func(k string) string { v, _ := a[k].(string); return v }
	wait := ""
	for _, k := range []string{"wait_for_text", "wait_for_selector", "wait_for_url", "expect_text", "expect_selector"} {
		if v := s(k); v != "" {
			wait = fmt.Sprintf("; wait for %s `%s`", strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(k, "wait_for_"), "expect_"), "_", " "), v)
			break
		}
	}
	if b, _ := a["expect_url_change"].(bool); b && wait == "" {
		wait = "; wait for the address to change"
	}
	switch st.Action {
	case "open":
		if u := s("url"); u != "" {
			return fmt.Sprintf("Open `%s`%s.", u, wait)
		}
		return ""
	case "observe":
		if sel := s("selector"); sel != "" {
			return fmt.Sprintf("Read the text of `%s`.", sel)
		}
		return ""
	case "act":
		target := "`" + s("selector") + "`"
		if t := s("text"); t != "" {
			target = fmt.Sprintf("«%s» (`%s`)", t, s("selector"))
		}
		switch m := s("method"); m {
		case "", "click":
			return fmt.Sprintf("Click %s%s.", target, wait)
		case "press":
			return fmt.Sprintf("Press %s in %s%s.", s("key"), target, wait)
		default:
			if in != nil {
				return fmt.Sprintf("Type `{%s}` into %s%s.", in.Name, target, wait)
			}
			return fmt.Sprintf("%s into %s%s.", strings.ToUpper(m[:1])+m[1:], target, wait)
		}
	case "login":
		return "Sign in with the owner's account (browser.login by the `site` slot)."
	case "download":
		return fmt.Sprintf("Download the file behind `%s`.", s("selector"))
	case "dialog":
		return "Answer the page dialog."
	case "page_screenshot":
		return "Take a screenshot of the page."
	case "reload":
		return "Reload the page" + wait + "."
	}
	return ""
}

// Fixture — шаги для hosttest.Replay: то, что воспроизводит коннектор.
func (d Draft) Fixture() hosttest.Fixture { return hosttest.Fixture{Steps: d.Steps} }

// GoCase — тело case действия в Execute коннектора: подвызовы браузера по шагам.
func (d Draft) GoCase() string {
	var b strings.Builder
	lastObserve := -1
	for i, st := range d.Steps {
		if st.Action == "observe" {
			lastObserve = i
		}
	}
	for i, st := range d.Steps {
		args := maps.Clone(st.Args)
		if args == nil {
			args = map[string]any{}
		}
		in := d.inputAt(i)
		switch st.Action {
		case "login":
			args = map[string]any{"account": "site"}
		case "observe":
			if i == lastObserve {
				fmt.Fprintf(&b, "\t\tvar p page\n\t\tif _, err := h.ServiceCall(\"browser\", \"observe\", %s, &p); err != nil {\n\t\t\treturn abi.ExecResult{}, err\n\t\t}\n", goMap(args, in))
				continue
			}
		}
		fmt.Fprintf(&b, "\t\tif _, err := h.ServiceCall(\"browser\", %q, %s, nil); err != nil {\n\t\t\treturn abi.ExecResult{}, err\n\t\t}\n", st.Action, goMap(args, in))
	}
	if lastObserve >= 0 {
		b.WriteString("\t\treturn abi.ExecResult{Structured: map[string]any{\"title\": p.Title, \"text\": p.Text}, Source: \"web:\" + p.URL}, nil\n")
	} else {
		b.WriteString("\t\treturn abi.ExecResult{Structured: map[string]any{\"done\": true}}, nil\n")
	}
	return b.String()
}

// goMap — литерал map[string]any{…} для аргументов подвызова; значение поля
// ввода — из аргументов действия.
func goMap(a map[string]any, in *Input) string {
	if len(a) == 0 {
		return "nil"
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(a)) {
		v := a[k]
		if in != nil && k == "value" {
			parts = append(parts, fmt.Sprintf("%q: r.Args[%q]", k, in.Name))
			continue
		}
		parts = append(parts, fmt.Sprintf("%q: %s", k, goLit(v)))
	}
	return "map[string]any{" + strings.Join(parts, ", ") + "}"
}

func goLit(v any) string {
	switch t := v.(type) {
	case string:
		return fmt.Sprintf("%q", t)
	case bool:
		return fmt.Sprint(t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprint(int64(t))
		}
		return fmt.Sprint(t)
	case int, int64:
		return fmt.Sprint(t)
	}
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("json.RawMessage(%q)", raw)
}

// InputSchema — схема аргументов действия: поля ввода из сеанса.
func (d Draft) InputSchema() string {
	if len(d.Inputs) == 0 {
		return "{ type: object, properties: {} }"
	}
	var props, req []string
	for _, in := range d.Inputs {
		props = append(props, fmt.Sprintf("%s: { type: string, description: %q }", in.Name, "typed into "+in.Selector+" (recorded: "+in.Value+")"))
		req = append(req, in.Name)
	}
	return "{ type: object, properties: { " + strings.Join(props, ", ") + " }, required: [" + strings.Join(req, ", ") + "] }"
}

// ExampleArgs — аргументы для теста: записанные значения полей ввода.
func (d Draft) ExampleArgs() map[string]any {
	if len(d.Inputs) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, in := range d.Inputs {
		out[in.Name] = in.Value
	}
	return out
}
