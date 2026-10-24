// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// waitEngine — страница, на которой нужного элемента нет: адреса по очереди
// (последний остаётся), документ ещё грузится (loading).
type waitEngine struct {
	closableEngine
	urls    []string
	found   bool
	loading bool
}

func (e *waitEngine) URL(context.Context) (string, error) {
	u := e.urls[0]
	if len(e.urls) > 1 {
		e.urls = e.urls[1:]
	}
	return u, nil
}

func (e *waitEngine) Eval(_ context.Context, expr string, out any) error {
	switch {
	case strings.Contains(expr, "ready_state"):
		return json.Unmarshal([]byte(`{"url":"`+e.urls[0]+`?token=abc","title":"Grafana","ready_state":"interactive","pending_requests":3,"text":"Loading…"}`), out)
	case expr == idleJS:
		return json.Unmarshal([]byte(map[bool]string{true: "false", false: "true"}[e.loading]), out)
	case strings.Contains(expr, "__gate4aiSecret"):
		return json.Unmarshal([]byte("false"), out)
	}
	if e.found {
		return json.Unmarshal([]byte(`{"text":"ok"}`), out)
	}
	return json.Unmarshal([]byte("null"), out)
}

func waitExecutor(urls ...string) (*Executor, *waitEngine) {
	e := &waitEngine{urls: urls}
	return &Executor{Engine: e, Sites: domain.Sites{Domains: []string{"*.grafana.net"}}, LoginOrigins: []string{"https://grafana.com"}}, e
}

// Таймаут ожидания отдаёт состояние страницы: адрес (без токенов), заголовок,
// readyState, незавершённые запросы и начало текста.
func TestWaitTimeoutState(t *testing.T) {
	x, _ := waitExecutor("https://a.grafana.net/alerting/list")
	start := time.Now()
	err := x.WaitFor(context.Background(), Wait{Text: "Alert rules", TimeoutMS: 300, From: "https://a.grafana.net/alerting/list"})
	if !errors.Is(err, ErrNotFound) || time.Since(start) < 300*time.Millisecond {
		t.Fatal(err)
	}
	for _, want := range []string{`wait_for_text "Alert rules"`, `"ready_state":"interactive"`, `"pending_requests":3`, `"title":"Grafana"`, `"text":"Loading…"`, "token=***", "timeout_ms up to 60000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("no %s in %v", want, err)
		}
	}
}

// Уход на страницу или сайт входа прерывает ожидание сразу.
func TestWaitRedirected(t *testing.T) {
	for _, to := range []string{"https://a.grafana.net/login", "https://grafana.com/auth/sign-in", "https://a.grafana.net/#/signin", "https://evil.example/"} {
		x, _ := waitExecutor("https://a.grafana.net/alerting/list", to)
		start := time.Now()
		err := x.WaitFor(context.Background(), Wait{Selector: "#rules", TimeoutMS: 5000, From: "https://a.grafana.net/alerting/list"})
		if !errors.Is(err, ErrRedirected) || time.Since(start) > time.Second || !strings.Contains(err.Error(), "sign in") {
			t.Errorf("%s: %v", to, err)
		}
	}
	// Переход SPA на другой путь сайта — не вход: ждём дальше.
	x, _ := waitExecutor("https://a.grafana.net/alerting/list", "https://a.grafana.net/alerting/new")
	if err := x.WaitFor(context.Background(), Wait{Selector: "#rules", TimeoutMS: 300, From: "https://a.grafana.net/alerting/list"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// Ждали именно страницу входа — её адрес и есть результат.
	x, _ = waitExecutor("https://a.grafana.net/alerting/list", "https://a.grafana.net/login")
	if err := x.WaitFor(context.Background(), Wait{URL: "/login", From: "https://a.grafana.net/alerting/list"}); err != nil {
		t.Fatal(err)
	}
}

// wait_for_url: шаблон с * — весь адрес, без * — подстрока; вместе с элементом.
func TestWaitForURL(t *testing.T) {
	x, e := waitExecutor("https://a.grafana.net/a", "https://a.grafana.net/a", "https://a.grafana.net/alerting/list?view=list")
	if err := x.WaitFor(context.Background(), Wait{URL: "https://a.grafana.net/alerting/*", TimeoutMS: 2000}); err != nil {
		t.Fatal(err)
	}
	e.urls = []string{"https://a.grafana.net/x"}
	if err := x.WaitFor(context.Background(), Wait{URL: "https://a.grafana.net/alerting/*", TimeoutMS: 200}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "wait_for_url") {
		t.Fatal(err)
	}
	e.urls, e.found = []string{"https://a.grafana.net/alerting/list"}, true
	if err := x.WaitFor(context.Background(), Wait{URL: "alerting/list", Text: "Rules"}); err != nil {
		t.Fatal(err)
	}
	if !globRe("https://a/*/b").MatchString("https://a/x/y/b") || globRe("https://a/*/b").MatchString("https://a/x/b/c") {
		t.Fatal("glob")
	}
}

// Долгая загрузка: timeout_ms до 30 с и больше работает, а open сообщает,
// что страница не догрузилась за IdleMax.
func TestWaitLongAndStillLoading(t *testing.T) {
	if testing.Short() {
		t.Skip("waits")
	}
	x, e := waitExecutor("https://a.grafana.net/a")
	go func() { time.Sleep(11 * time.Second); e.found = true }()
	if err := x.WaitFor(context.Background(), Wait{Text: "Rules", TimeoutMS: 30000}); err != nil {
		t.Fatal("a wait longer than the default 10 s:", err)
	}
	old := IdleMax
	IdleMax = 300 * time.Millisecond
	defer func() { IdleMax = old }()
	x, e = waitExecutor("https://a.grafana.net/a")
	e.loading = true
	if _, err := x.Open(context.Background(), "https://a.grafana.net/a"); err != nil || !x.StillLoading {
		t.Fatal("still loading:", x.StillLoading, err)
	}
	e.loading = false
	if _, err := x.Open(context.Background(), "https://a.grafana.net/a"); err != nil || x.StillLoading {
		t.Fatal("loaded:", x.StillLoading, err)
	}
}
