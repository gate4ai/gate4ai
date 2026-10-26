// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
)

// Ветка входа в записи сеанса: шаги от страницы входа (login_required или
// страница на origin входа) до ухода с неё. Из неё делается отдельный
// коннектор входа (действие login {account} на observe / act / fill_secret), а
// в записи основного коннектора её заменяет один шаг login по слоту — как
// хост и записывает вход учётки с via.

// LoginBranch — шаги входа и где он произошёл.
type LoginBranch struct {
	Origin string          // origin страницы входа (https://pass.rzd.ru)
	Steps  []hosttest.Step // шаги на ней (без close)
	// Fields — что агент вводил на странице входа: поля коннектора входа
	// (fill_secret по имени; значения в коннектор не попадают).
	Fields []LoginField
	// Success — адрес после входа (куда ушла страница).
	Success string
}

// LoginField — поле формы входа: имя для fill_secret и селектор.
type LoginField struct {
	Name     string `json:"name"`
	Selector string `json:"selector"`
	Secret   bool   `json:"secret"` // похоже на пароль: только input[type=password]
}

// SplitLogin отделяет ветку входа от шагов задачи. origin пустой — берётся из
// первого login_required или первого ухода страницы на другой origin, где
// агент что-то вводил. Возвращает шаги задачи (ветка заменена шагом login по
// слоту site) и ветку; ветки нет — nil.
func SplitLogin(steps []hosttest.Step, origin string) ([]hosttest.Step, *LoginBranch) {
	// Вход уже делал хост (browser.login учётки): ветки, из которой делать
	// коннектор входа, в записи нет.
	if slices.ContainsFunc(steps, func(st hosttest.Step) bool { return st.Action == "login" && st.Via == "" }) {
		return steps, nil
	}
	origin = domain.OriginOf(origin)
	if origin == "" {
		origin = guessLoginOrigin(steps)
	}
	if origin == "" {
		return steps, nil
	}
	var main []hosttest.Step
	br := &LoginBranch{Origin: origin}
	in := false
	for _, st := range steps {
		if st.Via != "" || st.Action == "close" {
			if st.Action == "close" {
				main = append(main, st)
			}
			continue
		}
		u := stepURL(st)
		onLogin := u != "" && domain.OriginOf(u) == origin
		if st.Action == "login" {
			// Вход уже шёл учёткой: ветка — то, что было до него на сайте входа.
			in = false
			main = append(main, st)
			continue
		}
		switch {
		case !in && onLogin && st.Action != "open":
			in = true
			br.Steps = append(br.Steps, st)
		case !in && onLogin && st.Action == "open" && domain.OriginOf(stepArgURL(st)) == origin:
			in = true
			br.Steps = append(br.Steps, st)
		case in && (onLogin || u == ""):
			br.Steps = append(br.Steps, st)
		case in && !onLogin:
			// Ушли со страницы входа: вход состоялся. Клик или клавиша, после
			// которых страница ушла, — отправка формы, часть входа; переход
			// агента на другой адрес — уже задача.
			in = false
			br.Success = u
			if st.Action == "act" {
				br.Steps = append(br.Steps, st)
			}
			main = append(main, hosttest.Step{Action: "login", Args: map[string]any{"account": "site"}, Result: map[string]any{"status": "logged_in", "url": u, "origin": origin}})
			if st.Action != "act" {
				main = append(main, st)
			}
		default:
			main = append(main, st)
		}
	}
	if len(br.Steps) == 0 {
		return steps, nil
	}
	if in {
		main = append(main, hosttest.Step{Action: "login", Args: map[string]any{"account": "site"}, Result: map[string]any{"status": "submitted", "origin": origin}})
	}
	for i, st := range br.Steps {
		if st.Action != "act" {
			continue
		}
		v, _ := st.Args["value"].(string)
		if v == "" {
			continue
		}
		sel, _ := st.Args["selector"].(string)
		f := LoginField{Name: fieldName(sel, len(br.Fields)), Selector: sel, Secret: looksSecret(sel)}
		br.Fields = append(br.Fields, f)
		_ = i
	}
	return main, br
}

// guessLoginOrigin — origin входа: из login_required первого open/observe, иначе
// origin, на который страница ушла после open и где агент вводил значение.
func guessLoginOrigin(steps []hosttest.Step) string {
	for _, st := range steps {
		if lr, ok := st.Result["login_required"].(map[string]any); ok && lr != nil {
			if u := stepURL(st); u != "" {
				return domain.OriginOf(u)
			}
		}
	}
	first := ""
	for _, st := range steps {
		if st.Action == "open" {
			if u := stepArgURL(st); first == "" && u != "" {
				first = domain.OriginOf(u)
			}
		}
		if st.Action == "act" {
			if v, _ := st.Args["value"].(string); v != "" {
				if o := domain.OriginOf(stepURL(st)); o != "" && o != first && looksSecret(fmt.Sprint(st.Args["selector"])) {
					return o
				}
			}
		}
	}
	return ""
}

func stepURL(st hosttest.Step) string {
	u, _ := st.Result["url"].(string)
	return u
}

func stepArgURL(st hosttest.Step) string {
	u, _ := st.Args["url"].(string)
	return u
}

// looksSecret — селектор поля похож на пароль.
func looksSecret(sel string) bool {
	l := strings.ToLower(sel)
	return strings.Contains(l, "pass") || strings.Contains(l, "pwd") || strings.Contains(l, "type=password") || strings.Contains(l, "secret")
}

// fieldName — имя поля учётки по селектору: #login → login, #password → password.
func fieldName(sel string, n int) string {
	l := strings.ToLower(sel)
	switch {
	case looksSecret(l):
		return "password"
	case strings.Contains(l, "login") || strings.Contains(l, "user") || strings.Contains(l, "email") || strings.Contains(l, "phone"):
		return "login"
	case strings.Contains(l, "code") || strings.Contains(l, "otp") || strings.Contains(l, "sms"):
		return "code"
	}
	return fmt.Sprintf("field_%d", n+1)
}

// LoginDraft — черновик коннектора входа из ветки.
type LoginDraft struct {
	Connector string
	Branch    LoginBranch
}

// Calls — подвызовы браузера действия login.
func (d LoginDraft) Calls() []string {
	calls := []string{"browser.observe"}
	for _, st := range d.Branch.Steps {
		switch st.Action {
		case "act":
			if v, _ := st.Args["value"].(string); v != "" {
				if !slices.Contains(calls, "browser.fill_secret") {
					calls = append(calls, "browser.fill_secret")
				}
				continue
			}
			if !slices.Contains(calls, "browser.act") {
				calls = append(calls, "browser.act")
			}
		case "open":
			if !slices.Contains(calls, "browser.open") {
				calls = append(calls, "browser.open")
			}
		}
	}
	slices.Sort(calls)
	return calls
}

// FieldNames — поля, которые коннектор входа вводит сам (манифест: credentials.fields).
func (d LoginDraft) FieldNames() []string {
	var out []string
	for _, f := range d.Branch.Fields {
		if !slices.Contains(out, f.Name) {
			out = append(out, f.Name)
		}
	}
	return out
}

// GoCase — тело действия login: шаги входа как подвызовы, значения — fill_secret
// по имени поля учётки (значений коннектор не видит).
func (d LoginDraft) GoCase() string {
	var b strings.Builder
	b.WriteString("\t\taccount, _ := r.Args[\"account\"].(string)\n")
	fi := 0
	for _, st := range d.Branch.Steps {
		switch st.Action {
		case "open":
			fmt.Fprintf(&b, "\t\tif _, err := h.ServiceCall(\"browser\", \"open\", %s, nil); err != nil {\n\t\t\treturn abi.ExecResult{}, err\n\t\t}\n", goMap(st.Args, nil))
		case "observe":
			fmt.Fprintf(&b, "\t\tif _, err := h.ServiceCall(\"browser\", \"observe\", %s, nil); err != nil {\n\t\t\treturn abi.ExecResult{}, err\n\t\t}\n", goMap(st.Args, nil))
		case "act":
			if v, _ := st.Args["value"].(string); v != "" && fi < len(d.Branch.Fields) {
				f := d.Branch.Fields[fi]
				fi++
				fmt.Fprintf(&b, "\t\tif _, err := h.ServiceCall(\"browser\", \"fill_secret\", map[string]any{\"account\": account, \"field\": %q, \"selector\": %q}, nil); err != nil {\n\t\t\treturn abi.ExecResult{}, err\n\t\t}\n", f.Name, f.Selector)
				continue
			}
			fmt.Fprintf(&b, "\t\tif _, err := h.ServiceCall(\"browser\", \"act\", %s, nil); err != nil {\n\t\t\treturn abi.ExecResult{}, err\n\t\t}\n", goMap(st.Args, nil))
		}
	}
	b.WriteString("\t\tvar p page\n\t\tif _, err := h.ServiceCall(\"browser\", \"observe\", map[string]any{\"max_chars\": 200}, &p); err != nil {\n\t\t\treturn abi.ExecResult{}, err\n\t\t}\n")
	b.WriteString("\t\tstatus := \"submitted\"\n\t\tif !strings.HasPrefix(p.URL, " + fmt.Sprintf("%q", d.Branch.Origin) + ") {\n\t\t\tstatus = \"logged_in\"\n\t\t}\n")
	b.WriteString("\t\treturn abi.ExecResult{Structured: map[string]any{\"status\": status, \"url\": p.URL}}, nil\n")
	return b.String()
}

// Fixture — шаги ветки для теста коннектора входа: ввод значений заменён на
// fill_secret без значений.
func (d LoginDraft) Fixture() hosttest.Fixture {
	var out []hosttest.Step
	fi := 0
	for _, st := range d.Branch.Steps {
		if st.Action == "act" {
			if v, _ := st.Args["value"].(string); v != "" && fi < len(d.Branch.Fields) {
				f := d.Branch.Fields[fi]
				fi++
				out = append(out, hosttest.Step{Action: "fill_secret", Args: map[string]any{"account": "@test", "field": f.Name, "selector": f.Selector},
					Result: map[string]any{"url": stepURL(st), "origin": d.Branch.Origin}})
				continue
			}
		}
		out = append(out, st)
	}
	out = append(out, hosttest.Step{Action: "observe", Args: map[string]any{"max_chars": float64(200)}, Result: map[string]any{"url": firstNonEmpty(d.Branch.Success, d.Branch.Origin+"/"), "title": "", "text": ""}})
	return hosttest.Fixture{Steps: out}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Body — тело RECIPE.md коннектора входа.
func (d LoginDraft) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## login\n\nSign-in on `%s`, recorded from a browser session. The connector never sees the values: it names the fields of the owner's account (fill_secret), gate4ai types them in.\n\n", d.Branch.Origin)
	n := 0
	for _, st := range d.Branch.Steps {
		if line := describeStep(st, nil); line != "" {
			n++
			fmt.Fprintf(&b, "%d. %s\n", n, line)
		}
	}
	if len(d.Branch.Fields) > 0 {
		b.WriteString("\n### Fields of the account\n\n")
		for _, f := range d.Branch.Fields {
			kind := "text"
			if f.Secret {
				kind = "secret (input[type=password])"
			}
			fmt.Fprintf(&b, "- `%s` → `%s` (%s)\n", f.Name, f.Selector, kind)
		}
	}
	b.WriteString("\n### Pitfalls\n\n- (sign-in changes often: captcha, a second step, «confirm the device» — note what you saw)\n")
	return b.String()
}
