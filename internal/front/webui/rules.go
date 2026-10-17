// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
)

// Конструктор правил: условия из блоков → CEL (спецификация, «Как
// редактируются»), описание правил человеческим языком и готовые шаблоны.

// Названия классов эффекта и решений.
var (
	classRu  = map[string]string{"read": "чтение", "write": "изменение", "external": "отправка наружу", "money": "деньги"}
	effectRu = map[string]string{"allow": "разрешать", "ask": "спрашивать", "deny": "запрещать"}
	viaRu    = map[string]string{"mcp": "MCP (stdio)", "cli": "CLI", "mcp-http": "remote MCP", "https": "удалённый CLI", "agent-socket": "сокет агентов", "socket": "сокет"}
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
	{"in_list", "из списка"},
	{"eq", "равно"},
	{"contains", "содержит текст"},
	{"in_folder", "внутри папки"},
	{"domain", "сайт из списка"},
	{"lte", "не больше"},
	{"gte", "не меньше"},
	{"untrusted", "взято из входящего письма или страницы"},
}

// Cond — один блок условия формы.
type Cond struct {
	Kind, Arg, Value string
	Not              bool
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
		return "", fmt.Errorf("choose an argument for «%s»", c.Kind)
	}
	v := strings.TrimSpace(c.Value)
	need := func() error {
		if v == "" {
			return fmt.Errorf("condition on %s needs a value", c.Arg)
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
			return "", fmt.Errorf("list name %q: use lowercase letters, digits and _", v)
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
			return "", fmt.Errorf("list the sites for %s", c.Arg)
		}
		q := make([]string, len(items))
		for i, it := range items {
			q[i] = celString(it)
		}
		e = each("domain_matches(§, [" + strings.Join(q, ", ") + "])")
	case "lte", "gte":
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "", fmt.Errorf("%s: %q is not a number", c.Arg, v)
		}
		op := map[string]string{"lte": " <= ", "gte": " >= "}[c.Kind]
		e = c.Arg + op + v
	case "untrusted":
		e = celString(c.Arg) + " in provenance && 'untrusted_content' in provenance[" + celString(c.Arg) + "]"
	default:
		return "", fmt.Errorf("unknown condition %q", c.Kind)
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
	f  func(m []string) string
}{
	{regexp.MustCompile(`^(\w+)\.all\(\w+, \w+ in lists\.(\w+)\)$`), func(m []string) string { return "все " + m[1] + " — из списка «" + m[2] + "»" }},
	{regexp.MustCompile(`^(\w+) in lists\.(\w+)$`), func(m []string) string { return m[1] + " — из списка «" + m[2] + "»" }},
	{regexp.MustCompile(`^glob\((\w+), "([^"]+)/\*\*"\)$`), func(m []string) string { return m[1] + " внутри папки " + m[2] }},
	{regexp.MustCompile(`^!glob\((\w+), "([^"]+)/\*\*"\)$`), func(m []string) string { return m[1] + " вне папки " + m[2] }},
	{regexp.MustCompile(`^!\(glob\((\w+), "([^"]+)/\*\*"\)\)$`), func(m []string) string { return m[1] + " вне папки " + m[2] }},
	{regexp.MustCompile(`^domain_matches\((\w+), \[(.+)\]\)$`), func(m []string) string { return m[1] + " — сайт из " + strings.ReplaceAll(m[2], `"`, "") }},
	{regexp.MustCompile(`^!\(?domain_matches\((\w+), \[(.+)\]\)\)?$`), func(m []string) string { return m[1] + " — сайт не из " + strings.ReplaceAll(m[2], `"`, "") }},
	{regexp.MustCompile(`^(?:"(\w+)" in provenance && )?'untrusted_content' in provenance(?:\.(\w+)|\["(\w+)"\])$`), func(m []string) string {
		return firstNonEmpty(m[2], m[3], m[1]) + " взят из входящего письма или страницы"
	}},
	{regexp.MustCompile(`^(\w+) <= ([\d.]+)$`), func(m []string) string { return m[1] + " не больше " + m[2] }},
	{regexp.MustCompile(`^(\w+) >= ([\d.]+)$`), func(m []string) string { return m[1] + " не меньше " + m[2] }},
	{regexp.MustCompile(`^counters\.spent_today \+ amount > ([\d.]+)$`), func(m []string) string { return "сумма за день превысит " + m[1] }},
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

// DescribeWhen — условие словами; ok=false — показать как код.
func DescribeWhen(when string) (string, bool) {
	when = strings.TrimSpace(when)
	if when == "" {
		return "всегда", true
	}
	// Проверка наличия ключа перед provenance["x"] — часть того же блока.
	when = provGuard.ReplaceAllString(when, "")
	var out []string
	for _, part := range strings.Split(when, " && ") {
		part = strings.TrimSpace(part)
		found := false
		for _, d := range describers {
			if m := d.re.FindStringSubmatch(part); m != nil {
				out = append(out, d.f(m))
				found = true
				break
			}
		}
		if !found {
			return when, false
		}
	}
	return strings.Join(out, " и "), true
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
	{ID: "read-only", Title: "Только чтение", Description: "Агент читает, но ничего не меняет и не отправляет: все действия, кроме чтения, запрещены.",
		Apply: func(c *config.Config, profile string, _ map[string]string, tools []daemon.Tool) error {
			acts := actionsOf(c, profile, tools, func(t daemon.Tool) bool { return t.Class != "read" })
			if len(acts) == 0 {
				return fmt.Errorf("profile %s has no actions besides reading", profile)
			}
			for _, t := range acts {
				if err := putRule(c, profile, config.Rule{ID: "read-only-" + strings.ReplaceAll(t.Name, ".", "-"), Action: t.Name, Effect: config.EffectDeny, Reason: "Только чтение"}); err != nil {
					return err
				}
			}
			return nil
		}},
	{ID: "ask-all", Title: "Спрашивать обо всём", Description: "Каждое действие агента, включая чтение, — только с вашего подтверждения.",
		Apply: func(c *config.Config, profile string, _ map[string]string, tools []daemon.Tool) error {
			for _, conn := range c.Profiles[profile].Connectors {
				if err := putRule(c, profile, config.Rule{ID: "ask-all-" + conn, Action: conn + ".*", Effect: config.EffectAsk, Reason: "Подтверждать каждое действие"}); err != nil {
					return err
				}
			}
			if len(c.Profiles[profile].Connectors) == 0 {
				return fmt.Errorf("profile %s has no connectors", profile)
			}
			return nil
		}},
	{ID: "safe-mail", Title: "Безопасная почта", Needs: []string{"email.send"},
		Description: "Письма доверенным адресатам уходят без вопроса, адресаты из входящих писем запрещены, остальное — с подтверждением.",
		Params:      []TemplateParam{{Name: "trusted", Label: "Доверенные адресаты", Placeholder: "landlord@example.com, accountant@example.com", Multiline: true}},
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
				When: `"to" in provenance && 'untrusted_content' in provenance["to"]`, Effect: config.EffectDeny, Reason: "Адресат взят из входящего письма"})
		}},
	{ID: "drafts-only", Title: "Только черновики", Needs: []string{"email.send"},
		Description: "Агент готовит черновики писем, но отправить не может.",
		Apply: func(c *config.Config, profile string, _ map[string]string, _ []daemon.Tool) error {
			return putRule(c, profile, config.Rule{ID: "no-send", Action: "email.send", Effect: config.EffectDeny, Reason: "Только черновики"})
		}},
	{ID: "notes-folder", Title: "Заметки в папке", Needs: []string{"files.read"},
		Description: "Чтение и дописывание в папке — без вопроса, перезапись — с подтверждением, всё вне папки — запрещено. Папка добавляется в настройки коннектора «Файлы».",
		Params:      []TemplateParam{{Name: "folder", Label: "Папка", Placeholder: "~/Documents/notes"}},
		Apply: func(c *config.Config, profile string, p map[string]string, tools []daemon.Tool) error {
			dir := strings.TrimSuffix(strings.TrimSpace(p["folder"]), "/")
			if dir == "" {
				return fmt.Errorf("choose a folder")
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
			if err := putRule(c, profile, config.Rule{ID: "notes-only", Action: "files.*", When: "!glob(path, " + g + ")", Effect: config.EffectDeny, Reason: "Только папка " + dir}); err != nil {
				return err
			}
			s := map[string]any{}
			for k, v := range c.Connectors["files"] {
				s[k] = v
			}
			roots := toStrings(s["roots"])
			if !slices.Contains(roots, dir) {
				roots = append(roots, dir)
			}
			s["roots"] = roots
			c.SetConnectorSettings("files", s)
			return c.SetProfileConnector(profile, "files", true)
		}},
	{ID: "sites-list", Title: "Сайты по списку", Needs: []string{"browser.read"},
		Description: "Браузер открывает только сайты из списка. Список попадает и в настройки коннектора «Браузер».",
		Params:      []TemplateParam{{Name: "sites", Label: "Сайты", Placeholder: "*.gosuslugi.ru, lk.mosenergosbyt.ru", Multiline: true}},
		Apply: func(c *config.Config, profile string, p map[string]string, _ []daemon.Tool) error {
			items := splitItems(p["sites"])
			if len(items) == 0 {
				return fmt.Errorf("list the sites")
			}
			q := make([]string, len(items))
			for i, it := range items {
				q[i] = celString(it)
			}
			if err := putRule(c, profile, config.Rule{ID: "sites-only", Action: "browser.*", When: "!domain_matches(url, [" + strings.Join(q, ", ") + "])", Effect: config.EffectDeny, Reason: "Сайт не из списка"}); err != nil {
				return err
			}
			s := map[string]any{}
			for k, v := range c.Connectors["browser"] {
				s[k] = v
			}
			s["allowed_domains"] = items
			c.SetConnectorSettings("browser", s)
			return nil
		}},
	{ID: "daily-limit", Title: "Не больше N действий в день", Description: "Ограничить число изменений и отправок за сутки по каждому коннектору профиля.",
		Params: []TemplateParam{{Name: "n", Label: "Действий в день", Placeholder: "20"}},
		Apply: func(c *config.Config, profile string, p map[string]string, tools []daemon.Tool) error {
			n, err := strconv.Atoi(strings.TrimSpace(p["n"]))
			if err != nil || n <= 0 {
				return fmt.Errorf("enter a positive number")
			}
			acts := actionsOf(c, profile, tools, func(t daemon.Tool) bool { return t.Class != "read" })
			if len(acts) == 0 {
				return fmt.Errorf("profile %s has no actions besides reading", profile)
			}
			for _, t := range acts {
				if err := c.PutLimit(profile, config.Limit{ID: "per-day-" + strings.ReplaceAll(t.Name, ".", "-"), Action: t.Name, PerDay: n,
					Reason: fmt.Sprintf("Не больше %d в день", n)}); err != nil {
					return err
				}
			}
			return nil
		}},
	{ID: "no-self-config", Title: "Агентам нельзя менять настройки gate4.ai", Description: "Запретить действие gate4ai.change_policy: агент не сможет менять правила, списки и коннекторы даже с вашего подтверждения.",
		Apply: func(c *config.Config, profile string, _ map[string]string, _ []daemon.Tool) error {
			return putRule(c, profile, config.Rule{ID: "no-self-config", Action: daemon.SelfConnector + ".change_policy", Effect: config.EffectDeny, Reason: "Настройки gate4.ai меняет только владелец"})
		}},
	{ID: "money-ask", Title: "Деньги — всегда с подтверждением", Description: "Любое действие класса «деньги» требует подтверждения, даже если другое правило его разрешает.",
		Apply: func(c *config.Config, profile string, _ map[string]string, tools []daemon.Tool) error {
			acts := actionsOf(c, profile, tools, func(t daemon.Tool) bool { return t.Class == "money" })
			if len(acts) == 0 {
				return fmt.Errorf("no payment connectors in profile %s yet: install one first", profile)
			}
			for _, t := range acts {
				if err := putRule(c, profile, config.Rule{ID: "money-ask-" + strings.ReplaceAll(t.Name, ".", "-"), Action: t.Name, Effect: config.EffectAsk, Reason: "Деньги — с подтверждением"}); err != nil {
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
