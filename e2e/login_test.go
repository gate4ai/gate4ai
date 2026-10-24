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
	"testing"

	"github.com/gate4ai/gate4ai/e2e/testsite"
)

// Тестовый сайт на двух origin: вход на localhost, кабинет на 127.0.0.1 —
// кабинет без сеанса уводит на вход, вход при верных значениях возвращает в
// кабинет с одноразовым билетом (testsite.LoginURL / CabinetURL).
type sites struct {
	site *testsite.Site
	lk   *httptest.Server
}

func newSites(t *testing.T) *sites {
	s := &sites{site: testsite.New()}
	s.lk = httptest.NewServer(s.site.Handler())
	t.Cleanup(s.lk.Close)
	s.site.CabinetURL = s.lk.URL
	s.site.LoginURL = s.signURL()
	return s
}

func (s *sites) signURL() string { return strings.Replace(s.lk.URL, "127.0.0.1", "localhost", 1) }

// Вход по учётке из Vault: агент открывает кабинет, сайт уводит на вход
// (страница остаётся — это сайт входа его учётки), browser.login вводит поля
// и отправляет форму, агент видит кабинет; затем то же из коннектора
// (calls: [browser.login], учётка по слоту); кнопка в кабинете — действие
// press класса write: ждёт подтверждения владельца, результат проверяется по
// странице (verified). Значение пароля не попадает ни в ответы, ни в журнал.
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
	g.installFrom(src(t, "e2e/testconnector"))
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	cfg := fmt.Sprintf(`agents:
  claude-ai: { profile: assistant }
profiles:
  assistant:
    connectors: [browser, testconnector]
    rules:
      - { id: browser-ok, action: "browser.*", effect: allow }
      - { id: login-ok, action: browser.login, effect: allow } # browser.* вход не покрывает
      - { id: cabinet-ok, action: testconnector.cabinet, effect: allow }
connectors:
  testconnector: { domains: ["127.0.0.1", localhost], site: %q, login_site: %q, credentials: { site: "@site" } }
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
`, s.lk.URL, s.signURL(), wrapper, filepath.Join(dir, "profile"), s.signURL(), testsite.User, s.lk.URL+"/")
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0o600)
	if r := g.run("", "", "policy", "set", cfgFile, "-m", "login e2e"); r.code != 0 {
		t.Fatalf("policy: %d %s", r.code, r.out)
	}
	if r := g.run("", testsite.Password, "vault", "set", "@site.password"); r.code != 0 {
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

	open := run("browser", "open", "--url", s.lk.URL+"/cabinet")
	session, _ := open["session"].(string)
	lr, _ := open["login_required"].(map[string]any)
	if !strings.HasPrefix(fmt.Sprint(open["url"]), s.signURL()+"/login") || lr["account"] != "@site" {
		t.Fatalf("open: %v", open)
	}
	login := run("browser", "login", "--session", session, "--account", "@site")
	if login["status"] != "logged_in" || !strings.HasPrefix(fmt.Sprint(login["url"]), s.lk.URL+"/cabinet") {
		t.Fatalf("login: %v", login)
	}
	page := run("browser", "observe", "--session", session)
	if page["title"] != "Кабинет" || !strings.Contains(fmt.Sprint(page["text"]), "1 234,56") {
		t.Fatalf("observe: %v", page)
	}
	run("browser", "close", "--session", session)

	// Коннектор: сеанс кабинета сброшен — снова вход, теперь из действия коннектора.
	http.Get(s.lk.URL + "/reset")
	cab := run("testconnector", "cabinet")
	if cab["status"] != "logged_in" || cab["title"] != "Кабинет" {
		t.Fatalf("connector: %v", cab)
	}

	// Кнопка в кабинете: write без правила — подтверждение владельца; после
	// него действие исполняется, verify сверяет номер заявки со страницей.
	r := g.run(token, "", "testconnector", "press")
	outs = append(outs, r.out)
	pending := r.json(t)
	if r.code == 0 || errOf(pending)["code"] != "pending_approval" {
		t.Fatalf("press without approval: %d %s", r.code, r.out)
	}
	id, _ := pending["id"].(string)
	if r := g.run("", "", "approvals", "approve", id); r.code != 0 {
		t.Fatalf("approve: %d %s", r.code, r.out)
	}
	r = g.run(token, "", "status", id, "--wait", "60s")
	outs = append(outs, r.out)
	done := r.json(t)
	result, _ := done["result"].(map[string]any)
	if r.code != 0 || done["status"] != "ok" || done["verified"] != true || result["number"] != float64(1) || result["result"] != "Заявка №1 принята" {
		t.Fatalf("press: %d %s", r.code, r.out)
	}
	if s.site.Presses() != 1 {
		t.Fatalf("the site got %d presses", s.site.Presses())
	}

	log := g.run("", "", "log", "--json")
	outs = append(outs, log.out)
	if !strings.Contains(log.out, "secret.use") || !strings.Contains(log.out, "@site.password") {
		t.Fatalf("journal has no secret.use: %s", log.out)
	}
	for _, o := range outs {
		if strings.Contains(o, testsite.Password) {
			t.Fatalf("the password leaked: %s", o)
		}
	}
}
