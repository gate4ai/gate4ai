// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/connectors/browser/worker"
)

// chromeBrowser — коннектор на настоящем Chrome без окна (headless) с
// движком engine; call — вызов от имени агента agent в клиенте client.
func chromeBrowser(t *testing.T, engine string, srvURL string) (*Browser, func(agent, client, action string, args map[string]any) (map[string]any, error)) {
	t.Helper()
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := worker.FindChrome()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	b := New()
	t.Cleanup(func() { b.W.Close() })
	settings := map[string]any{"engine": engine, "chrome_path": wrapper, "profile_dir": filepath.Join(dir, "profile"), "allowed_domains": []any{"127.0.0.1"}}
	return b, func(agent, client, action string, args map[string]any) (map[string]any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		res, err := b.Execute(ctx, nil, abi.ExecRequest{Action: action, Args: args, Settings: settings, Agent: agent, Client: client})
		m, _ := res.Structured.(map[string]any)
		return m, err
	}
}

// formPage — форма с несохранёнными изменениями (beforeunload), alert и
// медленным действием; /other — другая страница.
func formPage() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/other" {
			w.Write([]byte("<!doctype html><title>Other</title><p>Other page"))
			return
		}
		w.Write([]byte(`<!doctype html><title>Form</title><textarea id=q></textarea><button id=hi onclick="alert('Saved?')">Hi</button>
<script>addEventListener('beforeunload', e => { if (document.getElementById('q').value) { e.preventDefault(); e.returnValue = 'unsaved'; } })</script>`))
	}))
}

// Критерий задачи: страница с beforeunload — reload возвращается за
// timeout_ms с понятным статусом, сессия остаётся рабочей; alert — ответ
// dialog_open и действие dialog; занятый сеанс — session_blocked, а не зависание.
func TestReloadBeforeUnloadRealChrome(t *testing.T) {
	srv := formPage()
	defer srv.Close()
	for _, engine := range []string{"cdp", "stagehand"} {
		t.Run(engine, func(t *testing.T) {
			_, call := chromeBrowser(t, engine, srv.URL)
			res, err := call("dev", "c1", "open", map[string]any{"url": srv.URL + "/form"})
			if err != nil {
				t.Skip("chrome did not start:", err)
			}
			id := res["session"].(string)
			if _, err := call("dev", "c1", "act", map[string]any{"session": id, "selector": "#q", "method": "press", "value": "a"}); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			res, err = call("dev", "c1", "reload", map[string]any{"session": id, "wait_for_selector": "textarea", "timeout_ms": 20000.0})
			if err != nil || res["title"] != "Form" || time.Since(start) > 20*time.Second {
				t.Fatalf("reload with beforeunload: %v %v in %s", res, err, time.Since(start))
			}
			// Срок на действие целиком: элемента нет — ответ timeout за timeout_ms с состоянием страницы.
			start = time.Now()
			_, err = call("dev", "c1", "reload", map[string]any{"session": id, "wait_for_selector": "#never", "timeout_ms": 1500.0})
			if err == nil || !strings.Contains(err.Error(), `"title":"Form"`) || time.Since(start) > 1500*time.Millisecond+ActionGrace+2*time.Second {
				t.Fatalf("reload past timeout_ms: %v in %s", err, time.Since(start))
			}
			if res, err := call("dev", "c1", "observe", map[string]any{"session": id}); err != nil || res["title"] != "Form" {
				t.Fatal("the session works:", res, err)
			}
			// alert: dialog_open {type, message}; пока он открыт — сразу тот же ответ.
			if _, err := call("dev", "c1", "act", map[string]any{"session": id, "selector": "#hi", "method": "click"}); err == nil ||
				!strings.Contains(err.Error(), CodeDialogOpen+`: {"type":"alert","message":"Saved?"`) {
				t.Fatal("alert:", err)
			}
			start = time.Now()
			if _, err := call("dev", "c1", "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), CodeDialogOpen) || time.Since(start) > 10*time.Second {
				t.Fatal("observe with a dialog open:", err)
			}
			if res, err := call("dev", "c1", "dialog", map[string]any{"session": id, "accept": true}); err != nil || res["accepted"] != true {
				t.Fatal(res, err)
			}
			if res, err := call("dev", "c1", "observe", map[string]any{"session": id}); err != nil || res["title"] != "Form" {
				t.Fatal("after the dialog:", res, err)
			}
			// Действие идёт: другой вызов в том же сеансе — session_blocked с тем, что его держит.
			old := AgentWait
			AgentWait = 300 * time.Millisecond
			defer func() { AgentWait = old }()
			done := make(chan error, 1)
			go func() {
				_, err := call("dev", "c1", "act", map[string]any{"session": id, "selector": "#missing", "method": "click", "timeout_ms": 3000.0})
				done <- err
			}()
			time.Sleep(300 * time.Millisecond)
			if _, err := call("dev", "c1", "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), CodeSessionBlocked) ||
				!strings.Contains(err.Error(), "browser.act, running since") {
				t.Fatal("blocked:", err)
			}
			<-done
			if _, err := call("dev", "c1", "close", map[string]any{"session": id}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Критерий задачи: два агента (и два клиента одного агента) работают каждый в
// своём сеансе одновременно — в своих вкладках; close --all одного не трогает
// сеансы другого, а закрытый сеанс говорит, кто его закрыл.
func TestTwoAgentsRealChrome(t *testing.T) {
	srv := formPage()
	defer srv.Close()
	b, call := chromeBrowser(t, "cdp", srv.URL)
	a, err := call("dev", "session A", "open", map[string]any{"url": srv.URL + "/form"})
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	other, err := call("dev", "session B", "open", map[string]any{"url": srv.URL + "/other"})
	if err != nil {
		t.Fatal("a second client of the same agent:", err)
	}
	third, err := call("bot", "", "open", map[string]any{"url": srv.URL + "/other"})
	if err != nil {
		t.Fatal("another agent:", err)
	}
	if res, err := call("dev", "session A", "observe", map[string]any{"session": a["session"]}); err != nil || res["title"] != "Form" {
		t.Fatal("A's page is its own:", res, err)
	}
	if res, err := call("dev", "session B", "close", map[string]any{"all": true}); err != nil || res["sessions"] != 1 {
		t.Fatal("close --all of B closes only B's session:", res, err)
	}
	if res, err := call("dev", "session A", "observe", map[string]any{"session": a["session"]}); err != nil || res["title"] != "Form" {
		t.Fatal("A survives B's close --all:", res, err)
	}
	if res, err := call("bot", "", "observe", map[string]any{"session": third["session"]}); err != nil || res["title"] != "Other" {
		t.Fatal("the other agent survives:", res, err)
	}
	if _, err := call("dev", "session B", "observe", map[string]any{"session": other["session"]}); err == nil ||
		!strings.Contains(err.Error(), "closed by browser.close --all of dev (session B)") {
		t.Fatal("who closed it:", err)
	}
	b.mu.Lock()
	n := len(b.agents)
	b.mu.Unlock()
	if n != 2 {
		t.Fatal("open sessions:", n)
	}
}
