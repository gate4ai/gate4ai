// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// fakeField — поле страницы входа фейкового браузера.
type fakeField struct {
	tag, typ string
	visible  bool
	value    string
}

// loginChrome — фейковый Chrome по CDP со страницей входа: поля, кнопка
// отправки, переход в кабинет при верных (фейковых) значениях, метка
// документа markJS, которая пропадает при переходе.
type loginChrome struct {
	srv     *httptest.Server
	mu      sync.Mutex
	url     string
	fields  map[string]*fakeField
	focus   string
	marker  string
	typed   []string // все Input.insertText
	methods []string
	covered []any // селекторы, закрашенные на снимках (coverJS)
}

const (
	signURL  = "https://login.example/sign"
	cabinet  = "https://account.example/home"
	fakeUser = "user-1"
	fakePass = "fake-pass-1234"
)

func newLoginChrome(t *testing.T) *loginChrome {
	f := &loginChrome{}
	f.reset()
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://" + r.Host + "/devtools/browser/x"})
	})
	mux.HandleFunc("/devtools/browser/x", f.ws)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *loginChrome) reset() {
	f.fields = map[string]*fakeField{
		"#login":    {tag: "input", typ: "text", visible: true},
		"#password": {tag: "input", typ: "password", visible: true},
		"#trap":     {tag: "input", typ: "password", visible: false},
		"#title":    {tag: "input", typ: "text", visible: true},
		"#bind":     {tag: "button", typ: "submit", visible: true},
	}
	f.focus = ""
}

// navigate — новый документ: метки нет; кабинет без входа уводит на вход.
func (f *loginChrome) navigate(u string) {
	f.marker = ""
	f.reset()
	f.url = u
}

// tailArgs — аргументы вызова "(...[...])" в конце выражения.
func tailArgs(expr string) []any {
	i := strings.LastIndex(expr, "(...")
	if i < 0 {
		return nil
	}
	var a []any
	json.Unmarshal([]byte(strings.TrimSuffix(expr[i+4:], ")")), &a)
	return a
}

func (f *loginChrome) eval(expr string) any {
	args := tailArgs(expr)
	switch {
	case expr == "location.href":
		return f.url
	case expr == "document.title":
		return "Вход"
	case strings.HasPrefix(expr, "((sel, full) =>"): // shotJS
		return map[string]any{"box": map[string]any{"X": 1, "Y": 1, "W": 10, "H": 10}, "matched": 1}
	case strings.Contains(expr, "data-g4-cover"): // coverJS: закрасить поля секрета на снимке
		if args[0] != nil {
			f.covered = append(f.covered, args[0])
			return 1
		}
		return 0
	case strings.Contains(expr, "window.__gate4aiSecret === ") && !strings.Contains(expr, "defineProperty"):
		n := strings.TrimPrefix(expr, "window.__gate4aiSecret === ")
		var want string
		json.Unmarshal([]byte(n), &want)
		return f.marker != "" && f.marker == want
	case strings.Contains(expr, "defineProperty(window, '__gate4aiSecret'"):
		if f.marker == "" {
			f.marker = args[0].(string)
		}
		return f.marker == args[0]
	case strings.Contains(expr, "not a password field"): // checkFieldJS
		fl, ok := f.fields[args[0].(string)]
		switch {
		case !ok:
			return "not found"
		case !fl.visible:
			return "not visible"
		case fl.tag != "input":
			return "not a text field"
		case args[1] == true && fl.typ != "password":
			return "not a password field (input[type=password]); the owner allows other fields with type: text"
		}
		return ""
	case strings.HasPrefix(expr, "((sel) =>"): // fillFieldJS
		if fl, ok := f.fields[args[0].(string)]; ok && fl.visible {
			f.focus = args[0].(string)
			fl.value = ""
			return true
		}
		return false
	case strings.HasPrefix(expr, "((sel, text, method, secret) =>"): // actJS
		sel, text, method := args[0].(string), args[1].(string), args[2].(string)
		fl, ok := f.fields[sel]
		if !ok && sel != "body" {
			return nil
		}
		// Как подпись в actJS: значение поля сравнивается с текстом, только
		// пока секрета на странице нет (secret — false).
		if text != "" && (fl == nil || args[3] == true || !strings.Contains(fl.value, text)) {
			return nil
		}
		if method == "click" && sel == "#bind" {
			f.submit()
		}
		if method == "press" || method == "fill" {
			f.focus = sel
		}
		if fl != nil {
			// Как actJS: у input нет видимого текста — его value.
			return map[string]any{"text": fl.value}
		}
		return map[string]any{"text": ""}
	case strings.Contains(expr, "g4visible(root)") && strings.HasSuffix(expr, "(document.body)"): // текст страницы (visibleTextJS)
		// Текст страницы не содержит значений полей (как в Chrome).
		return "Вход на сайт. Логин, пароль"
	case strings.Contains(expr, "XMLHttpRequest.prototype.send ="):
		return true
	case strings.Contains(expr, "performance.getEntriesByType"):
		return []any{}
	case strings.Contains(expr, "nth-of-type"):
		return []any{map[string]any{"selector": "#login", "tag": "input"}}
	case strings.Contains(expr, "querySelector"):
		return map[string]any{"X": 1, "Y": 1, "W": 10, "H": 10}
	}
	return nil
}

func (f *loginChrome) submit() {
	if f.fields["#login"].value == fakeUser && f.fields["#password"].value == fakePass {
		f.navigate(cabinet)
	}
}

func (f *loginChrome) ws(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := context.Background()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		json.Unmarshal(b, &m)
		f.mu.Lock()
		f.methods = append(f.methods, m.Method)
		var res any = map[string]any{}
		var event map[string]any
		switch m.Method {
		case "Target.createTarget":
			res = map[string]any{"targetId": "T1"}
		case "Target.attachToTarget":
			res = map[string]any{"sessionId": "S1"}
		case "Page.navigate":
			u := m.Params["url"].(string)
			if u == cabinet+"?anon" {
				u = signURL + "?back=/home" // кабинет без входа уводит на страницу входа
			}
			f.navigate(u)
			event = map[string]any{"method": "Page.loadEventFired", "sessionId": "S1", "params": map[string]any{}}
		case "Runtime.evaluate":
			res = map[string]any{"result": map[string]any{"value": f.eval(m.Params["expression"].(string))}}
		case "Input.insertText":
			t := m.Params["text"].(string)
			f.typed = append(f.typed, t)
			if fl := f.fields[f.focus]; fl != nil {
				fl.value += t
			}
		case "Page.captureScreenshot":
			res = map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("\x89PNG"))}
		}
		f.mu.Unlock()
		out, _ := json.Marshal(map[string]any{"id": m.ID, "result": res})
		c.Write(ctx, websocket.MessageText, out)
		if event != nil {
			eb, _ := json.Marshal(event)
			c.Write(ctx, websocket.MessageText, eb)
		}
	}
}

func (f *loginChrome) typedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.typed)
}

func loginForm(pass string) LoginForm {
	return LoginForm{Bind: []string{"https://login.example"}, SubmitSelector: "#bind", SuccessPrefix: "https://account.example/",
		Fields: []LoginField{{Selector: "#login", Value: fakeUser}, {Selector: "#password", Value: pass, Secret: true}}}
}

func loginSession(t *testing.T) (*loginChrome, *Session) {
	t.Helper()
	f := newLoginChrome(t)
	svc := &Service{NewEngine: DefaultEngine, Launch: LaunchEnv}
	s, err := svc.Acquire(context.Background(), Settings{CDPURL: f.srv.URL, Engine: "cdp"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Release)
	return f, s
}

var lkSites = domain.Sites{Domains: []string{"account.example"}}

func noValue(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), fakePass) {
		t.Fatalf("secret in error: %v", err)
	}
}

func TestLoginViaCDP(t *testing.T) {
	f, s := loginSession(t)
	ctx := context.Background()
	// Кабинет уводит на страницу входа: без сайтов входа страница закрывается…
	if _, err := s.Open(ctx, cabinet+"?anon", lkSites); !errors.Is(err, ErrDomain) {
		t.Fatal(err)
	}
	// …а с origin учётки вызывающего остаётся на странице входа.
	s.SetLoginOrigins([]string{"https://login.example"})
	p, err := s.Open(ctx, cabinet+"?anon", lkSites)
	if err != nil || !strings.HasPrefix(p.URL, signURL) {
		t.Fatal(p, err)
	}
	res, err := s.Login(ctx, loginForm(fakePass))
	if err != nil || res.Status != LoggedIn || res.URL != cabinet || res.Origin != "https://login.example" {
		t.Fatal(res, err)
	}
	if strings.Join(f.typed, "|") != fakeUser+"|"+fakePass {
		t.Fatal(f.typed)
	}
	// Переход в кабинет — новый документ: читать снова можно.
	if p, err := s.Observe(ctx, 0); err != nil || strings.Contains(p.Text, fakePass) {
		t.Fatal(p, err)
	}
}

func TestLoginChecksBeforeInput(t *testing.T) {
	f, s := loginSession(t)
	ctx := context.Background()
	s.SetLoginOrigins([]string{"https://login.example"})
	// Страница не на origin учётки — ничего не вводится.
	if _, err := s.Open(ctx, "https://account.example/about", lkSites); err != nil {
		t.Fatal(err)
	}
	_, err := s.Login(ctx, loginForm(fakePass))
	if !errors.Is(err, ErrWrongOrigin) || !strings.Contains(err.Error(), "https://account.example/about") {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, signURL, lkSites); err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string][]LoginField{
		"not found":          {{Selector: "#login", Value: fakeUser}, {Selector: "#nope", Value: fakePass, Secret: true}},
		"not visible":        {{Selector: "#login", Value: fakeUser}, {Selector: "#trap", Value: fakePass, Secret: true}},
		"not a password":     {{Selector: "#title", Value: fakePass, Secret: true}},
		"not a text field":   {{Selector: "#bind", Value: fakeUser}},
		"secret in login id": {{Selector: "#login", Value: fakePass, Secret: true}, {Selector: "#password", Value: fakePass, Secret: true}},
	} {
		form := loginForm(fakePass)
		form.Fields = fields
		_, err := s.Login(ctx, form)
		if !errors.Is(err, ErrFormMismatch) {
			t.Errorf("%s: %v", name, err)
		}
		noValue(t, err)
	}
	if n := f.typedCount(); n != 0 {
		t.Fatalf("typed %d values before the form was checked", n)
	}
	// type: text у поля владельца — секрет в текстовое поле можно.
	if _, err := s.FillSecret(ctx, []string{"https://login.example"}, LoginField{Selector: "#login", Value: fakeUser, Secret: true, AnyType: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FillSecret(ctx, []string{"https://other.example"}, LoginField{Selector: "#password", Value: fakePass, Secret: true}); !errors.Is(err, ErrWrongOrigin) {
		t.Fatal(err)
	}
}

func TestStillOnLoginAndSecretOnPage(t *testing.T) {
	f, s := loginSession(t)
	ctx := context.Background()
	s.SetLoginOrigins([]string{"https://login.example"})
	if _, err := s.Open(ctx, signURL, lkSites); err != nil {
		t.Fatal(err)
	}
	form := loginForm("wrong-pass-0000")
	form.Timeout = 300 * time.Millisecond
	res, err := s.Login(ctx, form)
	if err != nil || res.Status != StillOnLogin || res.URL != signURL {
		t.Fatal(res, err)
	}
	// Секрет на странице: читать её нельзя, пока документ тот же.
	if _, err := s.Observe(ctx, 0); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("observe:", err)
	}
	if _, err := s.Elements(ctx, "", nil); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("elements:", err)
	}
	if _, err := s.Network(ctx, "", false); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("network:", err)
	}
	// Снимок можно, но поля секрета и пароля закрашены.
	if _, err := s.ScreenshotElement(ctx, "#password"); err != nil || len(f.covered) != 1 ||
		!strings.Contains(fmt.Sprint(f.covered[0]), "#password") {
		t.Fatal("screenshot with covered secret fields:", err, f.covered)
	}
	if _, err := s.Extract(ctx, "password"); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("extract:", err)
	}
	if _, err := s.Act(ctx, ActRequest{Selector: "#title", Method: ActFill, Value: "x"}); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("act fill:", err)
	}
	if _, err := s.Act(ctx, ActRequest{Selector: "#title", Method: ActType, Value: "x"}); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("act type:", err)
	}
	// click, press, wait и fill_secret на той же странице можно.
	for _, r := range []ActRequest{{Selector: "#login", Method: ActWait}, {Selector: "#password", Method: ActPress, Value: "Tab"}, {Selector: "#bind", Method: ActClick}} {
		if _, err := s.Act(ctx, r); err != nil {
			t.Fatal(r, err)
		}
	}
	if _, err := s.FillSecret(ctx, form.Bind, LoginField{Selector: "#password", Value: fakePass, Secret: true}); err != nil {
		t.Fatal(err)
	}
	// Значение введённого секрета не возвращается ни в ответе act, ни через
	// подбор по тексту (text, expect_text, wait_for_text).
	for _, r := range []ActRequest{{Selector: "#password", Method: ActWait}, {Selector: "#password", Method: ActClick},
		{Selector: "#password", Method: ActPress, Value: "Tab"}} {
		res, err := s.Act(ctx, r)
		if err != nil || res.Text != "" || strings.Contains(fmt.Sprint(res), fakePass) {
			t.Fatal(r, res, err)
		}
	}
	if _, err := s.Act(ctx, ActRequest{Selector: "#password", Method: ActWait, Text: fakePass[:4], TimeoutMS: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatal("act text matches the secret:", err)
	}
	if _, err := s.Act(ctx, ActRequest{Selector: "#bind", Method: ActWait, ExpectSelector: "#password", ExpectText: fakePass[:4], ExpectMS: 1}); !errors.Is(err, ErrNoEffect) {
		t.Fatal("expect_text matches the secret:", err)
	}
	if err := s.WaitFor(ctx, Wait{Selector: "#password", Text: fakePass[:4], TimeoutMS: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatal("wait_for_text matches the secret:", err)
	}
	// Переход верхнего документа снимает режим.
	if _, err := s.Open(ctx, signURL, lkSites); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Observe(ctx, 0); err != nil {
		t.Fatal(err)
	}
}

func TestLoginWithoutSuccessPrefix(t *testing.T) {
	_, s := loginSession(t)
	ctx := context.Background()
	s.SetLoginOrigins([]string{"https://login.example"})
	if _, err := s.Open(ctx, signURL, lkSites); err != nil {
		t.Fatal(err)
	}
	form := loginForm(fakePass)
	form.SuccessPrefix, form.SubmitSelector, form.SubmitPress = "", "", "Enter"
	form.Timeout = 300 * time.Millisecond
	// Enter в последнем поле фейк не отправляет: адрес не менялся — still_on_login.
	res, err := s.Login(ctx, form)
	if err != nil || res.Status != StillOnLogin {
		t.Fatal(res, err)
	}
	if _, err := s.Open(ctx, signURL, lkSites); err != nil {
		t.Fatal(err)
	}
	form.SubmitSelector, form.SubmitPress = "#bind", ""
	res, err = s.Login(ctx, form)
	if err != nil || res.Status != Submitted || res.URL != cabinet {
		t.Fatal(res, err)
	}
}
