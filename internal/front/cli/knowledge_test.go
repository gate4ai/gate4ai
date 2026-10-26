// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/knowledge"
)

// sessionRecord — запись сеанса покупки на сайте с входом, ошибкой и человеческим шагом.
const sessionRecord = `{"session":"s-abc","steps":[
 {"action":"open","args":{"url":"https://ticket.example.org/","wait_for_text":"Найти"},"result":{"url":"https://ticket.example.org/","title":"Билеты","login_required":{"account":"@ex"}}},
 {"action":"login","args":{"account":"@ex"},"result":{"status":"logged_in","url":"https://ticket.example.org/"}},
 {"action":"act","args":{"selector":"#from","method":"fill","value":"Москва"},"result":{"url":"https://ticket.example.org/"}},
 {"action":"act","args":{"selector":"button","text":"Найти","method":"click","expect_selector":".x"},"error":{"code":"not_found","message":"no .x"}},
 {"action":"act","args":{"selector":"button","text":"Найти","method":"click","expect_text":"Найдено"},"result":{"url":"https://ticket.example.org/search"}},
 {"action":"observe","args":{"max_chars":2000},"result":{"url":"https://ticket.example.org/search","title":"Поезда","text":"Найдено 5. Пассажир Иванов, ivanov@mail.ru, +7 916 123-45-67, заказ 1234567890"}},
 {"action":"handoff","args":{"message":"Оплатите"},"result":{"status":"done"}},
 {"action":"close","args":{},"result":{"closed":true}}
]}`

func TestConnectorsNewFromSession(t *testing.T) {
	home := t.TempDir()
	steps := filepath.Join(home, "s-abc.json")
	os.WriteFile(steps, []byte(sessionRecord), 0o600)
	dir := filepath.Join(home, "k", "gate4ai-ex")
	run := func(args ...string) (int, string) {
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		code := Run(context.Background(), &Env{Stdout: out, Stderr: errb, Getenv: func(k string) string {
			if k == "GATE4AI_HOME" {
				return home
			}
			return ""
		}}, args)
		return code, out.String() + errb.String()
	}
	code, out := run("connectors", "new", "ex", "--from-session", steps, "--task", "buy_ticket", "-o", dir)
	if code != ExitOK {
		t.Fatal(code, out)
	}
	res := jsonOf(t, out)
	if res["action"] != "buy_ticket" || res["class"] != "write" || len(res["pitfalls"].([]any)) != 1 || len(res["human_steps"].([]any)) != 1 {
		t.Fatalf("%v", out)
	}
	m, err := manifest.Parse(must(os.ReadFile(filepath.Join(dir, "manifest.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := m.Find("buy_ticket")
	if !ok || a.Class != "write" || !a.CallsAction("browser", "login") || !a.CallsAction("browser", "act") || len(m.Credentials) != 1 || m.Credentials[0].Bind[0] != "https://ticket.example.org" {
		t.Fatalf("%+v %+v", a, m.Credentials)
	}
	if _, req := a.InputSchema.Properties, a.InputSchema.IsRequired("input_1"); !req {
		t.Fatalf("%+v", a.InputSchema)
	}
	for _, f := range []string{"main.go", "connector.go", "connector_test.go"} {
		src := must(os.ReadFile(filepath.Join(dir, f)))
		if _, err := parser.ParseFile(token.NewFileSet(), f, src, parser.AllErrors); err != nil {
			t.Errorf("%s: %v\n%s", f, err, src)
		}
	}
	code2 := string(must(os.ReadFile(filepath.Join(dir, "connector.go"))))
	for _, want := range []string{`case "buy_ticket":`, `"login", map[string]any{"account": "site"}`, `r.Args["input_1"]`, `"expect_text": "Найдено"`} {
		if !strings.Contains(code2, want) {
			t.Errorf("connector.go lacks %q", want)
		}
	}
	// Фикстура: без шага с ошибкой, handoff и close; личные данные заменены.
	fx, err := hosttest.LoadFixture(filepath.Join(dir, "testdata", "buy_ticket.json"))
	if err != nil || len(fx.Steps) != 5 {
		t.Fatalf("%v %d", err, len(fx.Steps))
	}
	raw := string(must(os.ReadFile(filepath.Join(dir, "testdata", "buy_ticket.json"))))
	for _, gone := range []string{"ivanov@mail.ru", "+7 916 123-45-67", "1234567890"} {
		if strings.Contains(raw, gone) {
			t.Errorf("fixture still has %q", gone)
		}
	}
	// Рецепт: шапка, шаги, ловушка, человек; LICENSE и .gitignore на месте; git не инициализирован.
	r, body, err := knowledge.ParseRecipe(must(os.ReadFile(filepath.Join(dir, "RECIPE.md"))))
	if err != nil {
		t.Fatal(err)
	}
	if r.Site != "ticket.example.org" || r.Tasks[0].ID != "buy_ticket" || r.Tasks[0].Status != "draft" || r.Login == nil || strings.Join(r.HumanSteps, ",") != "handoff" {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{"Open `https://ticket.example.org/`", "Type `{input_1}` into `#from`", "not_found", "Оплатите"} {
		if !strings.Contains(body, want) {
			t.Errorf("recipe lacks %q:\n%s", want, body)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Fatal("git must not be initialised")
	}
	if rep := knowledge.Check(dir); !rep.OK {
		t.Fatalf("%+v", rep)
	}
	// Id сеанса → файл в каталоге данных; нет файла — подсказка про --save_steps.
	os.MkdirAll(filepath.Join(home, "browser-steps"), 0o700)
	os.WriteFile(filepath.Join(home, "browser-steps", "s-abc.json"), []byte(sessionRecord), 0o600)
	t.Setenv("GATE4AI_HOME", home)
	if code, out := run("connectors", "new", "ex2", "--from-session", "s-abc", "-o", filepath.Join(home, "ex2")); code != ExitOK || !strings.Contains(out, `"action": "task"`) {
		t.Fatal(code, out)
	}
	if code, out := run("connectors", "new", "ex3", "--from-session", "s-none", "-o", filepath.Join(home, "ex3")); code == ExitOK || !strings.Contains(out, "--save_steps") {
		t.Fatal(code, out)
	}
	// Без --site и без сеанса — usage.
	if code, _ := run("connectors", "new", "ex4", "-o", filepath.Join(home, "ex4")); code != ExitUsage {
		t.Fatal(code)
	}
}

func TestKnowledgeAndLookupCommands(t *testing.T) {
	know := t.TempDir()
	c, _ := connectors.NewInProcess(mailFake{})
	h := startWith(t, policy+"knowledge: { dir: "+know+" }\n", c)
	_, out := h.run(t, "", "agents", "token", "claude-ai")
	tok := jsonOf(t, out)["token"].(string)
	// Пустая папка.
	code, out := h.run(t, "", "knowledge")
	if code != ExitOK || !strings.Contains(out, "is empty") {
		t.Fatal(code, out)
	}
	// Папка с рецептом: list, check (не готова: нет манифеста и LICENSE), lookup по сайту — владелец и агент.
	f := filepath.Join(know, "gate4ai-shop")
	os.MkdirAll(f, 0o755)
	os.WriteFile(filepath.Join(f, "RECIPE.md"), []byte("---\nsite: shop.example.org\ntasks:\n  - { id: buy, status: draft }\n---\n## buy\n"), 0o644)
	code, out = h.run(t, "", "knowledge", "list")
	if code != ExitOK || !strings.Contains(out, "gate4ai-shop") || !strings.Contains(out, `"state": "files"`) {
		t.Fatal(code, out)
	}
	code, out = h.run(t, "", "knowledge", "check", "gate4ai-shop")
	if code != ExitError || !strings.Contains(out, "manifest.yaml") || !strings.Contains(out, "gh repo create") {
		t.Fatal(code, out)
	}
	if code, out = h.run(t, "", "knowledge", "check", "nope"); code == ExitOK {
		t.Fatal(out)
	}
	code, out = h.run(t, "", "sites", "lookup", "shop.example.org")
	if code != ExitOK {
		t.Fatal(code, out)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(out), &res)
	if kn := res["knowledge"].([]any); len(kn) != 1 || !strings.Contains(res["hint"].(string), "RECIPE.md") {
		t.Fatalf("%s", out)
	}
	code, out = h.run(t, tok, "sites", "lookup", "https://other.example.org/x")
	if code != ExitOK || !strings.Contains(out, "connectors new example --site other.example.org --from-session") {
		t.Fatal(code, out)
	}
	// Агенту папка знаний и проверка недоступны (команды владельца, как connectors).
	if code, _ = h.run(t, tok, "knowledge"); code == ExitOK {
		t.Fatal("agent read the knowledge folder")
	}
}

// vault status — агент ждёт, пока владелец задаст секреты учётки (значений не видит).
func TestVaultStatusWait(t *testing.T) {
	c, _ := connectors.NewInProcess(mailFake{})
	h := startWith(t, policy+"credentials:\n  \"@site\":\n    bind: [\"https://login.example.org\"]\n    form: { fields: [{selector: \"#p\", secret: \"@site.password\"}], submit: {selector: button} }\n    use: [assistant]\n", c)
	_, out := h.run(t, "", "agents", "token", "claude-ai")
	tok := jsonOf(t, out)["token"].(string)
	old := VaultPoll
	VaultPoll = 20 * time.Millisecond
	defer func() { VaultPoll = old }()
	code, out := h.run(t, tok, "vault", "status", "@site", "--wait", "100ms")
	if code != ExitPending || !strings.Contains(out, `"ready": false`) || strings.Contains(out, "s3cret") {
		t.Fatal(code, out)
	}
	if code, _ = h.run(t, tok, "vault", "status", "@other"); code == ExitOK {
		t.Fatal("unknown account")
	}
	if code, out = h.run(t, "", "vault", "set", "@site.password"); code != ExitOK {
		t.Fatal(out)
	}
	if code, out = h.run(t, tok, "vault", "status", "site", "--wait", "1s"); code != ExitOK || !strings.Contains(out, `"ready": true`) {
		t.Fatal(code, out)
	}
}

// --login-connector: шаги входа из записи сеанса — отдельный коннектор входа,
// в основном коннекторе вход — один browser.login по слоту.
func TestConnectorsNewLoginConnector(t *testing.T) {
	home := t.TempDir()
	rec := `{"session":"s-l","steps":[
 {"action":"open","args":{"url":"https://ticket.example.org/"},"result":{"url":"https://pass.example.org/login","login_required":{"account":"@ex"}}},
 {"action":"act","args":{"selector":"#login","method":"fill","value":"+79161234567"},"result":{"url":"https://pass.example.org/login"}},
 {"action":"act","args":{"selector":"#password","method":"fill","value":"hunter2"},"result":{"url":"https://pass.example.org/login"}},
 {"action":"act","args":{"selector":"button","text":"Войти","method":"click","expect_url_change":true},"result":{"url":"https://ticket.example.org/cabinet"}},
 {"action":"observe","args":{"max_chars":2000},"result":{"url":"https://ticket.example.org/cabinet","title":"Кабинет","text":"Привет"}},
 {"action":"close","args":{},"result":{"closed":true}}]}`
	steps := filepath.Join(home, "s-l.json")
	os.WriteFile(steps, []byte(rec), 0o600)
	dir := filepath.Join(home, "k", "gate4ai-ex")
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run(context.Background(), &Env{Stdout: out, Stderr: errb, Getenv: func(string) string { return "" }},
		[]string{"connectors", "new", "ex", "--from-session", steps, "--task", "cabinet", "--login-connector", "ex_login", "-o", dir})
	if code != ExitOK {
		t.Fatal(code, out.String()+errb.String())
	}
	res := jsonOf(t, out.String())
	lc := res["login_connector"].(map[string]any)
	ldir := lc["folder"].(string)
	if lc["connector"] != "ex_login" || lc["origin"] != "https://pass.example.org" || filepath.Base(ldir) != "gate4ai-ex-login" {
		t.Fatalf("%+v", lc)
	}
	m, err := manifest.Parse(must(os.ReadFile(filepath.Join(ldir, "manifest.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := m.Find("login")
	if !ok || !m.IsService("login") || m.Contract != 1 || !a.CallsAction("browser", "fill_secret") || len(m.Credentials) != 1 || strings.Join(m.Credentials[0].Fields, ",") != "login,password" {
		t.Fatalf("%+v %+v", m, a)
	}
	for _, f := range []string{"main.go", "connector.go", "connector_test.go"} {
		src := must(os.ReadFile(filepath.Join(ldir, f)))
		if _, err := parser.ParseFile(token.NewFileSet(), f, src, parser.AllErrors); err != nil {
			t.Errorf("%s: %v\n%s", f, err, src)
		}
		if bytes.Contains(src, []byte("hunter2")) {
			t.Errorf("%s: the password leaked", f)
		}
	}
	// Основной коннектор: слот site и подвызов login, но без шагов формы входа.
	mm, err := manifest.Parse(must(os.ReadFile(filepath.Join(dir, "manifest.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	ma, _ := mm.Find("cabinet")
	if !ma.CallsAction("browser", "login") || ma.CallsAction("browser", "fill_secret") || len(mm.Credentials) != 1 {
		t.Fatalf("%+v %+v", ma, mm.Credentials)
	}
	main := string(must(os.ReadFile(filepath.Join(dir, "connector.go"))))
	if !strings.Contains(main, `"login", map[string]any{"account": "site"}`) || strings.Contains(main, "#password") {
		t.Fatal(main)
	}
	// Запись, где вход делал хост (шаг login учётки): ветки входа нет — честная ошибка.
	os.WriteFile(filepath.Join(home, "s-abc.json"), []byte(sessionRecord), 0o600)
	out3, err3 := &bytes.Buffer{}, &bytes.Buffer{}
	code = Run(context.Background(), &Env{Stdout: out3, Stderr: err3, Getenv: func(string) string { return "" }},
		[]string{"connectors", "new", "ex3", "--from-session", filepath.Join(home, "s-abc.json"), "--login-connector", "ex3_login", "-o", filepath.Join(home, "ex3")})
	if all := out3.String() + err3.String(); code == ExitOK || !strings.Contains(all, "no sign-in steps") {
		t.Fatal(code, all)
	}
}
