// SPDX-License-Identifier: Apache-2.0

// Package manifest — манифест коннектора gate4.ai: действия, схемы аргументов,
// класс эффекта, способ проверки результата, формат доказательства, среда
// исполнения, возможности (capabilities) и лимиты. Манифест — контракт между
// коннектором и хостом: из него генерируются команды CLI, MCP-инструменты,
// skill и формы Web UI, а возможности обеспечивает среда исполнения.
package manifest

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Среды исполнения.
const (
	RuntimeWasm    = "wasm"    // WASM-модуль в хосте (wazero), только функции хоста
	RuntimeNative  = "native"  // процесс под надзором хоста (только проверенный издатель)
	RuntimeBuiltin = "builtin" // внутренний коннектор хоста (не из магазина)
)

// Manifest — описание коннектора.
type Manifest struct {
	Connector string `json:"connector" yaml:"connector"`
	// Title — имя коннектора для человека по-английски ("Files"); пусто — Connector.
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Description string `json:"description" yaml:"description"`
	// I18n — переводы title и description по коду языка ("ru").
	I18n    map[string]Text `json:"i18n,omitempty" yaml:"i18n,omitempty"`
	Version string          `json:"version" yaml:"version"`
	Runtime string          `json:"runtime,omitempty" yaml:"runtime,omitempty"`
	// Contract — мажор контракта действий из Services: несовместимое изменение
	// их схем — только с его повышением (contract.Diff).
	Contract int `json:"contract,omitempty" yaml:"contract,omitempty"`
	// Services — действия, которые другие коннекторы могут вызвать через service_call.
	Services []string `json:"services,omitempty" yaml:"services,omitempty"`
	// Platforms — платформы сборки "os/arch" (обязательны для runtime native).
	Platforms    []string     `json:"platforms,omitempty" yaml:"platforms,omitempty"`
	Capabilities Capabilities `json:"capabilities" yaml:"capabilities"`
	// Credentials — слоты учёток, с которыми коннектор входит на сайты
	// (browser.login / browser.fill_secret): владелец связывает слот со своей
	// учёткой в connectors.<коннектор>.credentials; значений коннектор не видит.
	Credentials []CredentialSlot `json:"credentials,omitempty" yaml:"credentials,omitempty"`
	Limits      Limits           `json:"limits" yaml:"limits"`
	Actions     []Action         `json:"actions" yaml:"actions"`
}

// CredentialSlot — слот учётки: имя (аргумент account в вызовах браузера) и
// origin, на которые коннектор входит с ней. Учётка владельца, связанная со
// слотом, должна быть привязана только к этим origin.
type CredentialSlot struct {
	Slot        string   `json:"slot" yaml:"slot"`
	Bind        []string `json:"bind" yaml:"bind"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	// Types — какие учётки подходят: form — простой вход формой (поля и
	// отправка описаны в учётке), via — вход коннектором входа; пусто — любые.
	Types []string `json:"types,omitempty" yaml:"types,omitempty"`
	// Fields — поля учётки, которые коннектор вводит сам (browser.fill_secret):
	// пароль, код из SMS; пусто — только browser.login.
	Fields []string `json:"fields,omitempty" yaml:"fields,omitempty"`
}

// Типы учёток слота.
const (
	SlotForm = "form"
	SlotVia  = "via"
)

// Accepts — учётка вида kind (form | via) подходит слоту.
func (c CredentialSlot) Accepts(kind string) bool {
	return len(c.Types) == 0 || slices.Contains(c.Types, kind)
}

// Slot ищет слот учётки по имени.
func (m *Manifest) Slot(name string) (CredentialSlot, bool) {
	for _, c := range m.Credentials {
		if c.Slot == name {
			return c, true
		}
	}
	return CredentialSlot{}, false
}

// Action — действие коннектора.
type Action struct {
	Name string `json:"name" yaml:"name"`
	// Title — короткое название действия по-английски ("Read a file"); пусто — Name.
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Description string `json:"description" yaml:"description"`
	// I18n — переводы title и description по коду языка ("ru").
	I18n         map[string]Text `json:"i18n,omitempty" yaml:"i18n,omitempty"`
	Class        string          `json:"class" yaml:"class"`                                     // read | write | external | money
	InputSchema  Schema          `json:"input_schema" yaml:"input_schema"`                       // аргументы (как inputSchema MCP)
	OutputSchema *Schema         `json:"output_schema,omitempty" yaml:"output_schema,omitempty"` // структурированный результат (как outputSchema MCP)
	Verify       string          `json:"verify,omitempty" yaml:"verify,omitempty"`               // метод проверки результата, "" — нет; для money обязателен
	Evidence     string          `json:"evidence,omitempty" yaml:"evidence,omitempty"`           // формат доказательства: eml, text, png, json
	Untrusted    bool            `json:"untrusted,omitempty" yaml:"untrusted,omitempty"`         // результат содержит недоверенный контент
	Sensitive    []string        `json:"sensitive,omitempty" yaml:"sensitive,omitempty"`         // аргументы, которые маскируются в журнале
	Example      string          `json:"example,omitempty" yaml:"example,omitempty"`             // пример вызова CLI
	// ServiceOnly — действие только для других коннекторов (service_call):
	// агенту не показывается. Должно быть в Services.
	ServiceOnly bool `json:"service_only,omitempty" yaml:"service_only,omitempty"`
	// Calls — подвызовы действия через service_call ("<поставщик>.<действие>",
	// каждый объявлен в capabilities.services). Хост пропускает из действия
	// только их; разрешение действия их покрывает, поэтому они видны
	// пользователю в описании, подтверждении и согласии.
	Calls []string `json:"calls,omitempty" yaml:"calls,omitempty"`
	// CallReasons — зачем действию подвызов (calls → причина): видно в
	// описании, подтверждении и согласии рядом с подвызовом. Например, чтение,
	// которому нужны клики (browser.act — «раскрыть список записей»).
	CallReasons map[string]string `json:"call_reasons,omitempty" yaml:"call_reasons,omitempty"`
}

// Text — перевод названия и описания на один язык.
type Text struct {
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

var langRe = regexp.MustCompile(`^[a-z]{2}$`)

func validateI18n(where string, i map[string]Text) error {
	for lang := range i {
		if !langRe.MatchString(lang) {
			return fmt.Errorf("manifest %s: i18n: bad language %q (two lowercase letters, e.g. ru)", where, lang)
		}
	}
	return nil
}

// TitleIn — название коннектора на языке lang: перевод, иначе Title, иначе имя.
func (m Manifest) TitleIn(lang string) string {
	if t := m.I18n[lang].Title; t != "" {
		return t
	}
	if m.Title != "" {
		return m.Title
	}
	return m.Connector
}

// DescriptionIn — описание коннектора на языке lang (иначе исходное).
func (m Manifest) DescriptionIn(lang string) string {
	if d := m.I18n[lang].Description; d != "" {
		return d
	}
	return m.Description
}

// TitleIn — название действия на языке lang: перевод, иначе Title, иначе имя.
func (a Action) TitleIn(lang string) string {
	if t := a.I18n[lang].Title; t != "" {
		return t
	}
	if a.Title != "" {
		return a.Title
	}
	return a.Name
}

// DescriptionIn — описание действия на языке lang (иначе исходное).
func (a Action) DescriptionIn(lang string) string {
	if d := a.I18n[lang].Description; d != "" {
		return d
	}
	return a.Description
}

// CallsText — подвызовы с причинами: "browser.open, browser.act (раскрыть список)".
func (a Action) CallsText() string {
	parts := make([]string, len(a.Calls))
	for i, c := range a.Calls {
		parts[i] = c
		if r := a.CallReasons[c]; r != "" {
			parts[i] += " (" + r + ")"
		}
	}
	return strings.Join(parts, ", ")
}

// ClassRank — порядок классов эффекта: read < write < external < money
// (подвызов не может быть выше класса вызывающего действия); -1 — неизвестный.
func ClassRank(class string) int {
	switch class {
	case "read":
		return 0
	case "write":
		return 1
	case "external":
		return 2
	case "money":
		return 3
	}
	return -1
}

// CallsAction — действие a объявило подвызов target.action.
func (a Action) CallsAction(target, action string) bool {
	for _, c := range a.Calls {
		if c == target+"."+action {
			return true
		}
	}
	return false
}

// Summary — возможности и подвызовы действий человеческим языком: то, на что
// владелец соглашается при установке (изменение — повторное согласие).
func (m *Manifest) Summary() []string {
	out := m.Capabilities.Summary()
	for _, c := range m.Credentials {
		detail := "slot " + c.Slot
		if len(c.Types) > 0 {
			kinds := map[string]string{SlotForm: "a sign-in form", SlotVia: "a sign-in connector"}
			var ks []string
			for _, t := range c.Types {
				ks = append(ks, kinds[t])
			}
			detail += "; " + strings.Join(ks, " or ")
		}
		if len(c.Fields) > 0 {
			detail += "; enters fields " + strings.Join(c.Fields, ", ")
		}
		out = append(out, fmt.Sprintf("signs in on %s with your account (%s)", strings.Join(c.Bind, ", "), detail))
	}
	for _, a := range m.Actions {
		if len(a.Calls) > 0 {
			out = append(out, fmt.Sprintf("action %s calls %s", a.Name, a.CallsText()))
		}
	}
	sort.Strings(out)
	return out
}

// Full — полное имя действия "connector.action".
func (m *Manifest) Full(a Action) string { return m.Connector + "." + a.Name }

// Find ищет действие по короткому имени.
func (m *Manifest) Find(name string) (Action, bool) {
	for _, a := range m.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validate проверяет манифест.
func (m *Manifest) Validate() error {
	if !nameRe.MatchString(m.Connector) {
		return fmt.Errorf("manifest: bad connector name %q", m.Connector)
	}
	switch m.Runtime {
	case "", RuntimeWasm, RuntimeNative, RuntimeBuiltin:
	default:
		return fmt.Errorf("manifest %s: bad runtime %q", m.Connector, m.Runtime)
	}
	if err := m.Capabilities.validate(); err != nil {
		return fmt.Errorf("manifest %s: %w", m.Connector, err)
	}
	if len(m.Capabilities.Reveal) > 0 && m.Runtime != RuntimeNative {
		return fmt.Errorf("manifest %s: capabilities.reveal is only for runtime native", m.Connector)
	}
	if m.Runtime == RuntimeNative && len(m.Platforms) == 0 {
		return fmt.Errorf("manifest %s: runtime native needs platforms", m.Connector)
	}
	if m.Runtime != RuntimeNative && len(m.Platforms) > 0 {
		return fmt.Errorf("manifest %s: platforms are only for runtime native", m.Connector)
	}
	seenPlat := map[string]bool{}
	for _, p := range m.Platforms {
		if !Platforms[p] || seenPlat[p] {
			return fmt.Errorf("manifest %s: bad or duplicate platform %q", m.Connector, p)
		}
		seenPlat[p] = true
	}
	if m.Contract < 0 || (len(m.Services) > 0 && m.Contract == 0) {
		return fmt.Errorf("manifest %s: services need contract >= 1", m.Connector)
	}
	seenSlot := map[string]bool{}
	for _, c := range m.Credentials {
		if !nameRe.MatchString(c.Slot) || seenSlot[c.Slot] {
			return fmt.Errorf("manifest %s: credentials: bad or duplicate slot %q", m.Connector, c.Slot)
		}
		seenSlot[c.Slot] = true
		if len(c.Bind) == 0 {
			return fmt.Errorf("manifest %s: credentials: slot %s: empty bind (origins the connector signs in on)", m.Connector, c.Slot)
		}
		for _, t := range c.Types {
			if t != SlotForm && t != SlotVia {
				return fmt.Errorf("manifest %s: credentials: slot %s: type %q (form or via)", m.Connector, c.Slot, t)
			}
		}
		for _, f := range c.Fields {
			if !nameRe.MatchString(f) {
				return fmt.Errorf("manifest %s: credentials: slot %s: bad field %q", m.Connector, c.Slot, f)
			}
		}
		for _, b := range c.Bind {
			if err := checkTemplate("credentials.bind", b); err != nil {
				return fmt.Errorf("manifest %s: %w", m.Connector, err)
			}
			if tmplRe.MatchString(b) {
				continue
			}
			if _, err := domain.BindOrigin(b); err != nil {
				return fmt.Errorf("manifest %s: credentials: slot %s: %w", m.Connector, c.Slot, err)
			}
		}
	}
	if err := validateI18n(m.Connector, m.I18n); err != nil {
		return err
	}
	if m.Limits.MemoryMB < 0 || m.Limits.TimeoutS < 0 || m.Limits.VerifyDeadlineS < 0 {
		return fmt.Errorf("manifest %s: negative limits", m.Connector)
	}
	seen := map[string]bool{}
	for _, a := range m.Actions {
		if !nameRe.MatchString(a.Name) || seen[a.Name] {
			return fmt.Errorf("manifest %s: bad or duplicate action %q", m.Connector, a.Name)
		}
		seen[a.Name] = true
		if err := validateI18n(m.Connector+"."+a.Name, a.I18n); err != nil {
			return err
		}
		switch a.Class {
		case "read", "write", "external", "money":
		default:
			return fmt.Errorf("manifest %s.%s: bad class %q", m.Connector, a.Name, a.Class)
		}
		if a.Class == "money" && a.Verify == "" {
			return fmt.Errorf("manifest %s.%s: class money needs verify", m.Connector, a.Name)
		}
		if err := a.InputSchema.validate(""); err != nil {
			return fmt.Errorf("manifest %s.%s: input_schema: %w", m.Connector, a.Name, err)
		}
		if a.OutputSchema != nil {
			if err := a.OutputSchema.validate(""); err != nil {
				return fmt.Errorf("manifest %s.%s: output_schema: %w", m.Connector, a.Name, err)
			}
		}
		for _, n := range a.Sensitive {
			if _, ok := a.InputSchema.Properties.Get(n); !ok {
				return fmt.Errorf("manifest %s.%s: sensitive argument %q is not declared", m.Connector, a.Name, n)
			}
		}
	}
	for _, s := range m.Services {
		if !seen[s] {
			return fmt.Errorf("manifest %s: service %q is not an action", m.Connector, s)
		}
	}
	for _, a := range m.Actions {
		for c := range a.CallReasons {
			if !slices.Contains(a.Calls, c) {
				return fmt.Errorf("manifest %s.%s: call_reasons names %q, which is not in calls", m.Connector, a.Name, c)
			}
		}
		seenCall := map[string]bool{}
		for _, c := range a.Calls {
			target, action, ok := strings.Cut(c, ".")
			if !ok || seenCall[c] {
				return fmt.Errorf("manifest %s.%s: bad or duplicate call %q (want <connector>.<action>)", m.Connector, a.Name, c)
			}
			seenCall[c] = true
			declared := false
			for _, sc := range m.Capabilities.Services {
				if sc.Target == target {
					for _, x := range sc.Actions {
						declared = declared || x == action
					}
				}
			}
			if !declared {
				return fmt.Errorf("manifest %s.%s: call %s is not declared in capabilities.services", m.Connector, a.Name, c)
			}
		}
		if a.ServiceOnly && !m.IsService(a.Name) {
			return fmt.Errorf("manifest %s.%s: service_only action must be listed in services", m.Connector, a.Name)
		}
	}
	return nil
}

// Platforms — платформы, под которые собираются нативные пакеты (как у ядра gate4ai).
var Platforms = map[string]bool{"linux/amd64": true, "linux/arm64": true, "darwin/amd64": true, "darwin/arm64": true,
	"windows/amd64": true, "windows/arm64": true}

// IsService — действие доступно другим коннекторам через service_call.
func (m *Manifest) IsService(action string) bool {
	for _, s := range m.Services {
		if s == action {
			return true
		}
	}
	return false
}

// ArgError — ошибка нормализации аргументов (отдаётся агенту как invalid_arguments).
type ArgError struct{ Msg string }

func (e *ArgError) Error() string { return e.Msg }

func argErr(f string, a ...any) error { return &ArgError{Msg: fmt.Sprintf(f, a...)} }

// Normalize проверяет аргументы и приводит их к input_schema (см. Schema.Normalize).
func (a Action) Normalize(in map[string]any) (map[string]any, error) {
	return a.InputSchema.Normalize(in)
}

// JSONSchema — inputSchema для MCP.
func (a Action) JSONSchema() map[string]any { return a.InputSchema.JSONSchema() }

// ArgNames — имена аргументов верхнего уровня.
func (a Action) ArgNames() []string { return a.InputSchema.Names() }

func sortStrings(s []string) { sort.Strings(s) }

// Origins — origin слота с подставленными настройками экземпляра (канонические).
func (c CredentialSlot) Origins(settings map[string]any) []string {
	var out []string
	for _, b := range expandAll(c.Bind, settings) {
		if o, err := domain.BindOrigin(b); err == nil {
			out = append(out, o)
		}
	}
	return out
}
