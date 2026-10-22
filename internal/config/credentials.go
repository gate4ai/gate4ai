// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Credential — учётка входа на сайт: метаданные в конфигурации (версии,
// откат), значения секретных полей — в Vault по алиасам. Вход — простой
// (Form: поля по порядку, затем отправка) или коннектором входа (Via).
type Credential struct {
	// Bind — точные origin (схема + хост + порт), где разрешён ввод.
	Bind []string   `yaml:"bind" json:"bind"`
	Form *LoginForm `yaml:"form,omitempty" json:"form,omitempty"`
	Via  *LoginVia  `yaml:"via,omitempty" json:"via,omitempty"`
	// Use — профили агентов, которым учётка доступна (browser.login от агента).
	Use []string `yaml:"use,omitempty" json:"use,omitempty"`
	// Connectors — коннекторы, которым она доступна (вход из их действий через service_call).
	Connectors []string `yaml:"connectors,omitempty" json:"connectors,omitempty"`
	// Prompts — вопросы человеку для кодов (browser.fill_secret с source: human): поле → вопрос.
	Prompts map[string]string `yaml:"prompts,omitempty" json:"prompts,omitempty"`
	// LoginPage — признак страницы входа (элемент или начало адреса): без него
	// login_required ставится на любой странице сайтов bind; с ним — только там,
	// где сайт действительно просит войти (код из SMS на сайте кабинета).
	LoginPage *LoginPage `yaml:"login_page,omitempty" json:"login_page,omitempty"`
}

// LoginPage — признак страницы входа: видимый элемент по Selector или адрес,
// начинающийся с URLPrefix (достаточно одного).
type LoginPage struct {
	Selector  string `yaml:"selector,omitempty" json:"selector,omitempty"`
	URLPrefix string `yaml:"url_prefix,omitempty" json:"url_prefix,omitempty"`
}

// LoginForm — простой вход: поля, отправка, признак успеха.
type LoginForm struct {
	Fields  []FormField   `yaml:"fields" json:"fields"`
	Submit  FormSubmit    `yaml:"submit" json:"submit"`
	Success *LoginSuccess `yaml:"success,omitempty" json:"success,omitempty"`
}

// FormField — поле формы: константа Value (видна в конфигурации и журнале)
// или Secret — алиас Vault. Секрет вводится только в input[type=password],
// если у поля не указан явно Type: text.
type FormField struct {
	Name     string `yaml:"name,omitempty" json:"name,omitempty"` // имя для fill_secret (по умолчанию — алиас секрета)
	Selector string `yaml:"selector" json:"selector"`
	Value    string `yaml:"value,omitempty" json:"value,omitempty"`
	Secret   string `yaml:"secret,omitempty" json:"secret,omitempty"`
	Type     string `yaml:"type,omitempty" json:"type,omitempty"` // text — секрет в текстовое поле
	// Без value и secret — поле кода, который вводит человек (fill_secret с
	// source: human и field: <name>); browser.login его пропускает.
}

// FormSubmit — отправка: клик по Selector или клавиша Press (Enter) в последнем поле.
type FormSubmit struct {
	Selector string `yaml:"selector,omitempty" json:"selector,omitempty"`
	Press    string `yaml:"press,omitempty" json:"press,omitempty"`
}

// LoginSuccess — признак успешного входа.
type LoginSuccess struct {
	URLPrefix string `yaml:"url_prefix" json:"url_prefix"`
}

// LoginVia — сложный вход: действие коннектора входа, которое daemon вызывает
// вместо формы с тем же account.
type LoginVia struct {
	Connector string `yaml:"connector" json:"connector"`
	Action    string `yaml:"action" json:"action"`
	// Fields — поля, которые коннектор входа вводит browser.fill_secret {field}
	// (логин, пароль): константа или секрет Vault; селектор передаёт коннектор
	// (здесь — необязательно, по умолчанию).
	Fields []FormField `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// Full — "<коннектор>.<действие>" коннектора входа.
func (v LoginVia) Full() string { return v.Connector + "." + v.Action }

// Secrets — алиасы секретов формы учётки.
func (c Credential) Secrets() []string {
	var out []string
	for _, f := range c.AllFields() {
		if f.Secret != "" {
			out = append(out, f.Secret)
		}
	}
	return out
}

// AllFields — поля учётки: формы или коннектора входа.
func (c Credential) AllFields() []FormField {
	switch {
	case c.Form != nil:
		return c.Form.Fields
	case c.Via != nil:
		return c.Via.Fields
	}
	return nil
}

// Origins — bind в каноническом виде (domain.OriginOf).
func (c Credential) Origins() []string {
	out := make([]string, 0, len(c.Bind))
	for _, b := range c.Bind {
		if o, err := domain.BindOrigin(b); err == nil {
			out = append(out, o)
		}
	}
	return out
}

var (
	credAliasRe = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]{0,63}$`)
	connNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// validateCredentials — учётки и связи слотов коннекторов (connectors.<имя>.credentials).
func (c *Config) validateCredentials(add func(path, format string, a ...any)) {
	for _, alias := range sortedKeys(c.Credentials) {
		cr := c.Credentials[alias]
		p := "credentials." + alias
		if !credAliasRe.MatchString(alias) {
			add(p, "account name must be a vault alias like @emias")
		}
		if len(cr.Bind) == 0 {
			add(p+".bind", "bind is required: the origins (https://login.example) where the account may be entered")
		}
		for i, b := range cr.Bind {
			if _, err := domain.BindOrigin(b); err != nil {
				add(fmt.Sprintf("%s.bind[%d]", p, i), "%v", err)
			}
		}
		switch {
		case cr.Form == nil && cr.Via == nil:
			add(p, "set form (simple sign-in) or via (a sign-in connector)")
		case cr.Form != nil && cr.Via != nil:
			add(p, "form and via are exclusive")
		}
		checkFields := func(base string, fields []FormField, needSelector bool) {
			names := map[string]bool{}
			for i, fl := range fields {
				fp := fmt.Sprintf("%s[%d]", base, i)
				if needSelector && strings.TrimSpace(fl.Selector) == "" {
					add(fp+".selector", "selector is required")
				}
				switch {
				case fl.Value != "" && fl.Secret != "":
					add(fp, "set one of value (a constant) and secret (a vault alias), or neither for a code field the user types (source: human)")
				case fl.Value == "" && fl.Secret == "" && !needSelector:
					add(fp, "set exactly one of value (a constant) and secret (a vault alias)")
				case fl.Value == "" && fl.Secret == "" && fl.Name == "":
					add(fp+".name", "a code field (no value, no secret) needs a name: fill_secret with source human asks for it by name")
				case fl.Secret != "" && !credAliasRe.MatchString(fl.Secret):
					add(fp+".secret", "secret must be a vault alias like @emias.password")
				}
				switch fl.Type {
				case "", "text":
				default:
					add(fp+".type", "type must be text (a secret in a field that is not input[type=password]) or empty")
				}
				name := fl.Name
				if name == "" {
					name = fl.Secret
				}
				if !needSelector && name == "" {
					add(fp+".name", "name is required (the sign-in connector asks for the field by it)")
				}
				if name != "" && names[name] {
					add(fp+".name", "duplicate field name %q", name)
				}
				names[name] = true
			}
		}
		if f := cr.Form; f != nil {
			if len(f.Fields) == 0 {
				add(p+".form.fields", "at least one field is required")
			}
			checkFields(p+".form.fields", f.Fields, true)
			switch {
			case f.Submit.Selector == "" && f.Submit.Press == "":
				add(p+".form.submit", "submit is required: { selector: \"#login-button\" } or { press: Enter }")
			case f.Submit.Selector != "" && f.Submit.Press != "":
				add(p+".form.submit", "set selector or press, not both")
			case f.Submit.Press != "" && f.Submit.Press != "Enter":
				add(p+".form.submit.press", "only Enter is supported")
			}
			if f.Success != nil && domain.OriginOf(f.Success.URLPrefix) == "" {
				add(p+".form.success.url_prefix", "must be an http(s) address prefix like https://lk.example/")
			}
		}
		if v := cr.Via; v != nil {
			if !connNameRe.MatchString(v.Connector) || !connNameRe.MatchString(v.Action) {
				add(p+".via", "connector and action are required (a sign-in connector and its action, e.g. login)")
			}
			checkFields(p+".via.fields", v.Fields, false)
		}
		if lp := cr.LoginPage; lp != nil {
			switch {
			case lp.Selector == "" && lp.URLPrefix == "":
				add(p+".login_page", "set selector (an element of the sign-in page) or url_prefix")
			case lp.URLPrefix != "" && domain.OriginOf(lp.URLPrefix) == "":
				add(p+".login_page.url_prefix", "must be an http(s) address prefix like https://login.example/")
			}
		}
		for i, u := range cr.Use {
			if _, ok := c.Profiles[u]; !ok {
				add(fmt.Sprintf("%s.use[%d]", p, i), "unknown profile %q", u)
			}
		}
		for i, n := range cr.Connectors {
			if !connNameRe.MatchString(n) {
				add(fmt.Sprintf("%s.connectors[%d]", p, i), "invalid connector name %q", n)
			}
		}
	}
	for _, conn := range sortedKeys(c.Connectors) {
		raw, ok := c.Connectors[conn]["credentials"]
		if !ok {
			continue
		}
		p := "connectors." + conn + ".credentials"
		slots, ok := raw.(map[string]any)
		if !ok {
			add(p, "must map the connector's credential slots to accounts: { login: \"@emias\" }")
			continue
		}
		for _, slot := range sortedKeys(slots) {
			alias, _ := slots[slot].(string)
			if _, ok := c.Credentials[alias]; !ok {
				add(p+"."+slot, "unknown account %q (define it in credentials)", slots[slot])
			}
		}
	}
}

// SlotAccount — учётка, связанная со слотом slot коннектора conn ("" — нет).
func (c *Config) SlotAccount(conn, slot string) string {
	m, _ := c.Connectors[conn]["credentials"].(map[string]any)
	a, _ := m[slot].(string)
	return a
}
