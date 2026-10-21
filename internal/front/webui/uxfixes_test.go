// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
)

// Защита входа: кнопки в строках и окна вместо полей под таблицей.
func TestLoginProtectionButtons(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	p := get("/settings")
	if strings.Contains(p, "argon2id") || !strings.Contains(p, `href="#m-pw-set">Задать`) || !strings.Contains(p, `id="m-pw-set"`) {
		t.Fatal("password row: no «Задать» window")
	}
	// Ссылка — единственный способ: её не выключить.
	if !strings.Contains(p, "<button disabled>Выключить</button>") || !strings.Contains(p, "единственный способ входа") {
		t.Fatal("the last login method must not be switchable off")
	}
	// Бота нет — «Включить» ведёт в мастер Telegram.
	if !strings.Contains(p, `/approvals/telegram?for=login">Включить`) || strings.Contains(p, "сначала подключите бота: ") {
		t.Fatal("telegram row must lead to the bot wizard")
	}
	post("/settings/login/password", url.Values{"password": {"secret-pw"}, "repeat": {"secret-pw"}})
	_, _, post = protectedSession(t, u, base, "secret-pw")
	p = post("/settings/login/link", url.Values{"on": {"1"}, "current": {"secret-pw"}}) // уже включена — просто страница
	if !strings.Contains(p, `href="#m-pw-change">Сменить`) || !strings.Contains(p, `href="#m-pw-remove">Снять`) || !strings.Contains(p, `href="#m-link">Выключить`) {
		t.Fatal("password set: no «Сменить», «Снять» and a password window for the link", p)
	}
	if p := post("/settings/login/password", url.Values{"remove": {"1"}, "current": {"wrong"}}); !strings.Contains(p, "текущий пароль не подошёл") {
		t.Fatal("wrong current password message", p)
	}
}

// Мастер Telegram, открытый из «Защиты входа», заканчивается включением подтверждения входа.
func TestTelegramWizardEnablesLogin(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	u.Confirm = &fakeConfirm{}
	if _, err := d.Config.Edit(owner, "telegram", func(c *config.Config) error {
		c.Approvals.Telegram = &config.Telegram{Token: "@telegram.bot", OwnerID: 1}
		c.SetChannel("telegram", true)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := get("/approvals/telegram?for=login")
	if !strings.Contains(p, "Включить подтверждение входа в Web UI через Telegram") || !strings.Contains(p, "← Настройки: защита входа") ||
		!strings.Contains(p, `name="from" value="telegram"`) {
		t.Fatal("no login confirmation button at the end of the wizard", p)
	}
	if p := get("/approvals/telegram"); !strings.Contains(p, "Включить подтверждение входа в Web UI через Telegram") || !strings.Contains(p, "← Каналы уведомлений") {
		t.Fatal("the button must be there when opened from the channels too")
	}
	p = post("/settings/login/telegram", url.Values{"on": {"1"}, "from": {"telegram"}})
	if !u.Protection().Telegram || !strings.Contains(p, "Вход в Web UI подтверждается в Telegram") || !strings.Contains(p, "Подтверждения в Telegram") {
		t.Fatal("must enable and return to the wizard", p)
	}
}

func TestVaultEditAndUses(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	post("/vault", url.Values{"alias": {"@site.password"}, "value": {"one"}})
	p := post("/vault", url.Values{"alias": {"@site.password"}, "value": {"two"}, "edit": {"1"}})
	if !strings.Contains(p, "Значение @site.password изменено") {
		t.Fatal("edit flash", p)
	}
	if v, _ := u.D.Vault.Get("@site.password"); v != "two" {
		t.Fatal("value not changed")
	}
	p = get("/vault")
	if !strings.Contains(p, `href="#m-sec-site.password">Изменить`) || !strings.Contains(p, "Применяется на") || strings.Contains(p, "Изменить учётки") {
		t.Fatal("vault page", p)
	}
}

// Решение для действия на странице коннектора — правило без условия в профиле.
func TestConnectorActionDecision(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	if p := get("/connectors/notes"); !strings.Contains(p, `<option value="" selected>без правил: спрашивать</option>`) {
		t.Fatal("default decision", p)
	}
	post("/connectors/notes/decision", url.Values{"profile": {"p"}, "action": {"append"}, "effect": {"allow"}})
	rs := d.Config.Current().Profiles["p"].Rules
	if len(rs) != 1 || rs[0].Action != "notes.append" || rs[0].Effect != "allow" || rs[0].When != "" {
		t.Fatal("rule", rs)
	}
	if p := get("/connectors/notes?profile=p"); !strings.Contains(p, `<option value="allow" selected>`) {
		t.Fatal("decision not shown")
	}
	post("/connectors/notes/decision", url.Values{"profile": {"p"}, "action": {"append"}, "effect": {""}})
	if rs := d.Config.Current().Profiles["p"].Rules; len(rs) != 0 {
		t.Fatal("rule not removed", rs)
	}
	// Правило с условием — список не трогает его, ведёт в «Правила».
	d.Config.Edit(owner, "cond", func(c *config.Config) error {
		return c.PutRule("p", config.Rule{ID: "c", Action: "notes.append", When: `text == "x"`, Effect: "allow"})
	})
	if p := get("/connectors/notes"); !strings.Contains(p, "правила с условием") {
		t.Fatal("conditional rule must not be editable here")
	}
	if p := post("/connectors/notes/decision", url.Values{"profile": {"p"}, "action": {"append"}, "effect": {"deny"}}); !strings.Contains(p, "правила с условием") {
		t.Fatal("conditional rule overwritten", p)
	}
}

func TestInstallNotFromStore(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, _, get := loginAs(t, u, base)
	if p := get("/connectors"); !strings.Contains(p, `id="m-local"`) || !strings.Contains(p, "Поставить не из магазина") {
		t.Fatal("no install-from-elsewhere window")
	}
	p := get("/connectors?local=1&dev=1&install=" + url.QueryEscape("/nonexistent/x.g4c"))
	if !strings.Contains(p, "/nonexistent/x.g4c") || !strings.Contains(p, "data-error") {
		t.Fatal("missing file must be an error", p)
	}
}

func TestErrView(t *testing.T) {
	e := errView(`relay https://relay.gate4.ai: Post "https://relay.gate4.ai/api/devices": dial tcp: lookup relay.gate4.ai on 127.0.0.53:53: read udp 127.0.0.1:43524->127.0.0.53:53: i/o timeout`)
	if !strings.HasPrefix(e.Text, "Нет связи с relay.gate4.ai") || !strings.Contains(e.Detail, "i/o timeout") {
		t.Fatal(e)
	}
	if e := errView("пароли не совпадают"); e.Text != "пароли не совпадают" || e.Detail != "" {
		t.Fatal(e)
	}
	if e := errView(`Get "https://x.example/a": dial tcp 1.2.3.4:443: connect: connection refused`); !strings.Contains(e.Text, "x.example") || !strings.Contains(e.Text, "отклонено") {
		t.Fatal(e)
	}
}

func TestSplitAnd(t *testing.T) {
	got := splitAnd(`glob(path, "~/a/**") && (x || y) && !text.contains("a && b")`)
	if len(got) != 3 || got[1] != "(x || y)" || got[2] != `!text.contains("a && b")` {
		t.Fatalf("%q", got)
	}
	if joinAnd([]string{"a || b", "c", ""}) != "(a || b) && c" {
		t.Fatal(joinAnd([]string{"a || b", "c"}))
	}
}

// Форма правила одной фразой: условия добавляются и убираются без сохранения,
// прежнее условие правила сохраняется как было.
func TestRuleSentenceForm(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	p := get("/policy/rule?profile=p&action=notes.append")
	if !strings.Contains(p, "когда агент вызывает") || !strings.Contains(p, `<span class="muted">всегда</span>`) || strings.Contains(p, `name="cond_kind"`) {
		t.Fatal("new rule: no sentence or empty condition rows", p)
	}
	form := url.Values{"profile": {"p"}, "action": {"notes.append"}, "effect": {"allow"}, "reason": {"r"}}
	add := func(v url.Values, kv ...string) url.Values {
		w := url.Values{}
		for k, x := range v {
			w[k] = x
		}
		for i := 0; i+1 < len(kv); i += 2 {
			w.Set(kv[i], kv[i+1])
		}
		return w
	}
	p = post("/policy/rule", add(form, "add_cond", "1", "new_arg", "text", "new_kind", "contains", "new_value", "hello"))
	if !strings.Contains(p, "если text содержит hello") || !strings.Contains(p, `name="cond_kind" value="contains"`) || len(d.Config.Current().Profiles["p"].Rules) != 0 {
		t.Fatal("condition not added (or rule saved early)", p)
	}
	if p := post("/policy/rule", add(form, "add_cond", "1", "new_kind", "lte")); !strings.Contains(p, "выберите аргумент") || !strings.Contains(p, `value="lte" selected`) {
		t.Fatal("bad condition must keep the new-condition fields with an error", p)
	}
	withCond := add(form, "cond_kind", "contains", "cond_arg", "text", "cond_value", "hello")
	if p := post("/policy/rule", add(withCond, "drop", "c0")); strings.Contains(p, "text содержит hello") {
		t.Fatal("condition not dropped")
	}
	post("/policy/rule", add(withCond, "when", `text.size() < 100`))
	rs := d.Config.Current().Profiles["p"].Rules
	if len(rs) != 1 || rs[0].When != `text.size() < 100 && text.contains("hello")` || rs[0].Effect != "allow" {
		t.Fatal("saved rule", rs)
	}
	// Правка: условия правила — чипами, сохраняются без изменений.
	p = get("/policy/rule?profile=p&id=" + rs[0].ID)
	if !strings.Contains(p, `<code>text.size() &lt; 100</code>`) || !strings.Contains(p, `name="when_part"`) {
		t.Fatal("existing condition not shown as chips", p)
	}
	post("/policy/rule", url.Values{"profile": {"p"}, "action": {"notes.append"}, "id": {rs[0].ID}, "effect": {"deny"},
		"when_part": {`text.size() < 100`, `text.contains("hello")`}})
	if r := d.Config.Current().Profiles["p"].Rules[0]; r.When != rs[0].When || r.Effect != "deny" {
		t.Fatal("edit changed the condition", r)
	}
}
