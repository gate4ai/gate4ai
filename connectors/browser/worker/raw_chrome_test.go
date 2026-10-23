// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// apiSite — сайт с API за cookie сеанса: страница ставит cookie, API отдаёт
// JSON и удаляет правило; /bounce через полсекунды уходит на /login.
type apiSite struct {
	mu      sync.Mutex
	deleted []string
	posted  []string
}

func (a *apiSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	signed := false
	if c, err := r.Cookie("sid"); err == nil && c.Value == "s1" {
		signed = true
	}
	switch {
	case r.URL.Path == "/":
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "s1", Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><title>Alerting</title><p>Alert rules</p>"))
	case r.URL.Path == "/bounce":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><title>Loading</title><p>Loading…</p><script>setTimeout(() => location.href = '/login', 500)</script>"))
	case r.URL.Path == "/login":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><title>Sign in</title><form><input type=password></form>"))
	case r.URL.Path == "/text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("plain\nlines"))
	case !signed:
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Unauthorized"}`))
	case r.URL.Path == "/api/v1/provisioning/alert-rules" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "r-1")
		w.Header().Set("X-Auth-Token", "hidden")
		w.Write([]byte(`[{"uid":"a1","title":"High CPU","api_token":"t0k"}]`))
	case r.URL.Path == "/api/v1/provisioning/alert-rules" && r.Method == http.MethodPost:
		b := make([]byte, 1000)
		n, _ := r.Body.Read(b)
		a.mu.Lock()
		a.posted = append(a.posted, r.Header.Get("Content-Type")+" "+string(b[:n]))
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"uid":"a2"}`))
	case strings.HasPrefix(r.URL.Path, "/api/v1/provisioning/alert-rules/") && r.Method == http.MethodDelete:
		a.mu.Lock()
		a.deleted = append(a.deleted, strings.TrimPrefix(r.URL.Path, "/api/v1/provisioning/alert-rules/"))
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/moved":
		http.Redirect(w, r, "/api/v1/provisioning/alert-rules", http.StatusTemporaryRedirect)
	default:
		http.NotFound(w, r)
	}
}

// docOnly — CDP без тела ответа документа: текст JSON-страницы — из DOM
// (как у Stagehand), без просмотрщика Chrome.
type docOnly struct{ *CDP }

func (docOnly) DocumentBody(context.Context) (string, bool) { return "", false }

// JSON-страница читается как прислал сервер (без просмотрщика Chrome),
// токены скрыты; запросы со страницы — с её cookies, по списку сайтов.
func TestRawAndFetchRealChrome(t *testing.T) {
	site := &apiSite{}
	x, eng, base, ctx := chromeServer(t, site)
	api := base + "/api/v1/provisioning/alert-rules"

	if _, err := x.Open(ctx, api); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Engine{eng, docOnly{eng}} {
		x.Engine = e
		b, ok, err := x.RawDocument(ctx, 10000)
		if err != nil || !ok || b.ContentType != "application/json" || b.Body != `[{"api_token":"***","title":"High CPU","uid":"a1"}]` || b.JSON != nil {
			t.Fatalf("%T: %+v %v %v", e, b, ok, err)
		}
	}
	x.Engine = eng
	if b, _, _ := x.RawDocument(ctx, 5); !b.Truncated || len([]rune(b.Body)) != 5 {
		t.Fatal("max chars:", b)
	}
	if _, err := x.Open(ctx, base+"/text"); err != nil {
		t.Fatal(err)
	}
	if b, ok, _ := x.RawDocument(ctx, 100); !ok || b.Body != "plain\nlines" || !strings.HasPrefix(b.ContentType, "text/plain") {
		t.Fatal(b)
	}
	if _, err := x.Open(ctx, base+"/"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := x.RawDocument(ctx, 100); ok {
		t.Fatal("an HTML page is read as text")
	}

	res, err := x.Fetch(ctx, FetchRequest{Method: "GET", URL: api})
	if err != nil || res.Status != 200 || res.ContentType != "application/json" || res.Size != 51 {
		t.Fatalf("%+v %v", res, err)
	}
	rules, _ := res.JSON.([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["api_token"] != "***" || res.Headers["x-request-id"] != "r-1" || res.Headers["x-auth-token"] != "" {
		t.Fatalf("parsed JSON, tokens and auth headers hidden: %+v", res)
	}
	body := `{"title":"Disk"}`
	if res, err = x.Fetch(ctx, FetchRequest{Method: "POST", URL: api, Body: &body, Headers: map[string]string{"Content-Type": "application/json"}}); err != nil || res.Status != 201 {
		t.Fatal(res, err)
	}
	if res, err = x.Fetch(ctx, FetchRequest{Method: "DELETE", URL: api + "/a1"}); err != nil || res.Status != 204 || res.Size != 0 {
		t.Fatal(res, err)
	}
	if len(site.deleted) != 1 || site.deleted[0] != "a1" || len(site.posted) != 1 || site.posted[0] != `application/json {"title":"Disk"}` {
		t.Fatal("requests went with the page's cookies:", site.deleted, site.posted)
	}
	// Запрос с эффектом не повторяется на адрес редиректа (307 повторил бы метод и тело).
	if res, err = x.Fetch(ctx, FetchRequest{Method: "POST", URL: base + "/api/moved", Body: &body}); err != nil || !res.Redirect || len(site.posted) != 1 {
		t.Fatal("redirect of a write:", res, err, site.posted)
	}
	if res, err = x.Fetch(ctx, FetchRequest{Method: "GET", URL: base + "/api/moved"}); err != nil || res.Status != 200 || !strings.HasSuffix(res.URL, "/alert-rules") {
		t.Fatal("a read follows redirects:", res, err)
	}
	if _, err = x.Fetch(ctx, FetchRequest{Method: "DELETE", URL: "https://evil.example/api"}); !errors.Is(err, ErrDomain) {
		t.Fatal("site list:", err)
	}
	if _, err = x.Fetch(ctx, FetchRequest{Method: "GET", URL: "http://127.0.0.2:1/"}); err == nil {
		t.Fatal("site list or network error")
	}
}

// Пока ждём содержимое, страница уходит на /login: redirected сразу, а не по таймауту.
func TestWaitRedirectedRealChrome(t *testing.T) {
	x, _, base, ctx := chromeServer(t, &apiSite{})
	if _, err := x.Open(ctx, base+"/bounce"); err != nil {
		t.Fatal(err)
	}
	err := x.WaitFor(ctx, Wait{Text: "Alert rules", TimeoutMS: 20000, From: base + "/bounce"})
	if !errors.Is(err, ErrRedirected) || !strings.Contains(err.Error(), "/login") || !strings.Contains(err.Error(), `"title":"Sign in"`) {
		t.Fatal(err)
	}
	if _, err := x.Open(ctx, base+"/"); err != nil {
		t.Fatal(err)
	}
	err = x.WaitFor(ctx, Wait{Selector: "#none", TimeoutMS: 300, From: base + "/"})
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), `"ready_state":"complete"`) || !strings.Contains(err.Error(), `"text":"Alert rules"`) {
		t.Fatal(err)
	}
}
