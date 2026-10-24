// SPDX-License-Identifier: AGPL-3.0-only

package testsite

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Без сессии кабинет уводит на вход; неверный пароль — 401 и форма снова;
// верный — кабинет; кнопка принимает заявку, результат виден; reset выходит.
func TestLoginCabinetAction(t *testing.T) {
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	if _, body := get("/"); !strings.Contains(body, "Электричество") || !strings.Contains(body, "noindex") {
		t.Fatalf("home: %s", body)
	}
	if _, body := get("/cabinet"); !strings.Contains(body, "<title>Вход</title>") || !strings.Contains(body, Password) {
		t.Fatalf("cabinet without a session: %s", body)
	}
	resp, _ := c.PostForm(srv.URL+"/login", url.Values{"login": {User}, "password": {"wrong"}, "back": {"/cabinet"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, _ = c.PostForm(srv.URL+"/login", url.Values{"login": {User}, "password": {Password}, "back": {"/cabinet"}})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "<title>Кабинет</title>") || !strings.Contains(string(b), "Заявок пока нет") {
		t.Fatalf("login: %d %s", resp.StatusCode, b)
	}
	resp, _ = c.PostForm(srv.URL+"/cabinet/action", nil)
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Заявка №1 принята") || s.Presses() != 1 {
		t.Fatalf("action: %s", b)
	}
	get("/reset")
	if _, body := get("/cabinet"); !strings.Contains(body, "<title>Вход</title>") {
		t.Fatalf("after reset: %s", body)
	}
}

// Вход на другом origin возвращает в кабинет одноразовым билетом.
func TestLoginAcrossOrigins(t *testing.T) {
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	s.CabinetURL = srv.URL
	s.LoginURL = strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := c.Get(srv.URL + "/cabinet")
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, s.LoginURL+"/login?back=/cabinet") {
		t.Fatalf("to login: %s", loc)
	}
	resp, _ = c.PostForm(s.LoginURL+"/login", url.Values{"login": {User}, "password": {Password}, "back": {"/cabinet"}})
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, srv.URL+"/cabinet?t=") {
		t.Fatalf("back to the cabinet: %s", loc)
	}
	resp, _ = c.Get(loc)
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) == 0 {
		t.Fatalf("ticket: %d %v", resp.StatusCode, resp.Cookies())
	}
	if resp, _ = c.Get(loc); resp.StatusCode != http.StatusFound {
		t.Fatalf("a ticket works once: %d", resp.StatusCode)
	}
}
