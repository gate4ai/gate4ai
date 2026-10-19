// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
)

const credBase = `
agents:
  dev: { profile: connector-dev }
profiles:
  connector-dev: { connectors: [browser] }
connectors:
  lk_emias: { credentials: { login: "@emias" } }
credentials:
  "@emias":
    bind: ["https://login.mos.ru"]
    form:
      fields:
        - { selector: "#login", value: "+79000000000" }
        - { selector: "#password", secret: "@emias.password" }
      submit: { selector: "#bind" }
      success: { url_prefix: "https://lk.emias.mos.ru/" }
    use: [connector-dev]
    connectors: [lk_emias]
  "@bank":
    bind: ["https://id.bank.example"]
    via: { connector: bank_login, action: login }
`

func TestCredentials(t *testing.T) {
	c, err := Parse([]byte(credBase))
	if err != nil {
		t.Fatal(err)
	}
	e := c.Credentials["@emias"]
	if e.Form.Fields[1].Secret != "@emias.password" || e.Form.Success.URLPrefix != "https://lk.emias.mos.ru/" || c.Credentials["@bank"].Via.Full() != "bank_login.login" {
		t.Fatalf("%+v", c.Credentials)
	}
	if got := e.Secrets(); len(got) != 1 || got[0] != "@emias.password" {
		t.Fatal(got)
	}
	if c.SlotAccount("lk_emias", "login") != "@emias" || c.SlotAccount("lk_emias", "x") != "" {
		t.Fatal("slot")
	}
	for name, edit := range map[string][2]string{
		"no bind":          {`bind: ["https://login.mos.ru"]`, `bind: []`},
		"http bind":        {`bind: ["https://login.mos.ru"]`, `bind: ["http://login.mos.ru"]`},
		"bind with path":   {`bind: ["https://login.mos.ru"]`, `bind: ["https://login.mos.ru/sign"]`},
		"form and via":     {`    use: [connector-dev]`, "    via: { connector: x, action: login }\n    use: [connector-dev]"},
		"value and secret": {`value: "+79000000000"`, `value: "+79000000000", secret: "@emias.login"`},
		"empty selector":   {`selector: "#password"`, `selector: ""`},
		"bad secret alias": {`secret: "@emias.password"`, `secret: "emias.password"`},
		"bad type":         {`secret: "@emias.password" }`, `secret: "@emias.password", type: email }`},
		"no submit":        {`submit: { selector: "#bind" }`, `submit: {}`},
		"press not Enter":  {`submit: { selector: "#bind" }`, `submit: { press: Tab }`},
		"unknown profile":  {`use: [connector-dev]`, `use: [nobody]`},
		"unknown account":  {`credentials: { login: "@emias" }`, `credentials: { login: "@nope" }`},
		"bad account name": {`"@bank":`, `"bank":`},
		"empty via":        {`via: { connector: bank_login, action: login }`, `via: { connector: "", action: login }`},
	} {
		src := strings.Replace(credBase, edit[0], edit[1], 1)
		if src == credBase {
			t.Fatalf("%s: edit did not apply", name)
		}
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// http — только для адресов этой машины (тесты).
	if _, err := Parse([]byte(strings.Replace(credBase, `bind: ["https://login.mos.ru"]`, `bind: ["http://127.0.0.1:8080"]`, 1))); err != nil {
		t.Fatal(err)
	}
}

// Учётка с коннектором входа: поля, которые он вводит сам, и признак страницы входа.
func TestCredentialViaFields(t *testing.T) {
	ok := `
agents: { bot: { profile: p } }
profiles: { p: { connectors: [] } }
credentials:
  "@emias":
    bind: ["https://login.mos.ru", "https://lk.emias.mos.ru"]
    via:
      connector: mos_login
      action: login
      fields:
        - { name: login, secret: "@mosru.login", type: text }
        - { name: password, secret: "@mosru.password" }
    login_page: { selector: "#otp_input" }
    prompts: { sms: "Код из SMS для входа в ЕМИАС" }
`
	c, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Credentials["@emias"].Secrets(); len(got) != 2 || got[1] != "@mosru.password" {
		t.Fatal(got)
	}
	for _, bad := range []string{
		strings.Replace(ok, `{ name: password, secret: "@mosru.password" }`, `{ name: password }`, 1),
		strings.Replace(ok, `login_page: { selector: "#otp_input" }`, "login_page: {}", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
