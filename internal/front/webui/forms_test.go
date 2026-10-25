// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
)

// Защита входа: кнопки в строках и окна вместо полей под таблицей.
func TestLoginProtectionButtons(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	p := get("/settings/login")
	if strings.Contains(p, "argon2id") || !strings.Contains(p, `href="#m-pw-change">Сменить`) || !strings.Contains(p, `id="m-pw-change"`) {
		t.Fatal("password row: no «Сменить» window", p)
	}
	// Снять пароль и выключить его нельзя: таких кнопок нет.
	if strings.Contains(p, "Снять") || strings.Contains(p, "Одноразовая ссылка") || !strings.Contains(p, "http://"+u.Addr+"/") {
		t.Fatal("password row must only offer a change", p)
	}
	// Бота нет — «Включить» ведёт в мастер Telegram.
	if !strings.Contains(p, `/approvals/telegram?for=login">Включить`) || strings.Contains(p, "сначала подключите бота: ") {
		t.Fatal("telegram row must lead to the bot wizard")
	}
	if p := post("/settings/login/password", url.Values{"current": {"wrong"}, "password": {"secret-pw"}, "repeat": {"secret-pw"}}); !strings.Contains(p, "текущий пароль не подошёл") {
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
	if !strings.Contains(p, "Включить подтверждение входа в Telegram") || !strings.Contains(p, "← Вход") ||
		!strings.Contains(p, `name="from" value="telegram"`) {
		t.Fatal("no login confirmation button at the end of the wizard", p)
	}
	if p := get("/approvals/telegram"); !strings.Contains(p, "Включить подтверждение входа в Telegram") || !strings.Contains(p, "← Уведомления") {
		t.Fatal("the button must be there when opened from the channels too")
	}
	p = post("/settings/login/telegram", url.Values{"on": {"1"}, "from": {"telegram"}, "current": {testPassword}})
	if !u.Protection().Telegram || !strings.Contains(p, "Вход в Web UI подтверждается в Telegram") || !strings.Contains(p, "Подтверждения в Telegram") {
		t.Fatal("must enable and return to the wizard", p)
	}
}

func TestVaultEditAndUses(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	post("/vault", url.Values{"alias": {"@site.password"}, "value": {"one"}})
	p := post("/vault", url.Values{"alias": {"@site.password"}, "value": {"two"}, "edit": {"1"}})
	if !strings.Contains(p, "@site.password заменён") {
		t.Fatal("edit flash", p)
	}
	if v, _ := u.D.Vault.Get("@site.password"); v != "two" {
		t.Fatal("value not changed")
	}
	p = get("/vault")
	if !strings.Contains(p, `href="#m-sec-site.password">Заменить`) || !strings.Contains(p, "Учётки для входа на сайты") || strings.Contains(p, "Изменить учётки") {
		t.Fatal("vault page", p)
	}
}

// Решение для действия на странице коннектора — правило без условия в профиле.
func TestConnectorActionDecision(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	if p := get("/connectors/notes"); !strings.Contains(p, `<option value="" selected>без правила: спрашивать</option>`) || !strings.Contains(p, "<th>Вид</th><th>Решение</th>") {
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
	e := errView("ru", `relay https://relay.gate4.ai: Post "https://relay.gate4.ai/api/devices": dial tcp: lookup relay.gate4.ai on 127.0.0.53:53: read udp 127.0.0.1:43524->127.0.0.53:53: i/o timeout`)
	if !strings.HasPrefix(e.Text, "Нет связи с relay.gate4.ai") || !strings.Contains(e.Detail, "i/o timeout") {
		t.Fatal(e)
	}
	if e := errView("en", "пароли не совпадают"); e.Text != "пароли не совпадают" || e.Detail != "" {
		t.Fatal(e)
	}
	if e := errView("ru", `Get "https://x.example/a": dial tcp 1.2.3.4:443: connect: connection refused`); !strings.Contains(e.Text, "x.example") || !strings.Contains(e.Text, "отклонено") {
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
	if p := post("/policy/rule", add(form, "add_cond", "1", "new_kind", "lte")); !strings.Contains(p, "Выберите аргумент") || !strings.Contains(p, `value="lte" selected`) {
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

// «Переустановить» ставит тот же пакет из того же источника заново и заменяет
// установленную копию; настройки и учётки коннектора остаются.
func TestReinstallConnector(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	const man = "connector: kvx\nversion: \"1\"\nruntime: wasm\ncapabilities: { storage: { quota_mb: 1 } }\nactions:\n  - { name: get, class: read }\n"
	b, err := store.Pack([]byte(man), []byte("\x00asm\x01\x00\x00\x00"), nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "kvx.g4c")
	_ = os.WriteFile(pkg, b, 0o644)
	plan, err := d.PrepareConnectorDev(context.Background(), pkg, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.InstallConnectorDev(context.Background(), pkg, plan.Capabilities, true); err != nil {
		t.Fatal(err)
	}
	d.Config.Edit(owner, "kvx settings", func(c *config.Config) error {
		c.SetConnectorSettings("kvx", map[string]any{"host": "example.com"})
		return nil
	})
	_ = d.Vault.Set("@kvx.token", "t0ken")

	if p := get("/connectors"); !strings.Contains(p, `/connectors/kvx/reinstall"`) || !strings.Contains(p, ">Переустановить</button>") {
		t.Fatal("no reinstall button", p)
	}
	module := filepath.Join(d.ConnectorsDir, "kvx", store.ModuleFile)
	_ = os.Remove(module)
	if p := post("/connectors/kvx/reinstall", url.Values{}); !strings.Contains(p, "переустановлен") {
		t.Fatal("no result message", p)
	}
	if _, err := os.Stat(module); err != nil {
		t.Fatal("installed copy not replaced:", err)
	}
	if s := d.Config.Current().Connectors["kvx"]; s["host"] != "example.com" {
		t.Fatal("settings lost", s)
	}
	if v, _ := d.Vault.Get("@kvx.token"); v != "t0ken" {
		t.Fatal("account lost")
	}
	// Источника больше нет — ошибка, установленная копия остаётся.
	_ = os.Remove(pkg)
	if p := post("/connectors/kvx/reinstall", url.Values{}); !strings.Contains(p, "data-error") {
		t.Fatal("missing source must be an error", p)
	}
	if _, err := os.Stat(module); err != nil {
		t.Fatal("failed reinstall removed the connector:", err)
	}
	if p := post("/connectors/nope/reinstall", url.Values{}); !strings.Contains(p, "data-error") {
		t.Fatal("unknown connector must be an error", p)
	}
}
