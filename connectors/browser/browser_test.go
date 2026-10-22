// SPDX-License-Identifier: AGPL-3.0-only

package main

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

	"github.com/gate4ai/gate4ai/connectors/browser/worker"
)

// fakeEngine — страница без Chrome: переходы, текст, поиск элемента для act.
type fakeEngine struct {
	url   string
	keys  []string
	hooks int
}

func (f *fakeEngine) Navigate(_ context.Context, u string) error {
	if strings.HasSuffix(u, "/redirect") {
		u = "https://evil.example/"
	}
	f.url = u
	return nil
}
func (f *fakeEngine) URL(context.Context) (string, error) { return f.url, nil }
func (f *fakeEngine) Title(context.Context) (string, error) {
	return "Личный кабинет", nil
}
func (f *fakeEngine) Text(context.Context) (string, error) {
	return "Задолженность 1 234,56 ₽", nil
}
func (f *fakeEngine) Screenshot(_ context.Context, sel string) ([]byte, error) {
	return []byte("\x89PNG-fake"), nil
}
func (f *fakeEngine) Extract(context.Context, string) (any, error) {
	return map[string]any{"debt": 1234.56}, nil
}
func (f *fakeEngine) Eval(_ context.Context, expr string, out any) error {
	switch {
	case strings.Contains(expr, "XMLHttpRequest.prototype.send ="): // перехват запросов
		f.hooks++
		return json.Unmarshal([]byte("true"), out)
	case strings.Contains(expr, "performance.getEntriesByType"): // список запросов
		return json.Unmarshal([]byte(`[{"kind":"fetch","url":"https://a.example.com/api/bill"},{"kind":"fetch","url":"https://mc.yandex.ru/watch?ua=x"},
			{"kind":"fetch","method":"GET","url":"https://a.example.com/api/bill?id=1","status":200,"mime":"application/json","body":"{\"debt\":10,\"access_token\":\"t0k\"}","size":36},
			{"kind":"xhr","method":"POST","url":"https://a.example.com/auth/login","status":200,"mime":"application/json","body":"{\"token\":\"t0k\"}","size":15}]`), out)
	case strings.Contains(expr, "nth-of-type"): // интерактивные элементы
		return json.Unmarshal([]byte(`[{"selector":"#pay","tag":"button","type":"submit","text":"Оплатить"}]`), out)
	}
	if strings.Contains(expr, "#missing") {
		return json.Unmarshal([]byte("null"), out)
	}
	if strings.Contains(expr, "#away") {
		f.url = "https://evil.example/"
	}
	return json.Unmarshal([]byte(`{"text":"ok"}`), out)
}
func (f *fakeEngine) InsertText(_ context.Context, t string) error {
	f.keys = append(f.keys, t)
	return nil
}
func (f *fakeEngine) Key(_ context.Context, k string) error { f.keys = append(f.keys, k); return nil }
func (f *fakeEngine) Close() error                          { return nil }

func setup(t *testing.T, settings map[string]any) (*hosttest.Host, *Browser, *fakeEngine) {
	t.Helper()
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{}
	b := &Browser{W: &worker.Service{NewEngine: func(context.Context, worker.Settings, string) (worker.Engine, error) { return eng, nil }}}
	if settings == nil {
		settings = map[string]any{"allowed_domains": []any{"*.example.com", "app.example.org"}, "cdp_url": "http://127.0.0.1:1"}
	}
	return hosttest.New(m, settings, nil), b, eng
}

func TestAgentReadScreenshotExtract(t *testing.T) {
	h, b, _ := setup(t, nil)
	res, err := h.Execute(b, "read", map[string]any{"url": "https://app.example.org/"})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Structured.(map[string]any)
	if p["title"] != "Личный кабинет" || !strings.Contains(res.Content[0].Text, "1 234,56") || res.Source != "web:https://app.example.org/" {
		t.Fatalf("%+v", res)
	}
	res, err = h.Execute(b, "screenshot", map[string]any{"url": "https://app.example.org/", "selector": "#debt"})
	if err != nil || string(res.Evidence[0].Data) != "\x89PNG-fake" || res.Content[0].Type != abi.ContentImage {
		t.Fatal(res, err)
	}
	res, err = h.Execute(b, "extract", map[string]any{"url": "https://app.example.org/", "instruction": "debt"})
	if err != nil || res.Structured.(map[string]any)["data"].(map[string]any)["debt"] != 1234.56 {
		t.Fatal(res, err)
	}
}

func TestAgentSites(t *testing.T) {
	h, b, _ := setup(t, nil)
	for _, u := range []string{"https://evil.example/", "https://example.com.evil.example/", "https://app.example.org/redirect"} {
		if _, err := h.Execute(b, "read", map[string]any{"url": u}); err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
			t.Errorf("%s: %v", u, err)
		}
	}
	h.Settings = map[string]any{"cdp_url": "http://127.0.0.1:1"}
	if _, err := h.Execute(b, "read", map[string]any{"url": "https://x.example"}); err == nil || !strings.Contains(err.Error(), "allowed_domains is empty") {
		t.Fatal("empty allowlist must deny:", err)
	}
	h.Settings = map[string]any{"mode": "blocklist", "blocked": []any{"*.evil.example"}, "cdp_url": "http://127.0.0.1:1"}
	if _, err := h.Execute(b, "read", map[string]any{"url": "https://shop.example/"}); err != nil {
		t.Fatal("blocklist allows other sites:", err)
	}
	for _, u := range []string{"https://a.evil.example/", "http://127.0.0.1:8080/"} {
		if _, err := h.Execute(b, "read", map[string]any{"url": u}); err == nil {
			t.Errorf("blocklist allowed %s", u)
		}
	}
}

func exec(b *Browser, action string, args map[string]any, caller *abi.Caller) (abi.ExecResult, error) {
	return b.Execute(context.Background(), nil, abi.ExecRequest{Action: action, Args: args, Caller: caller,
		Settings: map[string]any{"cdp_url": "http://127.0.0.1:1", "allowed_domains": []any{"*.example.com"}}})
}

func TestServiceActions(t *testing.T) {
	_, b, eng := setup(t, nil)
	caller := &abi.Caller{Connector: "tbank", Call: "root1", Params: map[string]any{"domains": []any{"www.tbank-online.com"}}}
	if _, err := exec(b, "observe", map[string]any{}, nil); err == nil || !strings.Contains(err.Error(), "pass session") {
		t.Fatal("agent page action without a session:", err)
	}
	if _, err := exec(b, "observe", map[string]any{"session": "s-1"}, caller); err == nil || !strings.Contains(err.Error(), "session is for agents") {
		t.Fatal("connector with an agent session:", err)
	}
	if _, err := exec(b, "act", map[string]any{"selector": "#sum", "method": "fill", "value": "10"}, caller); err == nil || !strings.Contains(err.Error(), "open first") {
		t.Fatal("act before open:", err)
	}
	if _, err := exec(b, "open", map[string]any{"url": "https://evil.example/"}, caller); err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
		t.Fatal("site outside caller params:", err)
	}
	res, err := exec(b, "open", map[string]any{"url": "https://www.tbank-online.com/"}, caller)
	if err != nil || res.Structured.(map[string]any)["url"] != "https://www.tbank-online.com/" {
		t.Fatal(res, err)
	}
	res, err = exec(b, "act", map[string]any{"selector": "#sum", "method": "fill", "value": "10"}, caller)
	if err != nil || res.Structured.(map[string]any)["text"] != "ok" || eng.keys[len(eng.keys)-1] != "10" {
		t.Fatal(res, err)
	}
	for _, bad := range []map[string]any{{"selector": "#x", "method": "hover"}, {"selector": "#x", "method": "press", "value": "F5"}, {"method": "click"}} {
		if _, err := exec(b, "act", bad, caller); err == nil || !strings.Contains(err.Error(), abi.CodeBadRequest) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if _, err := exec(b, "act", map[string]any{"selector": "#missing", "method": "click", "timeout_ms": 1.0}, caller); err == nil || !strings.Contains(err.Error(), abi.CodeNotFound) {
		t.Fatal("missing element:", err)
	}
	if _, err := exec(b, "act", map[string]any{"selector": "#away", "method": "click"}, caller); err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
		t.Fatal("click led off the site list:", err)
	}
	res, err = exec(b, "observe", map[string]any{}, caller)
	if err == nil {
		// после ухода со списка страница закрыта (about:blank)
		t.Fatal("observe after leaving the list:", res)
	}
	// Страница — одна на корневое действие: другой корень ждёт end_call.
	done := make(chan error, 1)
	go func() {
		_, err := exec(b, "open", map[string]any{"url": "https://www.tbank-online.com/"}, &abi.Caller{Connector: "tbank", Call: "root2", Params: caller.Params})
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("second page while the first is held")
	case <-time.After(100 * time.Millisecond):
	}
	b.EndCall("root1")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b.EndCall("root2")
	if _, err := exec(b, "open", map[string]any{"url": "https://www.tbank-online.com/"}, &abi.Caller{Connector: "x", Call: "r3"}); err == nil || !strings.Contains(err.Error(), "allows no sites") {
		t.Fatal("caller without sites:", err)
	}
}

func TestIdleSessionReleased(t *testing.T) {
	old := SessionIdle
	SessionIdle = 50 * time.Millisecond
	defer func() { SessionIdle = old }()
	_, b, _ := setup(t, nil)
	caller := &abi.Caller{Connector: "tbank", Call: "lost", Params: map[string]any{"domains": []any{"*.example.com"}}}
	if _, err := exec(b, "open", map[string]any{"url": "https://a.example.com/"}, caller); err != nil {
		t.Fatal(err)
	}
	// end_call не пришёл — страница освобождается по простою.
	if _, err := exec(b, "read", map[string]any{"url": "https://a.example.com/"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestManifest(t *testing.T) {
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		t.Fatal(err)
	}
	if m.Runtime != manifest.RuntimeNative || m.Contract != 1 || len(m.Platforms) < 4 {
		t.Fatal(m.Runtime, m.Contract, m.Platforms)
	}
	for _, a := range m.Actions {
		if a.ServiceOnly {
			t.Errorf("%s: page actions are for agents too", a.Name)
		}
	}
	for _, name := range m.Services {
		if _, ok := m.Find(name); !ok {
			t.Errorf("service %s is not an action", name)
		}
	}
	if len(m.Capabilities.Network) != 0 || len(m.Capabilities.Files) != 0 || len(m.Capabilities.Reveal) != 1 {
		t.Fatal("browser declares only reveal of the model key", m.Capabilities)
	}
}

// agentExec — действие агента (без caller) с настройками сайтов агента.
func agentExec(t *testing.T, b *Browser, action string, args map[string]any) (map[string]any, error) {
	t.Helper()
	res, err := exec(b, action, args, nil)
	if err != nil {
		return nil, err
	}
	m, _ := res.Structured.(map[string]any)
	return m, nil
}

func TestAgentSession(t *testing.T) {
	_, b, eng := setup(t, nil)
	if _, err := agentExec(t, b, "open", map[string]any{"url": "https://evil.example/"}); err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) ||
		!strings.Contains(err.Error(), "gate4ai sites allow evil.example") {
		t.Fatal("agent site list, with the owner's command:", err)
	}
	// Неудачный open не оставляет сеанс: страница свободна.
	if _, err := agentExec(t, b, "read", map[string]any{"url": "https://a.example.com/"}); err != nil {
		t.Fatal(err)
	}
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["session"].(string)
	if !strings.HasPrefix(id, "s-") || eng.hooks == 0 {
		t.Fatal("open returns the session and hooks the page's requests:", res, eng.hooks)
	}
	res, err = agentExec(t, b, "observe", map[string]any{"session": id, "elements": true})
	if err != nil || res["text"] != "Задолженность 1 234,56 ₽" {
		t.Fatal(res, err)
	}
	if els := res["elements"].([]worker.Element); len(els) != 1 || els[0].Selector != "#pay" || els[0].Text != "Оплатить" {
		t.Fatal(res["elements"])
	}
	if res, err = agentExec(t, b, "act", map[string]any{"session": id, "selector": "#pay", "method": "click"}); err != nil || res["url"] != "https://a.example.com/" {
		t.Fatal(res, err)
	}
	if _, err := agentExec(t, b, "act", map[string]any{"session": id, "selector": "#missing", "method": "click", "timeout_ms": 1.0}); err == nil {
		t.Fatal("missing element")
	}
	res, err = agentExec(t, b, "network", map[string]any{"session": id, "url_contains": "/"})
	if err != nil {
		t.Fatal(err)
	}
	reqs := res["requests"].([]worker.Request)
	if h, _ := res["hint"].(string); !strings.Contains(h, "(id)") || !strings.Contains(h, "redact_params") {
		t.Fatalf("identifiers in addresses: hint %q", h)
	}
	if len(reqs) != 3 || reqs[1].Body != `{"access_token":"***","debt":10}` || !strings.Contains(reqs[2].Body, "hidden") || strings.Contains(reqs[2].Body, "t0k") {
		t.Fatalf("tokens and sign-in responses are hidden: %+v", reqs)
	}

	// Страница сеанса занята: чтение и коннектор получают busy с подсказкой, а не ждут таймаута.
	old := AgentWait
	AgentWait = 20 * time.Millisecond
	defer func() { AgentWait = old }()
	if _, err := agentExec(t, b, "read", map[string]any{"url": "https://a.example.com/"}); err == nil || !strings.Contains(err.Error(), "browser.close") {
		t.Fatal("read while a session is open:", err)
	}
	caller := &abi.Caller{Connector: "site", Call: "root1", Params: map[string]any{"domains": []any{"*.example.com"}}}
	if _, err := exec(b, "open", map[string]any{"url": "https://a.example.com/"}, caller); err == nil || !strings.Contains(err.Error(), "browser.close") {
		t.Fatal("connector while a session is open:", err)
	}
	if _, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatal("second session:", err)
	}

	res, err = agentExec(t, b, "close", map[string]any{"session": id})
	if err != nil {
		t.Fatal(err)
	}
	steps := res["steps"].([]map[string]any)
	if len(steps) != 5 || steps[0]["action"] != "open" || steps[3]["error"] == nil || steps[1]["result"] == nil {
		t.Fatalf("steps: %+v", steps)
	}
	if _, ok := steps[0]["args"].(map[string]any)["session"]; ok {
		t.Fatal("steps are recorded without session (connectors call without it)")
	}
	// Шаги — фикстура для hosttest.Replay: та же форма после JSON.
	raw, _ := json.Marshal(res)
	var fx hosttest.Fixture
	if err := json.Unmarshal(raw, &fx); err != nil || len(fx.Steps) != 5 || fx.Steps[3].Error.Code != abi.CodeNotFound {
		t.Fatalf("fixture: %+v %v", fx, err)
	}
	if _, err := agentExec(t, b, "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), "closed or unknown") {
		t.Fatal("closed session:", err)
	}
	if _, err := exec(b, "open", map[string]any{"url": "https://a.example.com/"}, caller); err != nil {
		t.Fatal("the page is free after close:", err)
	}
	b.EndCall("root1")
}

func TestAgentSessionIdle(t *testing.T) {
	old := AgentIdle
	AgentIdle = 50 * time.Millisecond
	defer func() { AgentIdle = old }()
	_, b, _ := setup(t, nil)
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := agentExec(t, b, "read", map[string]any{"url": "https://a.example.com/"}); err != nil {
		t.Fatal("idle session is closed:", err)
	}
	if _, err := agentExec(t, b, "observe", map[string]any{"session": res["session"]}); err == nil {
		t.Fatal("idle session is gone")
	}
}

func TestAgentCloseAllAndCleanSteps(t *testing.T) {
	_, b, _ := setup(t, nil)
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/?code=one-time&x=1"})
	if err != nil {
		t.Fatal(err)
	}
	if res["url"] != "https://a.example.com/?code=***&x=1" {
		t.Fatal("one-time codes in the address are hidden:", res["url"])
	}
	id := res["session"].(string)
	// CLI передаёт значения по умолчанию — в шагах их нет.
	if _, err := agentExec(t, b, "act", map[string]any{"session": id, "selector": "#pay", "method": "click", "text": "", "timeout_ms": 0.0, "value": ""}); err != nil {
		t.Fatal(err)
	}
	// id потерян: закрыть все сеансы.
	res, err = agentExec(t, b, "close", map[string]any{"all": true})
	if err != nil || res["closed"] != 1 {
		t.Fatal(res, err)
	}
	if _, err := agentExec(t, b, "read", map[string]any{"url": "https://a.example.com/"}); err != nil {
		t.Fatal("the page is free:", err)
	}
	as := &agentSession{}
	as.record(abi.ExecRequest{Action: "act", Args: map[string]any{"session": id, "selector": "#pay", "method": "click", "text": "", "timeout_ms": 0.0, "value": ""}}, abi.ExecResult{}, nil)
	if args := as.steps[0]["args"].(map[string]any); len(args) != 2 {
		t.Fatal("steps without empty values:", args)
	}
}

// agentAs — действие агента agent (ExecRequest.Agent ставит хост).
func agentAs(b *Browser, agent, action string, args map[string]any) (map[string]any, error) {
	res, err := b.Execute(context.Background(), nil, abi.ExecRequest{Action: action, Args: args, Agent: agent,
		Settings: map[string]any{"cdp_url": "http://127.0.0.1:1", "allowed_domains": []any{"*.example.com"}}})
	m, _ := res.Structured.(map[string]any)
	return m, err
}

// Сеанс привязан к агенту: другой агент его не видит и close --all его не закрывает.
func TestAgentSessionsPerAgent(t *testing.T) {
	_, b, _ := setup(t, nil)
	res, err := agentAs(b, "alice", "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	for _, a := range []string{"observe", "act", "close"} {
		args := map[string]any{"session": id}
		if a == "act" {
			args["selector"], args["method"] = "#pay", "click"
		}
		if _, err := agentAs(b, "mallory", a, args); err == nil || !strings.Contains(err.Error(), "closed or unknown") {
			t.Fatalf("%s on another agent's session: %v", a, err)
		}
	}
	if res, err := agentAs(b, "mallory", "close", map[string]any{"all": true}); err != nil || res["closed"] != 0 {
		t.Fatal("close --all of another agent:", res, err)
	}
	if _, err := agentAs(b, "alice", "observe", map[string]any{"session": id}); err != nil {
		t.Fatal("the owner's session survives:", err)
	}
	if res, err := agentAs(b, "alice", "close", map[string]any{"all": true}); err != nil || res["closed"] != 1 {
		t.Fatal(res, err)
	}
}

// close --all отменяет идущее действие сеанса и освобождает страницу только
// после его завершения: следующий сеанс не получает её посреди действия.
func TestCloseAllWaitsForAction(t *testing.T) {
	_, b, _ := setup(t, nil)
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	done := make(chan error, 1)
	go func() {
		// элемента нет: act ждёт его до timeout_ms
		_, err := agentExec(t, b, "act", map[string]any{"session": id, "selector": "#missing", "method": "click", "timeout_ms": 30000.0})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	if res, err := agentExec(t, b, "close", map[string]any{"all": true}); err != nil || res["closed"] != 1 {
		t.Fatal(res, err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), abi.CodeCancelled) {
			t.Fatal("the running action is cancelled:", err)
		}
	default:
		t.Fatal("close --all released the page while an action was running")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("close --all waited for the action's timeout")
	}
	if _, err := agentExec(t, b, "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), "closed or unknown") {
		t.Fatal(err)
	}
}

func TestMain(m *testing.M) {
	worker.SettleTimeout = 200 * time.Millisecond // страницы вне списка в тестах не возвращаются
	os.Exit(m.Run())
}
