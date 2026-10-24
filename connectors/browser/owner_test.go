// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/connectors/browser/worker"
)

func owner(t *testing.T, b *Browser, op string, args map[string]any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return b.Owner(ctx, op, raw)
}

// Список сеансов, управление владельцем (агент ждёт), закрытие владельцем.
func TestOwnerSessions(t *testing.T) {
	_, b, _ := setup(t, nil)
	var mu sync.Mutex
	var events []string
	b.SetEmit(func(kind string, data any) {
		mu.Lock()
		events = append(events, kind)
		mu.Unlock()
	})
	res, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := res["session"].(string)
	st, err := owner(t, b, "sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	ss := st.(BrowserStatus).Sessions
	if len(ss) != 1 || ss[0].ID != id || ss[0].State != StateIdle || ss[0].URL != "https://a.example.com/" || ss[0].Closes.IsZero() {
		t.Fatalf("%+v", ss)
	}
	if _, err := owner(t, b, "input", map[string]any{"session": id, "input": map[string]any{"type": "text", "text": "x"}}); err == nil || !strings.Contains(err.Error(), "take control") {
		t.Fatal("input without control while the agent works:", err)
	}
	if _, err := owner(t, b, "view", map[string]any{"session": id}); err == nil {
		t.Fatal("a fake engine cannot be shown")
	}
	in, err := owner(t, b, "control", map[string]any{"session": id, "on": true})
	if err != nil || in.(SessionInfo).State != StateUser {
		t.Fatal(in, err)
	}
	if _, err := agentExec(t, b, "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), CodeHeldByUser) {
		t.Fatal("the agent waits while the owner holds the session:", err)
	}
	if _, err := agentExec(t, b, "close", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), CodeHeldByUser) {
		t.Fatal("nor closes it:", err)
	}
	if _, err := owner(t, b, "control", map[string]any{"session": id, "on": false}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentExec(t, b, "observe", map[string]any{"session": id}); err != nil {
		t.Fatal("given back:", err)
	}
	if _, err := owner(t, b, "close", map[string]any{"session": id}); err != nil {
		t.Fatal(err)
	}
	if _, err := agentExec(t, b, "observe", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), "closed by the user in gate4ai") {
		t.Fatal("who closed it:", err)
	}
	if _, err := owner(t, b, "view", map[string]any{"session": id}); err == nil || !strings.Contains(err.Error(), "closed or unknown") {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(events, " "), "state") || events[len(events)-1] != "closed" {
		t.Fatal(events)
	}
}

// Настоящий Chrome: кадры сеанса уходят хосту; ввод — только под управлением.
func TestOwnerViewRealChrome(t *testing.T) {
	srv := formPage()
	defer srv.Close()
	b, call := chromeBrowser(t, "cdp", srv.URL)
	frames := make(chan map[string]any, 100)
	b.SetEmit(func(kind string, data any) {
		if kind == "frame" {
			select {
			case frames <- data.(map[string]any):
			default:
			}
		}
	})
	res, err := call("dev", "", "open", map[string]any{"url": srv.URL + "/form"})
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	id := res["session"].(string)
	if _, err := owner(t, b, "view", map[string]any{"session": id, "max_width": 400}); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-frames:
		if f["session"] != id || !bytes.HasPrefix(f["jpeg"].([]byte), []byte{0xff, 0xd8}) || !strings.HasSuffix(f["url"].(string), "/form") || f["state"] != StateIdle {
			t.Fatal(f["session"], f["url"], f["state"])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no frame")
	}
	if st, _ := owner(t, b, "sessions", nil); !st.(BrowserStatus).Sessions[0].Watched {
		t.Fatal("watched")
	}
	if _, err := owner(t, b, "control", map[string]any{"session": id, "on": true}); err != nil {
		t.Fatal(err)
	}
	click := func(a string) map[string]any {
		return map[string]any{"session": id, "input": worker.Input{Type: "mouse", Action: a, X: 0.02, Y: 0.02, Clicks: 1}}
	}
	for _, a := range []string{"down", "up"} {
		if _, err := owner(t, b, "input", click(a)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner(t, b, "input", map[string]any{"session": id, "input": worker.Input{Type: "text", Text: "от человека"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner(t, b, "control", map[string]any{"session": id, "on": false}); err != nil {
		t.Fatal(err)
	}
	r, err := call("dev", "", "act", map[string]any{"session": id, "selector": "#q", "method": "wait"})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := r["value"].(*worker.FieldValue); v != nil && v.Start != "от человека" {
		t.Fatal(v)
	}
	var got string
	b.mu.Lock()
	as := b.agents[id]
	b.mu.Unlock()
	as.s.Executor.Engine.Eval(context.Background(), "q.value", &got)
	if got != "от человека" {
		t.Fatal("the owner's text:", got)
	}
	if _, err := owner(t, b, "unview", map[string]any{"session": id}); err != nil {
		t.Fatal(err)
	}
}
