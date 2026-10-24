// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser/worker"
)

// formEngine — страница входа без Chrome: поля по селекторам, метка секрета,
// отправка #bind уводит в кабинет при верных фейковых значениях.
type formEngine struct {
	url    string
	after  string // адрес кабинета после входа ("" — testLK)
	marked bool
	focus  string
	values map[string]string
}

const (
	testSign  = "https://login.example/sign"
	testLK    = "https://account.example/home"
	testPass  = "fake-pass-5678"
	testLogin = "+70000000000"
)

func (f *formEngine) Navigate(_ context.Context, u string) error {
	if u == testLK && f.values["#password"] != testPass {
		u = testSign // кабинет без входа уводит на вход
	}
	f.url, f.marked, f.values = u, false, map[string]string{}
	return nil
}
func (f *formEngine) URL(context.Context) (string, error)   { return f.url, nil }
func (f *formEngine) Title(context.Context) (string, error) { return "Вход", nil }
func (f *formEngine) Text(context.Context) (string, error)  { return "Логин и пароль", nil }
func (f *formEngine) Screenshot(context.Context, *worker.Clip, bool) ([]byte, error) {
	return []byte("\x89PNG"), nil
}
func (f *formEngine) Extract(context.Context, string) (any, error) { return "x", nil }
func (f *formEngine) Eval(_ context.Context, expr string, out any) error {
	var v any = map[string]any{"text": ""}
	var args []any
	if i := strings.LastIndex(expr, "(..."); i >= 0 {
		json.Unmarshal([]byte(strings.TrimSuffix(expr[i+4:], ")")), &args)
	}
	sel := func() string { s, _ := args[0].(string); return s }
	switch {
	case strings.HasPrefix(expr, "((sel, full) =>"): // shotJS
		v = map[string]any{"box": map[string]any{"X": 1, "Y": 1, "W": 10, "H": 10}, "matched": 1}
	case strings.Contains(expr, "data-g4-cover"):
		v = 1
	case strings.Contains(expr, "defineProperty(window, '__gate4aiSecret'"):
		f.marked, v = true, true
	case strings.HasPrefix(expr, "window.__gate4aiSecret === "):
		v = f.marked
	case strings.Contains(expr, "not a password field"):
		switch {
		case sel() == "#nope":
			v = "not found"
		case args[1] == true && sel() != "#password":
			v = "not a password field (input[type=password])"
		default:
			v = ""
		}
	case strings.HasPrefix(expr, "((sel) =>"):
		f.focus, v = sel(), true
	case strings.HasPrefix(expr, "((sel, text, method, secret) =>") && sel() == "#absent":
		v = nil // элемента нет (признак страницы входа не найден)
	case strings.HasPrefix(expr, "((sel, text, method, secret) =>"):
		if args[2] == "click" && sel() == "#bind" && f.values["#password"] == testPass {
			f.url, f.marked = testLK, false
			if f.after != "" {
				f.url = f.after
			}
		}
	case strings.Contains(expr, "XMLHttpRequest"), strings.Contains(expr, "performance."):
		v = []any{}
		if strings.Contains(expr, "XMLHttpRequest.prototype.send =") {
			v = true
		}
	}
	b, _ := json.Marshal(v)
	return json.Unmarshal(b, out)
}
func (f *formEngine) InsertText(_ context.Context, t string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[f.focus] += t
	return nil
}
func (f *formEngine) Key(context.Context, string) error { return nil }
func (f *formEngine) Type(_ context.Context, t string, _ time.Duration) error {
	return f.InsertText(context.Background(), t)
}
func (f *formEngine) Close() error { return nil }

// emias — учётка, как её передаёт хост: форма без значений, значения секретов — в Revealed.
var emias = abi.Account{Alias: "@emias", Bind: []string{"https://login.example"}, Success: "https://account.example/",
	Submit: &abi.AccountSubmit{Selector: "#bind"},
	Fields: []abi.AccountField{{Name: "login", Selector: "#login", Value: testLogin},
		{Name: "@emias.password", Selector: "#password", Secret: "@emias.password"},
		{Name: "sms", Selector: "#sms", Type: "text"}}} // поле кода от человека

func loginSetup(t *testing.T) (*hosttest.Host, *Browser, *formEngine) {
	t.Helper()
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		t.Fatal(err)
	}
	eng := &formEngine{}
	b := &Browser{W: &worker.Service{NewEngine: func(context.Context, worker.Settings, string) (worker.Engine, error) { return eng, nil }}}
	return hosttest.New(m, map[string]any{"allowed_domains": []any{"account.example"}, "cdp_url": "http://127.0.0.1:1"}, nil), b, eng
}

// req — запрос хоста агента: учётки агента; для login / fill_secret — форма и значения.
func req(action string, args map[string]any, login bool) abi.ExecRequest {
	r := abi.ExecRequest{Action: action, Args: args, Accounts: []abi.Account{{Alias: emias.Alias, Bind: emias.Bind}}}
	if login {
		acc := emias
		r.Login = &acc
		r.Revealed = map[string]string{"@emias.password": testPass}
	}
	return r
}

func TestAgentLogin(t *testing.T) {
	h, b, eng := loginSetup(t)
	// Кабинет уводит на вход: страница остаётся (сайт входа доступной учётки) и подсказывает login.
	res, err := h.ExecuteRequest(b, req("open", map[string]any{"url": testLK}, false))
	if err != nil {
		t.Fatal(err)
	}
	out := res.Structured.(map[string]any)
	session := out["session"].(string)
	if out["url"] != testSign || out["login_required"].(map[string]any)["account"] != "@emias" {
		t.Fatal(out)
	}
	// Без учётки от хоста (недоступна вызывающему) — secret_denied.
	if _, err := h.ExecuteRequest(b, req("login", map[string]any{"session": session, "account": "@emias"}, false)); err == nil || !strings.Contains(err.Error(), abi.CodeSecretDenied) {
		t.Fatal(err)
	}
	// Поле не найдено — form_mismatch, ничего не введено.
	bad := req("login", map[string]any{"session": session, "account": "@emias"}, true)
	bad.Login.Fields = append([]abi.AccountField{{Name: "x", Selector: "#nope", Value: "1"}}, bad.Login.Fields...)
	if _, err := h.ExecuteRequest(b, bad); err == nil || !strings.Contains(err.Error(), "form_mismatch: #nope: not found") || len(eng.values) != 0 {
		t.Fatal(err, eng.values)
	}
	res, err = h.ExecuteRequest(b, req("login", map[string]any{"session": session, "account": "@emias"}, true))
	if err != nil {
		t.Fatal(err)
	}
	out = res.Structured.(map[string]any)
	if out["status"] != "logged_in" || out["url"] != testLK || out["origin"] != "https://login.example" || res.ResultRef != "logged_in" {
		t.Fatal(out)
	}
	// В кабинете подсказки нет, страница читается.
	res, err = h.ExecuteRequest(b, req("observe", map[string]any{"session": session}, false))
	if err != nil || res.Structured.(map[string]any)["login_required"] != nil {
		t.Fatal(res, err)
	}
	t.Setenv("GATE4AI_HOME", t.TempDir())
	res, err = h.ExecuteRequest(b, req("close", map[string]any{"session": session, "save_steps": true}, false))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.Structured.(map[string]any)["steps_file"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testPass) || !strings.Contains(string(raw), `"action":"login","args":{"account":"@emias"},"result":{"origin":"https://login.example","status":"logged_in"`) {
		t.Fatalf("steps: %s", raw)
	}
}

func TestFillSecretAndSecretOnPage(t *testing.T) {
	h, b, eng := loginSetup(t)
	res, err := h.ExecuteRequest(b, req("open", map[string]any{"url": testSign}, false))
	if err != nil {
		t.Fatal(err)
	}
	session := res.Structured.(map[string]any)["session"].(string)
	// Селектор поля задал владелец: вызывающий его не подменяет.
	if _, err := h.ExecuteRequest(b, req("fill_secret", map[string]any{"session": session, "account": "@emias", "field": "password", "selector": "#login"}, true)); err == nil || !strings.Contains(err.Error(), "the owner set its selector") || len(eng.values) != 0 {
		t.Fatal(err, eng.values)
	}
	if _, err := h.ExecuteRequest(b, req("fill_secret", map[string]any{"session": session, "account": "@emias", "field": "nope"}, true)); err == nil || !strings.Contains(err.Error(), "no field") {
		t.Fatal(err)
	}
	// Поле по короткому имени секрета, селектор — из учётки.
	res, err = h.ExecuteRequest(b, req("fill_secret", map[string]any{"session": session, "account": "@emias", "field": "password"}, true))
	if err != nil || eng.values["#password"] != testPass || res.Structured.(map[string]any)["origin"] != "https://login.example" {
		t.Fatal(res, err, eng.values)
	}
	for _, a := range []string{"observe", "network", "page_extract"} {
		args := map[string]any{"session": session}
		if a == "page_extract" {
			args["instruction"] = "password"
		}
		if _, err := h.ExecuteRequest(b, req(a, args, false)); err == nil || !strings.Contains(err.Error(), "secret_on_page") {
			t.Errorf("%s with a secret on the page: %v", a, err)
		}
	}
	// Снимок можно: поля секрета закрашены (проверено в worker на настоящем Chrome).
	if _, err := h.ExecuteRequest(b, req("page_screenshot", map[string]any{"session": session, "selector": "#password"}, false)); err != nil {
		t.Fatal("page_screenshot with covered secret fields:", err)
	}
	if _, err := h.ExecuteRequest(b, req("act", map[string]any{"session": session, "selector": "#login", "method": "fill", "value": "x"}, false)); err == nil || !strings.Contains(err.Error(), "secret_on_page") {
		t.Fatal(err)
	}
	if _, err := h.ExecuteRequest(b, req("act", map[string]any{"session": session, "selector": "#login", "method": "click"}, false)); err != nil {
		t.Fatal(err)
	}
	// Код от человека: хост спросил и передал ответ; в результат и шаги он не попадает.
	// Поле для кода задаёт владелец (поле кода учётки); агент не выбирает
	// текстовое поле сам — без поля владельца только input[type=password].
	for _, args := range []map[string]any{
		{"session": session, "account": "@emias", "source": "human", "selector": "#login"},
		{"session": session, "account": "@emias", "source": "human", "field": "sms", "selector": "#login"},
	} {
		bad := req("fill_secret", args, true)
		bad.Revealed = map[string]string{abi.HumanValue: "246810"}
		if _, err := h.ExecuteRequest(b, bad); err == nil || eng.values["#login"] == "246810" {
			t.Fatal("human code typed into a field the agent chose", args, err)
		}
	}
	human := req("fill_secret", map[string]any{"session": session, "account": "@emias", "source": "human", "field": "sms"}, true)
	human.Revealed = map[string]string{abi.HumanValue: "246810"}
	if _, err := h.ExecuteRequest(b, human); err != nil || eng.values["#sms"] != "246810" {
		t.Fatal(err, eng.values)
	}
	res, _ = h.ExecuteRequest(b, req("close", map[string]any{"session": session}, false))
	raw, _ := json.Marshal(res.Structured)
	if strings.Contains(string(raw), testPass) || strings.Contains(string(raw), "246810") {
		t.Fatalf("values in steps: %s", raw)
	}
}

func TestLoginThroughConnector(t *testing.T) {
	h, b, _ := loginSetup(t)
	res, err := h.ExecuteRequest(b, req("open", map[string]any{"url": testSign}, false))
	if err != nil {
		t.Fatal(err)
	}
	session := res.Structured.(map[string]any)["session"].(string)
	// Учётка со сложным входом — через коннектор входа (его вызывает хост).
	via := req("login", map[string]any{"session": session, "account": "@emias"}, true)
	via.Login = &abi.Account{Alias: "@emias", Bind: emias.Bind, Via: "mos_login.login"}
	if _, err := h.ExecuteRequest(b, via); err == nil || !strings.Contains(err.Error(), "use_connector") || !strings.Contains(err.Error(), "mos_login.login") {
		t.Fatal(err)
	}
	// Коннектор входа, вызванный хостом для сеанса агента, работает на странице этого сеанса.
	caller := &abi.Caller{Connector: "mos_login", Call: "root1", Params: map[string]any{"domains": []any{"account.example"}}, Session: session}
	fill := req("fill_secret", map[string]any{"account": "@emias", "field": "password"}, true)
	fill.Caller = caller
	if _, err := h.ExecuteRequest(b, fill); err != nil {
		t.Fatal(err)
	}
	click := req("act", map[string]any{"selector": "#bind", "method": "click"}, false)
	click.Caller = caller
	res, err = h.ExecuteRequest(b, click)
	if err != nil || res.Structured.(map[string]any)["url"] != testLK {
		t.Fatal(res, err)
	}
}

func TestConnectorLoginOnRootPage(t *testing.T) {
	h, b, eng := loginSetup(t)
	caller := &abi.Caller{Connector: "lk_emias", Call: "root9", Params: map[string]any{"domains": []any{"account.example"}}}
	open := req("open", map[string]any{"url": testLK}, false)
	open.Caller = caller
	res, err := h.ExecuteRequest(b, open)
	if err != nil || res.Structured.(map[string]any)["login_required"] == nil {
		t.Fatal(res, err)
	}
	login := req("login", map[string]any{"account": "@emias"}, true)
	login.Caller = caller
	res, err = h.ExecuteRequest(b, login)
	if err != nil || res.Structured.(map[string]any)["status"] != "logged_in" {
		t.Fatal(res, err)
	}
	// Без учёток вызывающего сайт входа вне его сайтов закрыт.
	b.EndCall("root9")
	eng.values = nil // вышли из кабинета
	open.Accounts, open.Caller = nil, &abi.Caller{Connector: "lk_emias", Call: "root10", Params: caller.Params}
	if _, err := h.ExecuteRequest(b, open); err == nil || !strings.Contains(err.Error(), "redirected off the allowlist") {
		t.Fatal(err)
	}
	b.EndCall("root10")
}

// Учётка с коннектором входа: её поля вводит он сам (fill_secret), простой
// вход формой — нет; хост проверяет страницу до вопроса человеку; вход
// записывается одним шагом, шаги коннектора входа — с пометкой via.
func TestViaAccount(t *testing.T) {
	h, b, eng := loginSetup(t)
	via := abi.Account{Alias: "@emias", Bind: []string{"https://login.example"}, Via: "site_login.login",
		Fields: []abi.AccountField{{Name: "password", Secret: "@emias.password"}}}
	withVia := func(action string, args map[string]any) abi.ExecRequest {
		acc := via
		return abi.ExecRequest{Action: action, Args: args, Login: &acc, Accounts: []abi.Account{{Alias: via.Alias, Bind: via.Bind}},
			Revealed: map[string]string{"@emias.password": testPass}}
	}
	res, err := h.ExecuteRequest(b, req("open", map[string]any{"url": testSign}, false))
	if err != nil {
		t.Fatal(err)
	}
	session := res.Structured.(map[string]any)["session"].(string)
	if _, err := h.ExecuteRequest(b, withVia("login", map[string]any{"session": session, "account": "@emias"})); err == nil || !strings.Contains(err.Error(), "use_connector") {
		t.Fatal("login form of a via account:", err)
	}
	// Хост проверяет страницу и поле до вопроса человеку: ничего не вводится.
	typed := len(eng.values)
	pre := withVia("fill_secret", map[string]any{"session": session, "account": "@emias", "source": "human", "selector": "#login", "precheck": true, viaArg: "site_login"}) // шаг коннектора входа
	pre.Revealed = nil
	pre.Settings = h.Settings
	// Служебные аргументы хост добавляет после проверки по схеме — напрямую, как daemon.
	if _, err := b.Execute(context.Background(), nil, pre); err != nil || len(eng.values) != typed {
		t.Fatal("precheck:", err, eng.values)
	}
	// Шаг коннектора входа в сеансе агента: его поле — по имени, селектор — от него.
	step := withVia("fill_secret", map[string]any{"account": "@emias", "field": "password", "selector": "#password"})
	step.Caller = &abi.Caller{Connector: "site_login", Call: "root", Session: session}
	if _, err := h.ExecuteRequest(b, step); err != nil || eng.values["#password"] != testPass {
		t.Fatal("fill_secret by the sign-in connector:", err, eng.values)
	}
	// Хост записывает вход одним шагом.
	rec := withVia("login", map[string]any{"session": session, "account": "@emias", "record_only": true, "result": map[string]any{"status": "logged_in"}})
	rec.Settings = h.Settings
	if _, err := b.Execute(context.Background(), nil, rec); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATE4AI_HOME", t.TempDir())
	res, err = h.ExecuteRequest(b, req("close", map[string]any{"session": session, "save_steps": true}, false))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.Structured.(map[string]any)["steps_file"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var fx hosttest.Fixture
	json.Unmarshal(raw, &fx)
	var vias, logins int
	for _, s := range fx.Steps {
		if s.Via == "site_login" && s.Action == "fill_secret" {
			vias++
		}
		if s.Action == "login" && s.Via == "" && s.Result["status"] == "logged_in" {
			logins++
		}
		if _, ok := s.Args["precheck"]; ok {
			t.Fatal("the host's precheck is not a session step")
		}
	}
	if vias != 1 || logins != 1 || strings.Contains(string(raw), testPass) {
		t.Fatalf("steps: %s", raw)
	}
}

// Правило поля задаёт владелец: селектор вызывающего не уводит секрет
// поля type: text в другое поле; без селектора владельца вызывающий выбирает
// только поле пароля; коннектор входа учётки (via) задаёт поле сам.
func TestFillSecretOwnerSelector(t *testing.T) {
	h, b, eng := loginSetup(t)
	res, err := h.ExecuteRequest(b, req("open", map[string]any{"url": testSign}, false))
	if err != nil {
		t.Fatal(err)
	}
	session := res.Structured.(map[string]any)["session"].(string)
	with := func(f abi.AccountField, sel string) abi.ExecRequest {
		r := req("fill_secret", map[string]any{"session": session, "account": "@emias", "field": "pin", "selector": sel}, true)
		r.Login.Fields = []abi.AccountField{f}
		return r
	}
	pin := abi.AccountField{Name: "pin", Selector: "#pin", Secret: "@emias.password", Type: "text"}
	if _, err := h.ExecuteRequest(b, with(pin, "#comment")); err == nil || !strings.Contains(err.Error(), "the owner set its selector") {
		t.Fatal("type: text secret into the caller's field:", err)
	}
	pin.Selector = ""
	if _, err := h.ExecuteRequest(b, with(pin, "#comment")); err == nil || !strings.Contains(err.Error(), "form_mismatch") {
		t.Fatal("without the owner's selector — only a password field:", err)
	}
	if len(eng.values) != 0 {
		t.Fatal("typed:", eng.values)
	}
	if _, err := h.ExecuteRequest(b, with(pin, "#password")); err != nil || eng.values["#password"] != testPass {
		t.Fatal(err, eng.values)
	}
	// Учётка с via: агент — не её коннектор входа, коннектор другого входа — тоже.
	pin.Selector = "#pin"
	for _, c := range []*abi.Caller{nil, {Connector: "other_login", Call: "r", Session: session}} {
		r := with(pin, "#comment")
		r.Login.Via = "site_login.login"
		r.Caller = c
		if _, err := h.ExecuteRequest(b, r); err == nil || !strings.Contains(err.Error(), "the owner set its selector") {
			t.Fatal(c, err)
		}
	}
}

// Адрес после входа — без одноразовых кодов и токенов (query и fragment).
func TestLoginCleansURL(t *testing.T) {
	h, b, eng := loginSetup(t)
	eng.after = testLK + "?code=one-time-code#access_token=tok-123&state=x"
	res, err := h.ExecuteRequest(b, req("open", map[string]any{"url": testSign}, false))
	if err != nil {
		t.Fatal(err)
	}
	session := res.Structured.(map[string]any)["session"].(string)
	res, err = h.ExecuteRequest(b, req("login", map[string]any{"session": session, "account": "@emias"}, true))
	if err != nil {
		t.Fatal(err)
	}
	if u := res.Structured.(map[string]any)["url"]; u != testLK+"?code=***#access_token=***&state=***" {
		t.Fatal(u)
	}
	res, _ = h.ExecuteRequest(b, req("close", map[string]any{"session": session}, false))
	raw, _ := json.Marshal(res.Structured)
	if strings.Contains(string(raw), "one-time-code") || strings.Contains(string(raw), "tok-123") {
		t.Fatalf("steps: %s", raw)
	}
}

// login_required — только на странице входа, если учётка задаёт её признак.
func TestLoginPageMarker(t *testing.T) {
	h, b, _ := loginSetup(t)
	for _, c := range []struct {
		page *abi.AccountPage
		want bool
	}{{nil, true}, {&abi.AccountPage{Selector: "#absent"}, false}, {&abi.AccountPage{Selector: "#login"}, true}, {&abi.AccountPage{URLPrefix: testSign}, true}} {
		r := req("open", map[string]any{"url": testSign}, false)
		r.Accounts[0].LoginPage = c.page
		res, err := h.ExecuteRequest(b, r)
		if err != nil {
			t.Fatal(err)
		}
		out := res.Structured.(map[string]any)
		if _, got := out["login_required"]; got != c.want {
			t.Errorf("%+v: login_required %v", c.page, out)
		}
		h.ExecuteRequest(b, req("close", map[string]any{"session": out["session"]}, false))
	}
}
