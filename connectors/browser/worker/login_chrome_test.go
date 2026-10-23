// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// signPage — страница входа: логин, пароль, скрытое поле-ловушка для
// автозаполнения; отправка — POST формы.
const signPage = `<!doctype html><meta charset=utf-8><title>Вход</title>
<form method=post action=/session>
<input type=hidden name=back value="%s">
<label>Логин <input id=login name=login></label>
<label>Пароль <input id=password name=password type=password></label>
<input id=trap name=trap type=password style="position:absolute;left:-9999px;width:0;height:0">
<button id=bind type=submit>Войти</button>
</form>`

// loginSites — сайт входа (localhost) и кабинет (127.0.0.1) — разные origin;
// кабинет без сеанса уводит на вход, вход при верных значениях — в кабинет с
// одноразовым билетом.
type loginSites struct {
	sign, lk *httptest.Server
	mu       sync.Mutex
	tickets  map[string]bool
	trapped  bool
}

func newLoginSites(t *testing.T, user, pass string) *loginSites {
	s := &loginSites{tickets: map[string]bool{}}
	s.lk = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.mu.Lock()
		ok := s.tickets[r.URL.Query().Get("t")]
		delete(s.tickets, r.URL.Query().Get("t"))
		s.mu.Unlock()
		if c, err := r.Cookie("lk"); ok || (err == nil && c.Value == "1") {
			http.SetCookie(w, &http.Cookie{Name: "lk", Value: "1", Path: "/"})
			fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>Кабинет</title><p>Кабинет: добро пожаловать</p>")
			return
		}
		http.Redirect(w, r, s.signURL()+"/sign?back="+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(s.lk.Close)
	s.sign = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodPost && r.URL.Path == "/session" {
			r.ParseForm()
			if r.Form.Get("trap") != "" {
				s.mu.Lock()
				s.trapped = true
				s.mu.Unlock()
			}
			if r.Form.Get("login") == user && r.Form.Get("password") == pass {
				s.mu.Lock()
				s.tickets["t1"] = true
				s.mu.Unlock()
				http.Redirect(w, r, s.lk.URL+r.Form.Get("back")+"?t=t1", http.StatusFound)
				return
			}
			fmt.Fprintf(w, signPage+"<p>Неверный логин или пароль</p>", r.Form.Get("back"))
			return
		}
		fmt.Fprintf(w, signPage, r.URL.Query().Get("back"))
	}))
	t.Cleanup(s.sign.Close)
	return s
}

// signURL — сайт входа по имени localhost (другой origin, чем 127.0.0.1 кабинета).
func (s *loginSites) signURL() string {
	return strings.Replace(s.sign.URL, "127.0.0.1", "localhost", 1)
}

// TestLoginRealChrome — browser.login в настоящем Chrome (headless): редирект
// с кабинета на вход на другом origin, ввод учётки, отправка, кабинет.
func TestLoginRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	const user, pass = "user-1", "fake-pass-1234"
	sites := newLoginSites(t, user, pass)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cdpURL, proc, err := Launch(ctx, wrapper, filepath.Join(dir, "profile"))
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	defer StopChrome(proc, 3*time.Second)
	eng, err := DialCDP(ctx, cdpURL)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	x := &Executor{Engine: eng, Sites: domain.Sites{Domains: []string{"127.0.0.1"}}}
	bind := []string{sites.signURL()}
	// Без учётки редирект на сайт входа закрывает страницу.
	if _, err := x.Open(ctx, sites.lk.URL+"/home"); !errors.Is(err, ErrDomain) {
		t.Fatal(err)
	}
	x.LoginOrigins = bind
	u, err := x.Open(ctx, sites.lk.URL+"/home")
	if err != nil || !strings.HasPrefix(u, sites.signURL()+"/sign") {
		t.Fatal(u, err)
	}
	form := LoginForm{Bind: bind, SubmitSelector: "#bind", SuccessPrefix: sites.lk.URL + "/", Timeout: 2 * time.Second,
		Fields: []LoginField{{Selector: "#login", Value: user, AnyType: true}, {Selector: "#password", Value: "wrong-pass-0000", Secret: true}}}
	// Неверный пароль: сайт входа ответил новым документом (секрета на нём нет).
	res, err := x.Login(ctx, form)
	if err != nil || res.Status != StillOnLogin {
		t.Fatal(res, err)
	}
	if _, _, text, _, err := x.Observe(ctx, 0); err != nil || !strings.Contains(text, "Неверный") || strings.Contains(text, "wrong-pass") {
		t.Fatal("observe after a wrong password:", text, err)
	}
	// Секрет введён без отправки: страницу читать нельзя, кликать можно.
	if _, err := x.FillSecret(ctx, bind, LoginField{Selector: "#password", Value: pass, Secret: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := x.Observe(ctx, 0); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("observe with a secret on the page:", err)
	}
	// Значение секрета не возвращается агенту: ни в ответе act (в том числе
	// поля type: text, куда секрет ввёл хост), ни подбором по тексту.
	if _, err := x.FillSecret(ctx, bind, LoginField{Selector: "#login", Value: user, Secret: true, AnyType: true}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []ActRequest{{Selector: "#password", Method: ActWait}, {Selector: "#login", Method: ActWait},
		{Selector: "#password", Method: ActPress, Value: "Tab"}, {Selector: "#bind", Method: ActWait}} {
		if res, err := x.Act(ctx, r); err != nil || res.Text != "" {
			t.Fatal("act with a secret on the page:", r, res, err)
		}
	}
	for _, text := range []string{pass[:4], user[:4]} {
		if _, err := x.Act(ctx, ActRequest{Selector: "input", Method: ActWait, Text: text, TimeoutMS: 1}); !errors.Is(err, ErrNotFound) {
			t.Fatal("act text matches the secret:", text, err)
		}
		if err := x.WaitFor(ctx, Wait{Text: text, TimeoutMS: 1}); !errors.Is(err, ErrNotFound) {
			t.Fatal("wait_for_text matches the secret:", text, err)
		}
		if _, err := x.Act(ctx, ActRequest{Selector: "#bind", Method: ActWait, ExpectText: text, ExpectMS: 1}); !errors.Is(err, ErrNoEffect) {
			t.Fatal("expect_text matches the secret:", text, err)
		}
	}
	// Кнопку по надписи найти можно.
	if _, err := x.Act(ctx, ActRequest{Selector: "button", Method: ActWait, Text: "войти"}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Elements(ctx, "", nil); !errors.Is(err, ErrSecretOnPage) {
		t.Fatal("elements with a secret on the page:", err)
	}
	// Снимок — с закрашенным полем пароля: ни одного светлого пикселя.
	png, err := x.Screenshot(ctx, "#password")
	if err != nil {
		t.Fatal("screenshot with a secret on the page:", err)
	}
	if light := lightPixels(t, png); light > 0 {
		t.Fatalf("the password field is not covered: %d light pixels", light)
	}
	var covers int
	if eng.Eval(ctx, "document.querySelectorAll('[data-g4-cover]').length", &covers); covers != 0 {
		t.Fatal("covers stay on the page:", covers)
	}
	// Скрипт страницы не снимает метку.
	var gone bool
	eng.Eval(ctx, "(() => { try { delete window.__gate4aiSecret; window.__gate4aiSecret = 1; } catch (_) {} return window.__gate4aiSecret === undefined; })()", &gone)
	if !x.SecretOnPage(ctx) {
		t.Fatal("the page removed the secret mark")
	}
	// Ловушка (поле password нулевого размера) не подходит для ввода.
	trap := form
	trap.Fields = []LoginField{{Selector: "#trap", Value: pass, Secret: true}}
	if _, err := x.Login(ctx, trap); !errors.Is(err, ErrFormMismatch) || !strings.Contains(err.Error(), "not visible") {
		t.Fatal(err)
	}
	// Пароль — только в input[type=password].
	wrong := form
	wrong.Fields = []LoginField{{Selector: "#login", Value: pass, Secret: true}}
	if _, err := x.Login(ctx, wrong); !errors.Is(err, ErrFormMismatch) || !strings.Contains(err.Error(), "not a password field") {
		t.Fatal(err)
	}
	form.Fields[1].Value = pass
	res, err = x.Login(ctx, form)
	if err != nil || res.Status != LoggedIn || !strings.HasPrefix(res.URL, sites.lk.URL+"/home") || res.Origin != domain.OriginOf(sites.signURL()) {
		t.Fatal(res, err)
	}
	_, title, text, _, err := x.Observe(ctx, 0)
	if err != nil || title != "Кабинет" || !strings.Contains(text, "добро пожаловать") || strings.Contains(text, pass) {
		t.Fatal(title, text, err)
	}
	if sites.trapped {
		t.Fatal("the hidden trap field got a value")
	}
	// Страница не на сайте входа — wrong_origin.
	if _, err := x.Login(ctx, form); !errors.Is(err, ErrWrongOrigin) {
		t.Fatal(err)
	}
}

// lightPixels — сколько пикселей снимка светлее тёмно-серого.
func lightPixels(t *testing.T, b []byte) int {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 > 40 || g>>8 > 40 || bl>>8 > 40 {
				n++
			}
		}
	}
	return n
}
