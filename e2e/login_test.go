// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Фейковые сайт входа (localhost) и кабинет (127.0.0.1) — разные origin:
// кабинет без сеанса уводит на вход, вход при верных значениях возвращает в
// кабинет с одноразовым билетом. Значения — фейковые.
const (
	e2eUser = "user-e2e"
	e2ePass = "fake-pass-e2e-4242"
)

type sites struct {
	sign, lk *httptest.Server
	mu       sync.Mutex
	gen      int // поколение сеансов кабинета: reset выходит из всех
	tickets  map[string]bool
}

func newSites(t *testing.T) *sites {
	s := &sites{tickets: map[string]bool{}, gen: 1}
	s.lk = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/reset" {
			s.gen++
			return
		}
		cookie := fmt.Sprint(s.gen)
		t := r.URL.Query().Get("t")
		c, err := r.Cookie("lk")
		if s.tickets[t] || (err == nil && c.Value == cookie) {
			delete(s.tickets, t)
			http.SetCookie(w, &http.Cookie{Name: "lk", Value: cookie, Path: "/"})
			fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>Кабинет</title><p>Кабинет: долг 1 234,56 ₽</p>")
			return
		}
		http.Redirect(w, r, s.signURL()+"/sign?back="+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(s.lk.Close)
	s.sign = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodPost {
			r.ParseForm()
			if r.Form.Get("login") == e2eUser && r.Form.Get("password") == e2ePass {
				s.mu.Lock()
				s.tickets["t1"] = true
				s.mu.Unlock()
				http.Redirect(w, r, s.lk.URL+r.Form.Get("back")+"?t=t1", http.StatusFound)
				return
			}
		}
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>Вход</title><form method=post action=/session>
<input type=hidden name=back value="%s"><input id=login name=login><input id=password name=password type=password>
<button id=bind>Войти</button></form>`, r.URL.Query().Get("back"))
	}))
	t.Cleanup(s.sign.Close)
	return s
}

func (s *sites) signURL() string { return strings.Replace(s.sign.URL, "127.0.0.1", "localhost", 1) }

// Вход по учётке из Vault: агент открывает кабинет, сайт уводит на вход
// (страница остаётся — это сайт входа его учётки), browser.login вводит поля
// и отправляет форму, агент видит кабинет; затем то же из коннектора
// (calls: [browser.login], учётка по слоту). Значение пароля не попадает ни
// в ответы, ни в журнал.
func TestBrowserLoginByAccount(t *testing.T) {
	t.Parallel()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		if chrome, err = exec.LookPath("chromium"); err != nil {
			t.Skip("needs Chrome")
		}
	}
	s := newSites(t)
	g := startGate(t)
	reader, _ := filepath.Abs(filepath.Join("testdata", "pagereader"))
	g.installFrom(reader)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	cfg := fmt.Sprintf(`agents:
  claude-ai: { profile: assistant }
profiles:
  assistant:
    connectors: [browser, pagereader]
    rules:
      - { id: browser-ok, action: "browser.*", effect: allow }
      - { id: login-ok, action: browser.login, effect: allow } # browser.* вход не покрывает
      - { id: cabinet-ok, action: pagereader.cabinet, effect: allow }
connectors:
  pagereader: { domains: ["127.0.0.1", localhost], login_site: %q, credentials: { site: "@site" } }
  browser:
    engine: cdp
    chrome_path: %q
    profile_dir: %q
access:
  sites: [{ domain: "127.0.0.1", access: allow }, { domain: localhost, access: allow }]
credentials:
  "@site":
    bind: [%q]
    form:
      fields:
        - { selector: "#login", value: %q }
        - { selector: "#password", secret: "@site.password" }
      submit: { selector: "#bind" }
      success: { url_prefix: %q }
    use: [assistant]
`, s.signURL(), wrapper, filepath.Join(dir, "profile"), s.signURL(), e2eUser, s.lk.URL+"/")
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0o600)
	if r := g.run("", "", "policy", "set", cfgFile, "-m", "login e2e"); r.code != 0 {
		t.Fatalf("policy: %d %s", r.code, r.out)
	}
	if r := g.run("", e2ePass, "vault", "set", "@site.password"); r.code != 0 {
		t.Fatalf("vault: %d %s", r.code, r.out)
	}
	token := g.run("", "", "agents", "token", "claude-ai").json(t)["token"].(string)
	var outs []string
	run := func(args ...string) map[string]any {
		t.Helper()
		r := g.run(token, "", args...)
		outs = append(outs, r.out)
		res := r.json(t)
		if r.code != 0 || res["status"] != "ok" {
			t.Fatalf("%v: %d %s", args, r.code, r.out)
		}
		m, _ := res["result"].(map[string]any)
		return m
	}

	open := run("browser", "open", "--url", s.lk.URL+"/home")
	session, _ := open["session"].(string)
	lr, _ := open["login_required"].(map[string]any)
	if !strings.HasPrefix(fmt.Sprint(open["url"]), s.signURL()+"/sign") || lr["account"] != "@site" {
		t.Fatalf("open: %v", open)
	}
	login := run("browser", "login", "--session", session, "--account", "@site")
	if login["status"] != "logged_in" || !strings.HasPrefix(fmt.Sprint(login["url"]), s.lk.URL+"/home") {
		t.Fatalf("login: %v", login)
	}
	page := run("browser", "observe", "--session", session)
	if page["title"] != "Кабинет" || !strings.Contains(fmt.Sprint(page["text"]), "1 234,56") {
		t.Fatalf("observe: %v", page)
	}
	run("browser", "close", "--session", session)

	// Коннектор: сеанс кабинета сброшен — снова вход, теперь из действия коннектора.
	http.Get(s.lk.URL + "/reset")
	cab := run("pagereader", "cabinet", "--url", s.lk.URL+"/home")
	if cab["status"] != "logged_in" || cab["title"] != "Кабинет" {
		t.Fatalf("connector: %v", cab)
	}

	log := g.run("", "", "log", "--json")
	outs = append(outs, log.out)
	if !strings.Contains(log.out, "secret.use") || !strings.Contains(log.out, "@site.password") {
		t.Fatalf("journal has no secret.use: %s", log.out)
	}
	for _, o := range outs {
		if strings.Contains(o, e2ePass) {
			t.Fatalf("the password leaked: %s", o)
		}
	}
}
