// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/connectors/browser/worker"
)

// fakeEngine — страница без Chrome: переходы, текст, поиск элемента для act.
type fakeEngine struct {
	mu      sync.Mutex // url меняет и «человек» (handoff) из другой горутины
	url     string
	keys    []string
	hooks   int
	json    string   // верхний документ — JSON с этим телом
	fetches []string // запросы со страницы: метод и адрес
	reply   string   // тело ответа запроса со страницы ("" — короткий JSON)
	jsMax   float64  // предел тела, с которым запрос ушёл на страницу
	tabs    int      // открытые временные вкладки (NewTab)
	closed  int      // закрытые вкладки (Close)
	parent  *fakeEngine
}

// NewTab — временная вкладка: своя страница (адрес), тот же тип документа.
func (f *fakeEngine) NewTab(context.Context, bool) (worker.Engine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tabs++
	return &fakeEngine{url: "about:blank", json: f.json, parent: f}, nil
}

func (f *fakeEngine) Navigate(_ context.Context, u string) error {
	if strings.HasSuffix(u, "/redirect") {
		u = "https://evil.example/"
	}
	f.mu.Lock()
	f.url = u
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) URL(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.url, nil
}
func (f *fakeEngine) Title(context.Context) (string, error) {
	return "Личный кабинет", nil
}
func (f *fakeEngine) Text(context.Context) (string, error) {
	return "Задолженность 1 234,56 ₽", nil
}
func (f *fakeEngine) Screenshot(context.Context, *worker.Clip, bool) ([]byte, error) {
	return []byte("\x89PNG-fake"), nil
}
func (f *fakeEngine) Extract(context.Context, string) (any, error) {
	return map[string]any{"debt": 1234.56}, nil
}
func (f *fakeEngine) Eval(_ context.Context, expr string, out any) error {
	switch {
	case strings.Contains(expr, "document.contentType"): // тип документа
		if f.json != "" {
			b, _ := json.Marshal(map[string]any{"type": "application/json", "html": false, "body": f.json})
			return json.Unmarshal(b, out)
		}
		return json.Unmarshal([]byte(`{"type":"text/html","html":true}`), out)
	case strings.Contains(expr, "credentials: 'include'"): // запрос со страницы
		var args []any
		json.Unmarshal([]byte(expr[strings.LastIndex(expr, "(...")+4:len(expr)-1]), &args)
		f.fetches = append(f.fetches, args[1].(string)+" "+args[0].(string))
		f.jsMax, _ = args[4].(float64)
		if f.reply != "" { // как fetchJS: тело до max+1 символов и его длина
			body := f.reply
			if len(body) > int(f.jsMax)+1 {
				body = body[:int(f.jsMax)+1]
			}
			b, _ := json.Marshal(map[string]any{"status": 200, "url": args[0], "headers": map[string]string{"content-type": "application/json"},
				"size": len(f.reply), "chars": len(f.reply), "body": body})
			return json.Unmarshal(b, out)
		}
		return json.Unmarshal([]byte(`{"status":200,"url":"`+args[0].(string)+`","headers":{"content-type":"application/json","set-cookie":"x","x-total":"1"},"size":30,"body":"{\"uid\":\"a1\",\"token\":\"t0k\"}"}`), out)
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
	if strings.HasPrefix(expr, "((secret, max) =>") { // changesJS: что изменилось после type / press
		return json.Unmarshal([]byte(`{"added":[{"tag":"span","text":"a"}],"closed":[{"tag":"ul","role":"listbox"}]}`), out)
	}
	if strings.Contains(expr, "data-g4-cover") { // coverJS: закрасить поля пароля
		return json.Unmarshal([]byte("1"), out)
	}
	if strings.HasPrefix(expr, "((sel, full) =>") { // shotJS
		return json.Unmarshal([]byte(`{"box":{"X":0,"Y":0,"W":800,"H":600},"matched":1}`), out)
	}
	if strings.Contains(expr, "#missing") {
		return json.Unmarshal([]byte("null"), out)
	}
	if strings.Contains(expr, "#bounce") { // SPA ждёт данные, а сайт отправил на вход
		f.Navigate(context.Background(), "https://login.example.com/sign-in")
		return json.Unmarshal([]byte("null"), out)
	}
	if strings.Contains(expr, "#away") {
		f.Navigate(context.Background(), "https://evil.example/")
	}
	return json.Unmarshal([]byte(`{"text":"ok"}`), out)
}
func (f *fakeEngine) InsertText(_ context.Context, t string) error {
	f.keys = append(f.keys, t)
	return nil
}
func (f *fakeEngine) Key(_ context.Context, k string) error { f.keys = append(f.keys, k); return nil }
func (f *fakeEngine) Type(_ context.Context, t string, _ time.Duration) error {
	f.keys = append(f.keys, "type:"+t)
	return nil
}
func (f *fakeEngine) Close() error {
	if p := f.parent; p != nil {
		p.mu.Lock()
		p.closed++
		p.mu.Unlock()
	}
	return nil
}

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
	// Страница — одна на корневое действие; другой корень — в своей вкладке.
	if _, err := exec(b, "open", map[string]any{"url": "https://www.tbank-online.com/"}, &abi.Caller{Connector: "tbank", Call: "root2", Params: caller.Params}); err != nil || eng.tabs != 1 {
		t.Fatal("a second root call gets a tab of its own:", eng.tabs, err)
	}
	b.EndCall("root2")
	// Браузер полон (MaxSessions): другой корень ждёт end_call.
	oldMax := worker.MaxSessions
	worker.MaxSessions = 1
	defer func() { worker.MaxSessions = oldMax }()
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
	// Неудачный open не оставляет сеанс.
	if len(b.agents) != 0 {
		t.Fatal("the failed open left a session")
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

	// Страница коннектора — своя вкладка: страница сеанса не меняется.
	caller := &abi.Caller{Connector: "site", Call: "root1", Params: map[string]any{"domains": []any{"*.example.com"}}}
	if _, err := exec(b, "open", map[string]any{"url": "https://b.example.com/x"}, caller); err != nil || eng.tabs != 1 {
		t.Fatal("connector while a session is open:", eng.tabs, err)
	}
	if res, err := agentExec(t, b, "observe", map[string]any{"session": id}); err != nil || res["url"] != "https://a.example.com/" {
		t.Fatal("the session's page:", res, err)
	}
	b.EndCall("root1")
	// Браузер полон: коннектор и второй сеанс получают busy — чей сеанс, с
	// какого времени, когда закроется — а не ждут таймаута (read — во
	// временной вкладке, см. TestReadDuringSession).
	old, oldMax := AgentWait, worker.MaxSessions
	AgentWait, worker.MaxSessions = 20*time.Millisecond, 1
	defer func() { AgentWait, worker.MaxSessions = old, oldMax }()
	if _, err := exec(b, "open", map[string]any{"url": "https://a.example.com/"}, caller); err == nil || !strings.Contains(err.Error(), "browser.close") {
		t.Fatal("connector while the browser is full:", err)
	}
	if _, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"}); err == nil || !strings.Contains(err.Error(), "busy") ||
		!strings.Contains(err.Error(), "a session of yours (") || !strings.Contains(err.Error(), "closes at") ||
		!strings.Contains(err.Error(), "--wait_for_browser 10m") || !strings.Contains(err.Error(), "without session") {
		t.Fatal("second session, with whose session it is and the alternatives:", err)
	}

	// close: по умолчанию коротко; --steps — шаги без ответов; save_steps —
	// полная запись в файл каталога данных (фикстура hosttest.Replay).
	t.Setenv("GATE4AI_HOME", t.TempDir())
	res, err = agentExec(t, b, "close", map[string]any{"session": id, "steps": true, "save_steps": true})
	if err != nil {
		t.Fatal(err)
	}
	brief := res["step_list"].([]map[string]any)
	if len(brief) != 6 || brief[0]["action"] != "open" || brief[3]["status"] != abi.CodeNotFound || brief[1]["status"] != "ok" || brief[1]["result"] != nil {
		t.Fatalf("brief steps: %+v", brief)
	}
	if _, ok := brief[0]["args"].(map[string]any)["session"]; ok {
		t.Fatal("steps are recorded without session (connectors call without it)")
	}
	path, _ := res["steps_file"].(string)
	if !strings.HasPrefix(path, os.Getenv("GATE4AI_HOME")) {
		t.Fatal("steps file in the data dir:", res)
	}
	fx, err := hosttest.LoadFixture(path)
	if err != nil || len(fx.Steps) != 6 || fx.Steps[3].Error.Code != abi.CodeNotFound || fx.Steps[1].Result["text"] == nil {
		t.Fatalf("fixture: %+v %v", fx, err)
	}
	if _, err := agentExec(t, b, "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), "was closed at") || !strings.Contains(err.Error(), "closed by browser.close") {
		t.Fatal("closed session, who closed it:", err)
	}
	if _, err := exec(b, "open", map[string]any{"url": "https://a.example.com/"}, caller); err != nil {
		t.Fatal("the page is free after close:", err)
	}
	b.EndCall("root1")
}

// TestReadDuringSession — read / screenshot / extract без session при
// открытом сеансе агента идут во временной вкладке: адрес страницы сеанса не
// меняется, вкладка закрывается, список сайтов действует как без сеанса;
// лимит вкладок — ошибка с рабочей альтернативой.
func TestReadDuringSession(t *testing.T) {
	_, b, eng := setup(t, nil)
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/cabinet"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	before, err := agentExec(t, b, "observe", map[string]any{"session": id})
	if err != nil {
		t.Fatal(err)
	}
	r, err := exec(b, "read", map[string]any{"url": "https://b.example.com/status"}, nil)
	if err != nil || r.Structured.(map[string]any)["url"] != "https://b.example.com/status" || !strings.Contains(r.Content[0].Text, "1 234,56") {
		t.Fatal("read while a session is open:", r, err)
	}
	for _, a := range []map[string]any{{"url": "https://b.example.com/", "selector": "#debt"}, {"url": "https://b.example.com/", "instruction": "debt"}} {
		action := "screenshot"
		if a["instruction"] != nil {
			action = "extract"
		}
		if _, err := exec(b, action, a, nil); err != nil {
			t.Fatal(action, "while a session is open:", err)
		}
	}
	// Список сайтов — как без сеанса.
	if _, err := exec(b, "read", map[string]any{"url": "https://evil.example/"}, nil); err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
		t.Fatal("site list in a temporary tab:", err)
	}
	after, err := agentExec(t, b, "observe", map[string]any{"session": id})
	if err != nil || after["url"] != before["url"] || after["url"] != "https://a.example.com/cabinet" || after["title"] != before["title"] {
		t.Fatal("the session's page did not change:", before, after, err)
	}
	if eng.tabs != 4 || eng.closed != 4 {
		t.Fatalf("each call opens and closes its tab: opened %d, closed %d", eng.tabs, eng.closed)
	}

	// Все вкладки заняты: ошибка предлагает сеанс, а не только ждать.
	old, oldMax := AgentWait, worker.MaxTabs
	AgentWait, worker.MaxTabs = 20*time.Millisecond, 1
	defer func() { AgentWait, worker.MaxTabs = old, oldMax }()
	_, b2, _ := setup(t, nil)
	ss, err := b2.W.Tab(context.Background(), worker.Settings{CDPURL: "http://127.0.0.1:1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec(b2, "read", map[string]any{"url": "https://b.example.com/"}, nil); err == nil || !strings.Contains(err.Error(), abi.CodeLimit) ||
		!strings.Contains(err.Error(), "browser.get --session <id> --url") {
		t.Fatal("tab limit, with the alternatives:", err)
	}
	ss.Release()
	if _, err := exec(b2, "read", map[string]any{"url": "https://b.example.com/"}, nil); err != nil {
		t.Fatal("a tab is free again:", err)
	}
}

// TestAgentEditReloadHandoff — act fill возвращает итоговое значение, reload
// и handoff работают в сеансе; handoff проверяется до уведомления человека.
func TestAgentEditReloadHandoff(t *testing.T) {
	_, b, eng := setup(t, nil)
	if _, err := agentExec(t, b, "open", map[string]any{"url": "https://evil.example/"}); err == nil ||
		!strings.Contains(err.Error(), `"op":"add_setting_items","connector":"browser","key":"allowed_domains","items":["evil.example"]`) {
		t.Fatal("the agent learns how to ask for a site:", err)
	}
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	if _, err := agentExec(t, b, "act", map[string]any{"session": id, "selector": "textarea", "method": "fill", "value": strings.Repeat("x", worker.MaxActValue+1)}); err == nil ||
		!strings.Contains(err.Error(), `argument "value" is too long`) {
		t.Fatal(err)
	}
	if _, err := agentExec(t, b, "act", map[string]any{"session": id, "selector": "textarea", "method": "press", "value": "Ctrl+V"}); err == nil ||
		!strings.Contains(err.Error(), "method paste") {
		t.Fatal(err)
	}
	if res, err = agentExec(t, b, "reload", map[string]any{"session": id}); err != nil || res["url"] != "https://a.example.com/" {
		t.Fatal(res, err)
	}
	if res, err = agentExec(t, b, "handoff", map[string]any{"session": id, "message": "Войдите", "precheck": true}); err != nil {
		t.Fatal(res, err)
	}
	if _, err = agentExec(t, b, "handoff", map[string]any{"session": id, "message": "Войдите", "timeout_ms": 3600000.0}); err == nil {
		t.Fatal("handoff over the limit")
	}
	old := worker.HandoffPoll
	worker.HandoffPoll = 10 * time.Millisecond
	defer func() { worker.HandoffPoll = old }()
	go func() {
		time.Sleep(50 * time.Millisecond)
		eng.Navigate(context.Background(), "https://a.example.com/home")
	}()
	if res, err = agentExec(t, b, "handoff", map[string]any{"session": id, "message": "Войдите", "timeout_ms": 5000.0}); err != nil ||
		res["status"] != "done" || res["url"] != "https://a.example.com/home" {
		t.Fatal(res, err)
	}
	res, err = agentExec(t, b, "close", map[string]any{"session": id, "steps": true})
	if err != nil || res["steps"] != 3 {
		t.Fatal(res, err)
	}
	for _, st := range res["step_list"].([]map[string]any) {
		if st["action"] == "handoff" && st["args"].(map[string]any)["precheck"] != nil {
			t.Fatal("the host's precheck is not a step:", st)
		}
	}
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
	if _, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"}); err != nil {
		t.Fatal("idle session is closed, the page is free:", err)
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
	if err != nil || res["sessions"] != 1 {
		t.Fatal(res, err)
	}
	if _, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"}); err != nil {
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
	if res, err := agentAs(b, "mallory", "close", map[string]any{"all": true}); err != nil || res["sessions"] != 0 {
		t.Fatal("close --all of another agent:", res, err)
	}
	if _, err := agentAs(b, "alice", "observe", map[string]any{"session": id}); err != nil {
		t.Fatal("the owner's session survives:", err)
	}
	if res, err := agentAs(b, "alice", "close", map[string]any{"all": true}); err != nil || res["sessions"] != 1 {
		t.Fatal(res, err)
	}
}

func agentIn(b *Browser, agent, instance, action string, args map[string]any) (map[string]any, error) {
	res, err := b.Execute(context.Background(), nil, abi.ExecRequest{Action: action, Args: args, Agent: agent, AgentInstance: instance,
		Settings: map[string]any{"cdp_url": "http://127.0.0.1:1", "allowed_domains": []any{"*.example.com"}}})
	m, _ := res.Structured.(map[string]any)
	return m, err
}

// Две сессии Claude Code — один агент claude-code: сеанс у своей сессии (своя
// вкладка), close --all второй не закрывает сеанс первой; браузер полон —
// busy называет чужой сеанс по агенту и сессии, а свой — «yours» с id.
// Экземпляр неизвестен — сеансы всех экземпляров агента.
func TestAgentSessionsPerInstance(t *testing.T) {
	old := AgentWait
	AgentWait = 50 * time.Millisecond
	t.Cleanup(func() { AgentWait = old })
	_, b, _ := setup(t, nil)
	res, err := agentIn(b, "claude-code", "session:one", "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	oldMax := worker.MaxSessions
	worker.MaxSessions = 1
	t.Cleanup(func() { worker.MaxSessions = oldMax })
	_, err = agentIn(b, "claude-code", "session:two", "open", map[string]any{"url": "https://b.example.com/"})
	if err == nil || !strings.Contains(err.Error(), "a session of claude-code (session one) open since") || strings.Contains(err.Error(), id) {
		t.Fatal("the second session's open:", err)
	}
	if _, err := agentIn(b, "claude-code", "session:two", "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), "closed or unknown") {
		t.Fatal("another session of the agent uses the id:", err)
	}
	if res, err := agentIn(b, "claude-code", "session:two", "close", map[string]any{"all": true}); err != nil || res["sessions"] != 0 {
		t.Fatal("close --all of the second session:", res, err)
	}
	if _, err := agentIn(b, "claude-code", "session:one", "observe", map[string]any{"session": id}); err != nil {
		t.Fatal("the first session's browser session survives:", err)
	}
	_, err = agentIn(b, "claude-code", "session:one", "open", map[string]any{"url": "https://a.example.com/"})
	if err == nil || !strings.Contains(err.Error(), "a session of yours ("+id+")") {
		t.Fatal("the holder's second open names its own session:", err)
	}
	if _, err := agentIn(b, "claude-code", "", "observe", map[string]any{"session": id}); err != nil {
		t.Fatal("an unknown instance of the agent:", err)
	}
	if res, err := agentIn(b, "claude-code", "", "close", map[string]any{"all": true}); err != nil || res["sessions"] != 1 {
		t.Fatal("close --all with an unknown instance:", res, err)
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
	if res, err := agentExec(t, b, "close", map[string]any{"all": true}); err != nil || res["sessions"] != 1 {
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
	if _, err := agentExec(t, b, "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), "closed by browser.close --all") {
		t.Fatal(err)
	}
}

// Запросы со страницы сеанса: метод — имя действия (get, head — чтение,
// post, put, patch, delete — изменения), адрес — по списку сайтов, ответ —
// разобранный JSON без токенов и cookies, квитанция — метод, адрес, статус, размер.
func TestAgentFetch(t *testing.T) {
	h, b, eng := setup(t, nil)
	for name, class := range map[string]string{"get": "read", "head": "read", "post": "write", "put": "write", "patch": "write", "delete": "write"} {
		a, ok := h.Manifest.Find(name)
		if !ok || a.Class != class || !a.Untrusted {
			t.Errorf("%s: %+v", name, a)
		}
		if _, body := a.InputSchema.Properties.Get("body"); body != (class == "write" && name != "delete") {
			t.Errorf("%s: body argument %v", name, body)
		}
	}
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	r, err := exec(b, "get", map[string]any{"session": id, "url": "https://a.example.com/api/v1/provisioning/alert-rules"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := r.Structured.(map[string]any)
	if out["status"] != 200 || out["json"].(map[string]any)["token"] != "***" || out["json"].(map[string]any)["uid"] != "a1" ||
		out["headers"].(map[string]string)["set-cookie"] != "" || out["headers"].(map[string]string)["x-total"] != "1" {
		t.Fatalf("%+v", out)
	}
	r, err = exec(b, "delete", map[string]any{"session": id, "url": "https://a.example.com/api/v1/provisioning/alert-rules/a1"}, nil)
	if err != nil || r.ResultRef != "DELETE https://a.example.com/api/v1/provisioning/alert-rules/a1 → 200, 30 bytes" {
		t.Fatal(r.ResultRef, err)
	}
	if len(eng.fetches) != 2 || eng.fetches[1] != "DELETE https://a.example.com/api/v1/provisioning/alert-rules/a1" {
		t.Fatal(eng.fetches)
	}
	if _, err := exec(b, "delete", map[string]any{"session": id, "url": "https://evil.example/api"}, nil); err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) || len(eng.fetches) != 2 {
		t.Fatal("site list:", err)
	}
	for _, bad := range []map[string]any{
		{"session": id, "url": "https://a.example.com/", "headers": "Accept: x"},
		{"session": id, "url": "https://a.example.com/", "body": strings.Repeat("x", worker.MaxFetchBody+1)},
	} {
		if _, err := exec(b, "post", bad, nil); err == nil || !strings.Contains(err.Error(), abi.CodeBadRequest) {
			t.Fatal(bad["headers"], err)
		}
	}
	if _, err := exec(b, "get", map[string]any{"url": "https://a.example.com/"}, &abi.Caller{Connector: "site", Call: "c1"}); err == nil {
		t.Fatal("for agents only")
	}
	if _, err := exec(b, "get", map[string]any{"url": "https://a.example.com/"}, nil); err == nil || !strings.Contains(err.Error(), "browser.open") {
		t.Fatal("needs a session:", err)
	}
	fr, _ := fetchRequest(abi.ExecRequest{Action: "post", Args: map[string]any{"url": "https://a.example.com/", "body": `{"a":1}`}})
	if fr.Headers["Content-Type"] != "application/json" {
		t.Fatal("a JSON body gets its type:", fr.Headers)
	}
	// Шаги close: кратко, без ответов.
	res, err = agentExec(t, b, "close", map[string]any{"session": id, "steps": true})
	if err != nil {
		t.Fatal(err)
	}
	if st := res["step_list"].([]map[string]any); len(st) != 4 || st[2]["action"] != "delete" || st[2]["status"] != "ok" || st[3]["status"] != abi.CodeCapabilityDenied {
		t.Fatalf("%+v", st)
	}
}

// num принимает любые числовые типы аргументов: float64 из JSON, int и int64
// из Go, json.Number от декодера с UseNumber.
func TestNum(t *testing.T) {
	for _, v := range []any{200000.0, float32(200000), 200000, int64(200000), int32(200000), json.Number("200000"), json.Number("200000.0")} {
		if n := num(map[string]any{"max_chars": v}, "max_chars"); n != 200000 {
			t.Errorf("%T %v: %d", v, v, n)
		}
	}
	for _, v := range []any{nil, "200000", true, json.Number("x")} {
		if n := num(map[string]any{"max_chars": v}, "max_chars"); n != 0 {
			t.Errorf("%T %v: %d", v, v, n)
		}
	}
	for _, v := range []any{200000.0, 200000, int64(200000), json.Number("200000")} {
		fr, err := fetchRequest(abi.ExecRequest{Action: "get", Args: map[string]any{"url": "https://a.example.com/", "max_chars": v}})
		if err != nil || fr.Max != 200000 {
			t.Errorf("%T: FetchRequest.Max %d, %v", v, fr.Max, err)
		}
	}
}

// JSON-ответ больше max_text_chars (35 КБ при 30000): по умолчанию и с
// max_chars 200000 — разобранный json без truncated; явный max_chars меньше
// ответа — тело с подсказкой, какой max_chars вернёт его целиком.
func TestFetchLargeJSON(t *testing.T) {
	_, b, eng := setup(t, nil)
	items := make([]string, 0, 1000)
	for i := range 1000 {
		items = append(items, `{"id":`+strconv.Itoa(i)+`,"name":"rule number `+strconv.Itoa(i)+`"}`)
	}
	eng.reply = `{"rules":[` + strings.Join(items, ",") + `]}`
	if len(eng.reply) < 35000 {
		t.Fatal(len(eng.reply))
	}
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	for _, max := range []any{nil, 200000.0, json.Number("200000"), 200000} {
		args := map[string]any{"session": id, "url": "https://a.example.com/api/rules"}
		want := float64(worker.DefaultJSONChars)
		if max != nil {
			args["max_chars"] = max
			want = 200000
		}
		out, err := agentExec(t, b, "get", args)
		if err != nil {
			t.Fatal(err)
		}
		if out["truncated"] != nil || out["body"] != nil || out["hint"] != nil || len(out["json"].(map[string]any)["rules"].([]any)) != 1000 || eng.jsMax != want {
			t.Fatalf("max_chars %v: page max %v, %v", max, eng.jsMax, out["truncated"])
		}
	}
	out, err := agentExec(t, b, "get", map[string]any{"session": id, "url": "https://a.example.com/api/rules", "max_chars": 1000.0})
	if err != nil {
		t.Fatal(err)
	}
	full := strconv.Itoa(len(eng.reply))
	if out["truncated"] != true || out["json"] != nil || len(out["body"].(string)) != 1000 ||
		out["hint"] != "json not parsed: body truncated at 1000 of "+full+" chars, pass max_chars ≥ "+full {
		t.Fatalf("%v %v", out["truncated"], out["hint"])
	}
	// Больше верхней границы — граница.
	if _, err := agentExec(t, b, "get", map[string]any{"session": id, "url": "https://a.example.com/api/rules", "max_chars": 5e6}); err != nil || eng.jsMax != worker.MaxResponseChars {
		t.Fatal(eng.jsMax, err)
	}
	// observe JSON-документа: max_chars больше max_text_chars не урезается до него.
	eng.json = eng.reply
	if out, err = agentExec(t, b, "observe", map[string]any{"session": id, "max_chars": 200000.0}); err != nil || out["truncated"] != nil || out["body"] != eng.reply {
		t.Fatal(out["truncated"], err)
	}
	if out, err = agentExec(t, b, "observe", map[string]any{"session": id, "max_chars": 100.0}); err != nil || out["truncated"] != true ||
		out["hint"] != "body truncated at 100 of "+full+" chars, pass max_chars ≥ "+full {
		t.Fatal(out["hint"], err)
	}
	agentExec(t, b, "close", map[string]any{"session": id})
}

// JSON-страница: observe и read отдают тело как прислал сервер, с типом;
// результат — по схеме манифеста.
func TestObserveJSONPage(t *testing.T) {
	h, b, eng := setup(t, nil)
	eng.json = `{"rules":[{"uid":"a1"}],"access_token":"t0k"}`
	res, err := h.Execute(b, "read", map[string]any{"url": "https://a.example.com/api/rules"})
	if err != nil || res.Structured.(map[string]any)["body"] != `{"access_token":"***","rules":[{"uid":"a1"}]}` || res.Structured.(map[string]any)["content_type"] != "application/json" {
		t.Fatal(res, err)
	}
	if err := abi.CheckResult(res, mustFind(t, h, "read").OutputSchema); err != nil {
		t.Fatal(err)
	}
	r, err := exec(b, "open", map[string]any{"url": "https://a.example.com/api/rules"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := r.Structured.(map[string]any)["session"].(string)
	r, err = exec(b, "observe", map[string]any{"session": id, "max_chars": 10.0}, nil)
	if err != nil || r.Structured.(map[string]any)["body"] != `{"access_t` || r.Structured.(map[string]any)["truncated"] != true || r.Structured.(map[string]any)["text"] != nil {
		t.Fatal(r, err)
	}
	if err := abi.CheckResult(r, mustFind(t, h, "observe").OutputSchema); err != nil {
		t.Fatal(err)
	}
	agentExec(t, b, "close", map[string]any{"session": id})
}

// Уход на сайт входа во время ожидания — код redirected с подсказкой учётки.
func TestOpenRedirected(t *testing.T) {
	_, b, _ := setup(t, nil)
	_, err := b.Execute(context.Background(), nil, abi.ExecRequest{Action: "open", Agent: "a",
		Args:     map[string]any{"url": "https://a.example.com/alerting", "wait_for_selector": "#bounce", "timeout_ms": 30000.0},
		Accounts: []abi.Account{{Alias: "@site", Bind: []string{"https://login.example.com"}}},
		Settings: map[string]any{"cdp_url": "http://127.0.0.1:1", "allowed_domains": []any{"a.example.com"}}})
	var ae *abi.Error
	if !errors.As(err, &ae) || ae.Code != CodeRedirected || !strings.Contains(ae.Message, "https://login.example.com/sign-in") ||
		!strings.Contains(ae.Message, "browser.login --account @site") {
		t.Fatal(err)
	}
	if _, err := agentExec(t, b, "read", map[string]any{"url": "https://a.example.com/"}); err != nil {
		t.Fatal("the failed open left no session:", err)
	}
}

func mustFind(t *testing.T, h *hosttest.Host, name string) manifest.Action {
	t.Helper()
	a, ok := h.Manifest.Find(name)
	if !ok {
		t.Fatal(name)
	}
	return a
}

func TestMain(m *testing.M) {
	worker.SettleTimeout = 200 * time.Millisecond // страницы вне списка в тестах не возвращаются
	os.Exit(m.Run())
}
