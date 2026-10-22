// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
)

// Конструктор правил: условия из блоков → CEL (спецификация, «Как
// редактируются»), описание правил человеческим языком и готовые шаблоны.

// Названия классов эффекта, решений, способов обращения и каналов (английские
// исходники; перевод — каталог ru).
var (
	classWords = map[string]string{"read": "read", "write": "change", "external": "send out", "money": "money"}
	// classVerbs — группа действий класса в мастере и на странице коннектора.
	classVerbs   = map[string]string{"read": "Read", "write": "Create and change", "external": "Send", "money": "Pay"}
	effectWords  = map[string]string{"allow": "allow", "ask": "ask me", "deny": "deny"}
	viaWords     = map[string]string{"mcp": "MCP (stdio)", "cli": "CLI", "mcp-http": "remote MCP", "https": "remote CLI", "agent-socket": "agent socket", "socket": "socket"}
	channelWords = map[string]string{"telegram": "Telegram", "tray": "tray", "webui": "Web UI", "webui-remote": "Web UI on the phone", "cli": "CLI",
		"desktop": "notification on this computer", "webpush": "browser push"}
)

// defaultEffect — решение без правил по классу (спецификация).
func defaultEffect(class string) string {
	if class == "read" {
		return config.EffectAllow
	}
	return config.EffectAsk
}

// argInfo — аргумент действия для блоков условия.
type argInfo struct {
	Name, Type, Format, Description string
	Array                           bool
}

func actionArgs(t daemon.Tool) []argInfo {
	var out []argInfo
	for _, p := range t.Schema.Properties {
		s := p.Schema
		a := argInfo{Name: p.Name, Type: s.Type, Format: s.Format, Description: s.Description}
		if s.Type == "array" && s.Items != nil {
			a.Array, a.Type, a.Format = true, s.Items.Type, s.Items.Format
		}
		if a.Type == "object" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// Виды блоков условия.
var condKinds = []struct{ ID, Title string }{
	{"in_list", "is in the list"},
	{"eq", "equals"},
	{"contains", "contains text"},
	{"in_folder", "is inside the folder"},
	{"domain", "is a site from the list"},
	{"lte", "is at most"},
	{"gte", "is at least"},
	{"untrusted", "came from an incoming email or a web page"},
}

// kindTitle — название вида условия для сообщений (переводится как Word).
func kindTitle(id string) Word {
	for _, k := range condKinds {
		if k.ID == id {
			return Word(k.Title)
		}
	}
	return Word(id)
}

// Cond — один блок условия формы.
type Cond struct {
	Kind, Arg, Value string
	Not              bool
	Items            string // «из списка»: элементы нового списка через запятую
}

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func celString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func splitItems(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ';' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// CEL — выражение блока; arr — аргумент-массив.
func (c Cond) CEL(arr bool) (string, error) {
	if !identRe.MatchString(c.Arg) {
		return "", M("Choose the argument for the check “%s”", kindTitle(c.Kind))
	}
	v := strings.TrimSpace(c.Value)
	need := func() error {
		if v == "" {
			return M("The condition on %s needs a value", c.Arg)
		}
		return nil
	}
	each := func(expr string) string { // expr от переменной «§»
		if arr {
			return c.Arg + ".all(x, " + strings.ReplaceAll(expr, "§", "x") + ")"
		}
		return strings.ReplaceAll(expr, "§", c.Arg)
	}
	var e string
	switch c.Kind {
	case "in_list":
		if !identRe.MatchString(v) {
			return "", M("List name %q: only lowercase Latin letters, digits and _", v)
		}
		e = each("§ in lists." + v)
	case "eq":
		if err := need(); err != nil {
			return "", err
		}
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			e = c.Arg + " == " + v
		} else {
			e = each("§ == " + celString(v))
		}
	case "contains":
		if err := need(); err != nil {
			return "", err
		}
		e = each("§.contains(" + celString(v) + ")")
	case "in_folder":
		if err := need(); err != nil {
			return "", err
		}
		e = each("glob(§, " + celString(strings.TrimSuffix(strings.TrimSuffix(v, "/**"), "/")+"/**") + ")")
	case "domain":
		items := splitItems(v)
		if len(items) == 0 {
			return "", M("List the sites for %s", c.Arg)
		}
		q := make([]string, len(items))
		for i, it := range items {
			q[i] = celString(it)
		}
		e = each("domain_matches(§, [" + strings.Join(q, ", ") + "])")
	case "lte", "gte":
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "", M("%s: %q is not a number", c.Arg, v)
		}
		op := map[string]string{"lte": " <= ", "gte": " >= "}[c.Kind]
		e = c.Arg + op + v
	case "untrusted":
		e = celString(c.Arg) + " in provenance && 'untrusted_content' in provenance[" + celString(c.Arg) + "]"
	default:
		return "", M("Unknown check %q", c.Kind)
	}
	if c.Not {
		return "!(" + e + ")", nil
	}
	return e, nil
}

// BuildWhen — условия «и» в одно выражение.
func BuildWhen(conds []Cond, args []argInfo) (string, error) {
	var parts []string
	for _, c := range conds {
		if c.Kind == "" {
			continue
		}
		arr := false
		for _, a := range args {
			if a.Name == c.Arg {
				arr = a.Array
			}
		}
		e, err := c.CEL(arr)
		if err != nil {
			return "", err
		}
		parts = append(parts, e)
	}
	for i, p := range parts {
		if strings.Contains(p, "||") {
			parts[i] = "(" + p + ")"
		}
	}
	return strings.Join(parts, " && "), nil
}

// ---- Человеческое описание условия ----

var describers = []struct {
	re *regexp.Regexp
	f  func(m []string) Msg
}{
	{regexp.MustCompile(`^(\w+)\.all\(\w+, \w+ in lists\.(\w+)\)$`), func(m []string) Msg { return M("every %s is in the list “%s”", m[1], m[2]) }},
	{regexp.MustCompile(`^(\w+) in lists\.(\w+)$`), func(m []string) Msg { return M("%s is in the list “%s”", m[1], m[2]) }},
	{regexp.MustCompile(`^glob\((\w+), "([^"]+)/\*\*"\)$`), func(m []string) Msg { return M("%s is inside the folder %s", m[1], m[2]) }},
	{regexp.MustCompile(`^!glob\((\w+), "([^"]+)/\*\*"\)$`), func(m []string) Msg { return M("%s is outside the folder %s", m[1], m[2]) }},
	{regexp.MustCompile(`^!\(glob\((\w+), "([^"]+)/\*\*"\)\)$`), func(m []string) Msg { return M("%s is outside the folder %s", m[1], m[2]) }},
	{regexp.MustCompile(`^domain_matches\((\w+), \[(.+)\]\)$`), func(m []string) Msg { return M("%s is a site from %s", m[1], strings.ReplaceAll(m[2], `"`, "")) }},
	{regexp.MustCompile(`^!\(?domain_matches\((\w+), \[(.+)\]\)\)?$`), func(m []string) Msg {
		return M("%s is a site not from %s", m[1], strings.ReplaceAll(m[2], `"`, ""))
	}},
	{regexp.MustCompile(`^(?:"(\w+)" in provenance && )?'untrusted_content' in provenance(?:\.(\w+)|\["(\w+)"\])$`), func(m []string) Msg {
		return M("%s came from an incoming email or a web page", firstNonEmpty(m[2], m[3], m[1]))
	}},
	{regexp.MustCompile(`^(\w+) <= ([\d.]+)$`), func(m []string) Msg { return M("%s is at most %s", m[1], m[2]) }},
	{regexp.MustCompile(`^(\w+) >= ([\d.]+)$`), func(m []string) Msg { return M("%s is at least %s", m[1], m[2]) }},
	{regexp.MustCompile(`^counters\.spent_today \+ amount > ([\d.]+)$`), func(m []string) Msg { return M("the day's total would exceed %s", m[1]) }},
}

var provGuard = regexp.MustCompile(`"\w+" in provenance && `)

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// DescribeWhen — условие словами по-английски; ok=false — показать как код.
func DescribeWhen(when string) (string, bool) { return describeWhen(langEN, when) }

// describeWhen — условие словами на языке l.
func describeWhen(l, when string) (string, bool) {
	when = strings.TrimSpace(when)
	if when == "" {
		return tr(l, "always"), true
	}
	// Проверка наличия ключа перед provenance["x"] — часть того же блока.
	when = provGuard.ReplaceAllString(when, "")
	var out []string
	for _, part := range strings.Split(when, " && ") {
		part = strings.TrimSpace(part)
		found := false
		for _, d := range describers {
			if m := d.re.FindStringSubmatch(part); m != nil {
				out = append(out, d.f(m).In(l))
				found = true
				break
			}
		}
		if !found {
			return when, false
		}
	}
	return strings.Join(out, tr(l, " and ")), true
}

// ---- Шаблоны ----

// TemplateParam — параметр шаблона, который спрашивает форма.
type TemplateParam struct {
	Name, Label, Placeholder, Help string
	Multiline                      bool
}

// Template — готовый набор правил.
type Template struct {
	ID, Title, Description string
	Needs                  []string // нужные действия (без них шаблон недоступен)
	Params                 []TemplateParam
	// Apply меняет конфигурацию для профиля; tools — каталог действий.
	Apply func(c *config.Config, profile string, p map[string]string, tools []daemon.Tool) error
}

// connectorsOf — коннекторы профиля, у которых есть действия в каталоге.
func actionsOf(c *config.Config, profile string, tools []daemon.Tool, keep func(t daemon.Tool) bool) []daemon.Tool {
	prof := c.Profiles[profile]
	var out []daemon.Tool
	for _, t := range tools {
		if prof.HasConnector(t.Connector) && keep(t) {
			out = append(out, t)
		}
	}
	return out
}

func putRule(c *config.Config, profile string, r config.Rule) error {
	if r.ID == "" {
		r.ID = c.Profiles[profile].UniqueRuleID("rule")
	}
	return c.PutRule(profile, r)
}

// Templates — набор шаблонов.
var Templates = []Template{
	{ID: "read-only", Title: "Read only", Description: "The agent reads but changes and sends nothing: every action except reading is denied.",
		Apply: func(c *config.Config, profile string, _ map[string]string, tools []daemon.Tool) error {
			acts := actionsOf(c, profile, tools, func(t daemon.Tool) bool { return t.Class != "read" })
			if len(acts) == 0 {
				return M("The agent has no actions besides reading")
			}
			for _, t := range acts {
				if err := putRule(c, profile, config.Rule{ID: "read-only-" + strings.ReplaceAll(t.Name, ".", "-"), Action: t.Name, Effect: config.EffectDeny, Reason: "Read only"}); err != nil {
					return err
				}
			}
			return nil
		}},
	{ID: "ask-all", Title: "Ask about everything", Description: "Every action of the agent, reading included, needs your approval.",
		Apply: func(c *config.Config, profile string, _ map[string]string, tools []daemon.Tool) error {
			for _, conn := range c.Profiles[profile].Connectors {
				if err := putRule(c, profile, config.Rule{ID: "ask-all-" + conn, Action: conn + ".*", Effect: config.EffectAsk, Reason: "Approve every action"}); err != nil {
					return err
				}
			}
			if len(c.Profiles[profile].Connectors) == 0 {
				return M("The agent has no connectors")
			}
			return nil
		}},
	{ID: "safe-mail", Title: "Safe email", Needs: []string{"email.send"},
		Description: "Emails to trusted recipients go without asking, recipients taken from incoming emails are denied, everything else asks you.",
		Params:      []TemplateParam{{Name: "trusted", Label: "Trusted recipients", Placeholder: "landlord@example.com, accountant@example.com", Multiline: true}},
		Apply: func(c *config.Config, profile string, p map[string]string, _ []daemon.Tool) error {
			if items := splitItems(p["trusted"]); len(items) > 0 {
				if err := c.SetList("contacts_trusted", items); err != nil {
					return err
				}
				if err := putRule(c, profile, config.Rule{ID: "email-send-trusted", Action: "email.send", When: "to.all(r, r in lists.contacts_trusted)", Effect: config.EffectAllow}); err != nil {
					return err
				}
			}
			return putRule(c, profile, config.Rule{ID: "email-no-untrusted-recipients", Action: "email.send",
				When: `"to" in provenance && 'untrusted_content' in provenance["to"]`, Effect: config.EffectDeny, Reason: "The recipient was taken from an incoming email"})
		}},
	{ID: "drafts-only", Title: "Drafts only", Needs: []string{"email.send"},
		Description: "The agent prepares email drafts but cannot send them.",
		Apply: func(c *config.Config, profile string, _ map[string]string, _ []daemon.Tool) error {
			return putRule(c, profile, config.Rule{ID: "no-send", Action: "email.send", Effect: config.EffectDeny, Reason: "Drafts only"})
		}},
	{ID: "notes-folder", Title: "Notes in a folder", Needs: []string{"files.read"},
		Description: "Reading and appending in the folder go without asking, overwriting asks you, everything outside the folder is denied. The folder is added to the Files settings.",
		Params:      []TemplateParam{{Name: "folder", Label: "Folder", Placeholder: "~/Documents/notes"}},
		Apply: func(c *config.Config, profile string, p map[string]string, tools []daemon.Tool) error {
			dir := strings.TrimSuffix(strings.TrimSpace(p["folder"]), "/")
			if dir == "" {
				return M("Choose a folder")
			}
			g := celString(dir + "/**")
			has := func(a string) bool {
				return slices.ContainsFunc(tools, func(t daemon.Tool) bool { return t.Name == a })
			}
			for _, a := range []string{"files.read", "files.list", "files.append"} {
				if has(a) {
					if err := putRule(c, profile, config.Rule{ID: "notes-" + strings.TrimPrefix(a, "files."), Action: a, When: "glob(path, " + g + ")", Effect: config.EffectAllow}); err != nil {
						return err
					}
				}
			}
			if err := putRule(c, profile, config.Rule{ID: "notes-only", Action: "files.*", When: "!glob(path, " + g + ")", Effect: config.EffectDeny, Reason: "Only the folder " + dir}); err != nil {
				return err
			}
			s := map[string]any{}
			maps.Copy(s, c.Connectors["files"])
			roots := toStrings(s["roots"])
			if !slices.Contains(roots, dir) {
				roots = append(roots, dir)
			}
			s["roots"] = roots
			c.SetConnectorSettings("files", s)
			return c.SetProfileConnector(profile, "files", true)
		}},
	{ID: "sites-list", Title: "Sites from a list", Needs: []string{"browser.read"},
		Description: "The browser opens only the sites from the list. The list also goes to the Browser settings.",
		Params:      []TemplateParam{{Name: "sites", Label: "Sites", Placeholder: "*.example.com, app.example.org", Multiline: true}},
		Apply: func(c *config.Config, profile string, p map[string]string, _ []daemon.Tool) error {
			items := splitItems(p["sites"])
			if len(items) == 0 {
				return M("List the sites")
			}
			q := make([]string, len(items))
			for i, it := range items {
				q[i] = celString(it)
			}
			if err := putRule(c, profile, config.Rule{ID: "sites-only", Action: "browser.*", When: "!domain_matches(url, [" + strings.Join(q, ", ") + "])", Effect: config.EffectDeny, Reason: "The site is not in the list"}); err != nil {
				return err
			}
			s := map[string]any{}
			maps.Copy(s, c.Connectors["browser"])
			s["allowed_domains"] = items
			c.SetConnectorSettings("browser", s)
			return nil
		}},
	{ID: "daily-limit", Title: "At most N actions a day", Description: "Limit changes and sends per day for each connector of the agent.",
		Params: []TemplateParam{{Name: "n", Label: "Actions a day", Placeholder: "20"}},
		Apply: func(c *config.Config, profile string, p map[string]string, tools []daemon.Tool) error {
			n, err := strconv.Atoi(strings.TrimSpace(p["n"]))
			if err != nil || n <= 0 {
				return M("Enter a positive number")
			}
			acts := actionsOf(c, profile, tools, func(t daemon.Tool) bool { return t.Class != "read" })
			if len(acts) == 0 {
				return M("The agent has no actions besides reading")
			}
			for _, t := range acts {
				if err := c.PutLimit(profile, config.Limit{ID: "per-day-" + strings.ReplaceAll(t.Name, ".", "-"), Action: t.Name, PerDay: n,
					Reason: fmt.Sprintf("At most %d a day", n)}); err != nil {
					return err
				}
			}
			return nil
		}},
	{ID: "no-self-config", Title: "Agents may not change gate4.ai settings", Description: "Deny gate4ai.change_policy: the agent cannot change rules, lists and connectors even with your approval.",
		Apply: func(c *config.Config, profile string, _ map[string]string, _ []daemon.Tool) error {
			return putRule(c, profile, config.Rule{ID: "no-self-config", Action: daemon.SelfConnector + ".change_policy", Effect: config.EffectDeny, Reason: "Only the owner changes gate4.ai settings"})
		}},
	{ID: "money-ask", Title: "Money always asks", Description: "Every money action needs your approval, even if another rule allows it.",
		Apply: func(c *config.Config, profile string, _ map[string]string, tools []daemon.Tool) error {
			acts := actionsOf(c, profile, tools, func(t daemon.Tool) bool { return t.Class == "money" })
			if len(acts) == 0 {
				return M("The agent has no payment connectors yet: install one first")
			}
			for _, t := range acts {
				if err := putRule(c, profile, config.Rule{ID: "money-ask-" + strings.ReplaceAll(t.Name, ".", "-"), Action: t.Name, Effect: config.EffectAsk, Reason: "Money needs approval"}); err != nil {
					return err
				}
			}
			return nil
		}},
}

func findTemplate(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case string:
		if x != "" {
			return []string{x}
		}
	}
	return nil
}

// toolSchema — схема аргументов действия (для формы симулятора).
func toolSchema(tools []daemon.Tool, name string) (daemon.Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return daemon.Tool{}, false
}

// argsFromForm — аргументы действия из полей формы arg.<имя>.
func argsFromForm(t daemon.Tool, get func(string) string) (map[string]any, error) {
	args := map[string]any{}
	for _, p := range t.Schema.Properties {
		v := strings.TrimSpace(get("arg." + p.Name))
		if v == "" {
			continue
		}
		s := p.Schema
		switch {
		case s.Type == "array" && s.Items != nil && (s.Items.Type == "string" || s.Items.Type == ""):
			args[p.Name] = anyStrings(splitItems(v))
		case s.Type == "number" || s.Type == "integer":
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %q is not a number", p.Name, v)
			}
			args[p.Name] = f
		case s.Type == "boolean":
			args[p.Name] = v == "true" || v == "on" || v == "1"
		case s.Type == "object" || s.Type == "array":
			var x any
			if err := json.Unmarshal([]byte(v), &x); err != nil {
				return nil, fmt.Errorf("%s: JSON expected: %v", p.Name, err)
			}
			args[p.Name] = x
		default:
			args[p.Name] = v
		}
	}
	return args, nil
}

func anyStrings(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}
