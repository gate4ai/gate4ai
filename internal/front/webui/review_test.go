// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/pipeline"
)

// Проверки исправлений по UI/UX-ревью (docs/ux-review): язык, мастер первого
// запуска, «Разрешать всегда», группы действий, учётки, самонастройка.

// docs — коннектор с чтением и записью по пути (для «папок»).
type docs struct{ n int }

func (x *docs) Manifest() manifest.Manifest {
	path := manifest.MustSchema(`{ type: object, properties: { path: { type: string, format: path } }, required: [path] }`)
	return manifest.Manifest{Connector: "docs", Title: "Docs", I18n: map[string]manifest.Text{"ru": {Title: "Документы"}}, Actions: []manifest.Action{
		{Name: "read", Class: "read", Title: "Read a document", InputSchema: path},
		{Name: "list", Class: "read", InputSchema: path},
		{Name: "write", Class: "write", Title: "Write a document", I18n: map[string]manifest.Text{"ru": {Title: "Записать документ"}},
			InputSchema: manifest.MustSchema(`{ type: object, properties: { path: { type: string, format: path }, text: { type: string } }, required: [path, text] }`)}}}
}
func (x *docs) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	x.n++
	return abi.ExecResult{Structured: "ok"}, nil
}
func (x *docs) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

const docsCfg = "agents:\n  bot: {profile: p}\nprofiles:\n  p:\n    connectors: [notes, docs]\n"

// Основной язык — английский; русский — по Accept-Language или переключателю.
func TestEnglishByDefault(t *testing.T) {
	u, _, _, base := setup(t)
	u.DefaultLang = ""
	_, _, _, get := loginAs(t, u, base)
	p := get("/agents")
	if !strings.Contains(p, `lang="en"`) || !strings.Contains(p, ">Home<") || !strings.Contains(p, ">Access<") || strings.Contains(p, "Главная") {
		t.Fatal("english by default", p)
	}
	// Переключатель: cookie и возврат на ту же страницу; чужой адрес — на главную.
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	c.Get(u.LoginURL())
	r, err := c.Get(base + "/lang?l=ru&back=" + url.QueryEscape("/agents"))
	if err != nil || r.Request.URL.Path != "/agents" {
		t.Fatal("switch", err)
	}
	if p := body(t, r); !strings.Contains(p, "Главная") || !strings.Contains(p, `lang="ru"`) {
		t.Fatal("russian after switch", p)
	}
	r, _ = c.Get(base + "/lang?l=en&back=" + url.QueryEscape("//evil.example/x"))
	if r.Request.URL.Host != u.Addr || r.Request.URL.Path != "/" {
		t.Fatal("open redirect", r.Request.URL)
	}
	// Без cookie — по заголовку браузера.
	req, _ := http.NewRequest("GET", base+"/login", nil)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	r, _ = http.DefaultClient.Do(req)
	if p := body(t, r); !strings.Contains(p, "Нужен вход") {
		t.Fatal("login page in russian", p)
	}
}

// Свежая установка без агентов открывает мастер; «Пропустить» — главная с чек-листом.
func TestSetupWizard(t *testing.T) {
	dc := &docs{}
	u, d, _, base := setupCfg(t, "profiles:\n  default:\n    connectors: [notes]\n", dc)
	home := t.TempDir()
	u.Clients = agentsetup.Env{Home: home, GOOS: "linux", Getenv: func(string) string { return "" }, LookPath: func(string) (string, error) { return "", errors.New("no") }}
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	u.Bin = "/opt/gate4ai/gate4ai"
	as := &fakeAutostart{}
	u.Autostart = as
	c, _, post, get := loginAs(t, u, base)

	// Главная — сразу мастер, найденный клиент выбран.
	p := get("/")
	if !strings.Contains(p, "Какой AI-ассистент будет работать через gate4.ai?") || !strings.Contains(p, `value="cursor" checked`) || !strings.Contains(p, "найден на этом компьютере") {
		t.Fatal("wizard step 1", p)
	}
	// Шаг 2: коннекторы с группами действий и автозапуск.
	p = get("/setup/access?client=cursor")
	for _, want := range []string{`name="conn" value="docs"`, `name="effect.docs.read" value="allow" checked`, `name="effect.docs.write" value="ask" checked`, `name="autostart"`, "Документы"} {
		if !strings.Contains(p, want) {
			t.Fatal(want, p)
		}
	}
	// Сохранение: агент, права по группам, автоподключение, автозапуск.
	p = post("/setup", url.Values{"client": {"cursor"}, "conn": {"docs"}, "effect.docs.read": {"allow"}, "effect.docs.write": {"deny"}, "autostart": {"1"}})
	if !strings.Contains(p, "Cursor подключён к gate4.ai") || !strings.Contains(p, "Что происходит сейчас") {
		t.Fatal("wizard save", p)
	}
	cfg := d.Config.Current()
	prof := cfg.Profiles[cfg.Agents["cursor"].Profile]
	if !prof.HasConnector("docs") || prof.HasConnector("notes") {
		t.Fatalf("connectors %+v", prof)
	}
	got := map[string]string{}
	for _, r := range prof.Rules {
		got[r.Action] = r.Effect
	}
	if got["docs.read"] != "allow" || got["docs.list"] != "allow" || got["docs.write"] != "deny" {
		t.Fatalf("rules %v", got)
	}
	if !as.on {
		t.Fatal("autostart")
	}
	if b, err := os.ReadFile(filepath.Join(home, ".cursor", "mcp.json")); err != nil || !strings.Contains(string(b), "GATE4AI_TOKEN") {
		t.Fatal("client config", string(b), err)
	}
	// Решения на странице агента — те же группы.
	if p := get("/agents/cursor"); !strings.Contains(p, `name="class" value="write"`) || !strings.Contains(p, `value="deny" class="on"`) {
		t.Fatal("agent groups", p)
	}
	// Лента шага «Проверка» обновляется отдельно.
	if r, _ := c.Get(base + "/setup/feed?agent=cursor"); r.StatusCode != 200 || !strings.Contains(body(t, r), "Ждём первого запроса") {
		t.Fatal("feed")
	}
	// Агент есть — главная, мастер больше не открывается сам.
	if p := get("/"); strings.Contains(p, "Какой AI-ассистент") {
		t.Fatal("wizard after setup")
	}
}

func TestSetupSkip(t *testing.T) {
	u, _, _, base := setupCfg(t, "profiles:\n  default:\n    connectors: [notes]\n")
	c, _, _, _ := loginAs(t, u, base)
	r, _ := c.Get(base + "/setup/skip")
	if p := body(t, r); r.Request.URL.Path != "/" || !strings.Contains(p, "Начало работы") || strings.Contains(p, "Какой AI-ассистент") {
		t.Fatal("skip", p)
	}
}

// Мастер: файлы — только в выбранных папках, начальный «весь дом» заменяется.
func TestSetupFolders(t *testing.T) {
	u, _, _, base := setupCfg(t, "profiles:\n  default:\n    connectors: [notes]\n")
	// Встроенного files в тестовом daemon нет — проверяем правила напрямую.
	c := &config.Config{Connectors: map[string]map[string]any{"files": {"roots": []any{"~"}}}}
	if !filesWholeHome(c) {
		t.Fatal("whole home not detected")
	}
	if w := folderWhen([]string{"~/notes/", "/data"}); w != `glob(path, "~/notes/**") || glob(path, "/data/**")` {
		t.Fatal(w)
	}
	_, _, _, get := loginAs(t, u, base)
	if p := get("/setup/access?client=cli"); !strings.Contains(p, "my-agent") {
		t.Fatal("cli name", p)
	}
}

// Запрос подтверждения — человеческим языком; «Разрешать всегда» пишет правило
// по папке запроса.
func TestApprovalCardAndAllowAlways(t *testing.T) {
	dc := &docs{}
	u, d, _, base := setupCfg(t, docsCfg, dc)
	_, _, post, get := loginAs(t, u, base)
	res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "docs.write", Args: map[string]any{"path": "/srv/notes/todo.md", "text": "milk"}})
	if res.Status != pipeline.StatusPending {
		t.Fatalf("%+v", res)
	}
	p := get("/approvals")
	for _, want := range []string{"<b>bot</b> хочет <b>Записать документ</b>", "<code>/srv/notes/todo.md</code>", "milk",
		"Правила на это нет, а действия вида «изменение» спрашивают вас.", "Разрешать всегда в /srv/notes", "осталось"} {
		if !strings.Contains(p, want) {
			t.Fatal(want, p)
		}
	}
	if strings.Contains(p, "wants to run") || strings.Contains(p, "no rule matched") {
		t.Fatal("technical text in the card", p)
	}
	p = post("/approvals/"+res.ID, url.Values{"decision": {"approve_always"}})
	if dc.n != 1 || !strings.Contains(p, "Теперь это разрешено без вопроса") {
		t.Fatal("allow always", dc.n, p)
	}
	rs := d.Config.Current().Profiles["p"].Rules
	if len(rs) != 1 || rs[0].Action != "docs.write" || rs[0].Effect != "allow" || rs[0].When != `glob(path, "/srv/notes/**")` {
		t.Fatalf("%+v", rs)
	}
	// Тот же каталог — без вопроса; другой — по-прежнему спрашивает.
	if r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "docs.write", Args: map[string]any{"path": "/srv/notes/b.md", "text": "x"}}); r.Status != pipeline.StatusOK {
		t.Fatalf("%+v", r)
	}
	if r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "docs.write", Args: map[string]any{"path": "/etc/x", "text": "x"}}); r.Status != pipeline.StatusPending {
		t.Fatalf("%+v", r)
	}
}

// Группа действий: одно решение для всех действий класса.
func TestAccessGroup(t *testing.T) {
	dc := &docs{}
	u, d, _, base := setupCfg(t, docsCfg, dc)
	_, _, post, get := loginAs(t, u, base)
	post("/access/group", url.Values{"profile": {"p"}, "connector": {"docs"}, "class": {"read"}, "effect": {"ask"}, "back": {"/agents/bot"}})
	got := map[string]string{}
	for _, r := range d.Config.Current().Profiles["p"].Rules {
		got[r.Action] = r.Effect
	}
	if len(got) != 2 || got["docs.read"] != "ask" || got["docs.list"] != "ask" {
		t.Fatalf("%v", got)
	}
	if p := get("/connectors/docs?profile=p"); !strings.Contains(p, `value="ask" class="on"`) || !strings.Contains(p, "Для bot.") {
		t.Fatal("connector groups", p)
	}
	if p := post("/access/group", url.Values{"profile": {"p"}, "connector": {"docs"}, "class": {"read"}, "effect": {"maybe"}}); !strings.Contains(p, "Неизвестное решение") {
		t.Fatal("bad effect", p)
	}
}

// Учётка сайта из трёх полей: пароль — в хранилище, поля и отправка — по умолчанию.
func TestAccountWizard(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, _ := loginAs(t, u, base)
	p := post("/vault/account", url.Values{"site": {"login.example.com/signin"}, "login": {"me@example.com"}, "password": {"pw-123"}, "profiles": {"p"}})
	if !strings.Contains(p, "Учётка @login.example.com для https://login.example.com сохранена") || strings.Contains(p, "pw-123") {
		t.Fatal(p)
	}
	cr, ok := d.Config.Current().Credentials["@login.example.com"]
	if !ok || cr.Form == nil || cr.Form.Submit.Press != "Enter" || cr.Form.Fields[1].Secret != "@login.example.com.password" || cr.Use[0] != "p" {
		t.Fatalf("%+v", cr)
	}
	if v, err := d.Vault.Get("@login.example.com.password"); err != nil || v != "pw-123" {
		t.Fatal(v, err)
	}
	if p := post("/vault", url.Values{"name": {"Work mail"}, "value": {"x"}}); !strings.Contains(p, "Сохранено как @work-mail") {
		t.Fatal("alias from name", p)
	}
}

// «Агенты могут менять настройки»: нельзя — правило deny во всех правах с агентами.
func TestSelfConfigSetting(t *testing.T) {
	u, d, _, base := setup(t)
	_, _, post, get := loginAs(t, u, base)
	post("/settings/self", url.Values{"self": {"deny"}})
	if !selfConfigDenied(d.Config.Current()) {
		t.Fatal("not denied")
	}
	if p := get("/settings"); !strings.Contains(p, `value="deny" class="on"`) {
		t.Fatal(p)
	}
	post("/settings/self", url.Values{"self": {"ask"}})
	if selfConfigDenied(d.Config.Current()) || len(d.Config.Current().Profiles["p"].Rules) != 0 {
		t.Fatal("still denied")
	}
}

func TestAutostartStates(t *testing.T) {
	for st, want := range map[string]map[string]any{
		"off":        {"installed": false},
		"on":         {"installed": true, "enabled": true, "running": false},
		"on-running": {"installed": true, "enabled": true, "running": true},
	} {
		if got := autostartState(want); got != st {
			t.Errorf("%v: %s, want %s", want, got, st)
		}
	}
	// Файл юнита есть, а служба не включена — выключен.
	if got := autostartState(map[string]any{"installed": true, "enabled": false}); got != "off" {
		t.Fatal(got)
	}
}

// Готовые правила: без нужного коннектора — в свёрнутом списке.
func TestTemplatesRelevant(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, _, get := loginAs(t, u, base)
	p := get("/policy/templates")
	main, rest, _ := strings.Cut(p, "Нужен неустановленный коннектор")
	if !strings.Contains(main, "Только чтение") || strings.Contains(main, "Безопасная почта") || !strings.Contains(rest, "Безопасная почта") || !strings.Contains(rest, "Деньги") {
		t.Fatal(p)
	}
}

// Переведены все строки каталога и описания версий.
func TestVersionComments(t *testing.T) {
	for in, want := range map[string]string{
		"initial": "Первая версия",
		"connector browser: actions ask in profile default": "Коннектор browser добавлен в default: каждое действие спрашивает",
		"web: add agent claude-code":                        "Добавлен агент claude-code",
		"web: something new":                                "Web UI: something new",
	} {
		if got := versionComment(langRU, in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
