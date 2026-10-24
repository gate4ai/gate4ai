// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// fakeBrowser — встроенный браузер: сеанс s-1, запросы владельца и события.
type fakeBrowser struct {
	mu   sync.Mutex
	ops  []string
	sink func(kind string, data json.RawMessage)
}

func (f *fakeBrowser) Manifest() manifest.Manifest {
	m, _ := manifest.Parse([]byte("connector: browser\nruntime: builtin\nactions:\n  - { name: open, class: read, input_schema: { type: object, properties: { url: { type: string }, session: { type: string } } } }\n"))
	return m
}

func (f *fakeBrowser) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	out := map[string]any{"url": "https://a.example/", "title": "A"}
	if s, _ := r.Args["session"].(string); r.Action == "open" && s == "" {
		out["session"] = "s-1"
	}
	return abi.ExecResult{Structured: out}, nil
}

func (f *fakeBrowser) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified}, nil
}
func (f *fakeBrowser) Close() error { return nil }

func (f *fakeBrowser) SetEventSink(s func(kind string, data json.RawMessage)) {
	f.mu.Lock()
	f.sink = s
	f.mu.Unlock()
}

func (f *fakeBrowser) Owner(_ context.Context, op string, args, out any) error {
	f.mu.Lock()
	f.ops = append(f.ops, op)
	f.mu.Unlock()
	if op == "sessions" && out != nil {
		b, _ := json.Marshal(map[string]any{"sessions": []any{map[string]any{"id": "s-1", "agent": "bot", "who": "bot", "state": "idle"}}, "display": "headless", "warning": "no screen"})
		return json.Unmarshal(b, out)
	}
	return nil
}

func (f *fakeBrowser) event(kind string, v any) {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	s := f.sink
	f.mu.Unlock()
	s(kind, b)
}

func (f *fakeBrowser) opsSeen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.ops, " ")
}

func TestBrowserSessions(t *testing.T) {
	home := t.TempDir()
	fb := &fakeBrowser{}
	d, err := New(Options{Home: filepath.Join(home, "data"), Keys: keys.NewMemory(), Connectors: []connectors.Client{fb},
		InitialConfig: []byte("agents:\n  bot: { profile: p }\nprofiles:\n  p: { connectors: [browser], rules: [{ id: b, action: \"browser.*\", effect: allow }] }\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetLocalUI("http://127.0.0.1:7431")
	if u := d.BrowserViewURL("s-1"); u != "http://127.0.0.1:7431/browser/s-1" {
		t.Fatal(u)
	}
	d.AddPublicURL(func() string { return "https://dev1.relay.example/" })
	if u := d.BrowserViewURL("s-1"); u != "https://dev1.relay.example/ui/browser/s-1" {
		t.Fatal(u)
	}
	// browser.open с новым сеансом — view_url; с session — без него.
	ctx := context.Background()
	res := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "browser.open", Args: map[string]any{"url": "https://a.example/"}})
	if m, _ := res.Result.(map[string]any); m["view_url"] != "https://dev1.relay.example/ui/browser/s-1" {
		t.Fatalf("%+v", res)
	}
	res = d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "browser.open", Args: map[string]any{"url": "https://a.example/", "session": "s-1"}})
	if m, _ := res.Result.(map[string]any); m == nil || m["view_url"] != nil {
		t.Fatalf("view_url only for a new session: %+v", res)
	}
	st, err := d.BrowserStatus(ctx)
	if err != nil || len(st.Sessions) != 1 || st.Sessions[0].ViewURL != "https://dev1.relay.example/ui/browser/s-1" || st.Display != "headless" || !st.Running {
		t.Fatalf("%+v %v", st, err)
	}
	// Два зрителя: просмотр в пакете включает первый, выключает последний;
	// второй получает последний кадр сразу.
	ch1, stop1, err := d.WatchBrowser(ctx, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	fb.event("frame", map[string]any{"session": "s-1", "jpeg": []byte{0xff, 0xd8}, "width": 800, "height": 600, "url": "https://a.example/", "state": "idle"})
	if e := <-ch1; e.Kind != "frame" || e.Width != 800 || len(e.JPEG) != 2 {
		t.Fatalf("%+v", e)
	}
	ch2, stop2, err := d.WatchBrowser(ctx, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ch2:
		if e.Kind != "frame" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("the latest frame to a new viewer")
	}
	if n := strings.Count(fb.opsSeen(), "view"); n != 1 {
		t.Fatal("view once:", fb.opsSeen())
	}
	fb.event("state", map[string]any{"id": "s-1", "agent": "bot", "state": "user"})
	if e := <-ch1; e.Kind != "state" || e.State != "user" || e.Session.ViewURL == "" {
		t.Fatalf("%+v", e)
	}
	<-ch2
	stop1()
	if strings.Contains(fb.opsSeen(), "unview") {
		t.Fatal("unview while a viewer remains")
	}
	// Медленный зритель: кадры сверх очереди пропускаются, закрытие доходит.
	for range 20 {
		fb.event("frame", map[string]any{"session": "s-1", "jpeg": []byte{1}})
	}
	fb.event("closed", map[string]any{"session": "s-1", "reason": "closed by browser.close"})
	var last BrowserEvent
	for e := range ch2 {
		last = e
	}
	if last.Kind != "closed" || last.Reason != "closed by browser.close" {
		t.Fatalf("%+v", last)
	}
	stop2()
	// API владельца на сокете; агенту — нет.
	ownerSock, agentSock := filepath.Join(home, "run", "o.sock"), filepath.Join(home, "run", "a.sock")
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go d.Serve(sctx, ownerSock, agentSock)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(agentSock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner := client{c: ipc.HTTPClient(ownerSock)}
	var got BrowserStatus
	if code := owner.do(t, "GET", "/v1/browser/sessions", nil, &got); code != 200 || len(got.Sessions) != 1 {
		t.Fatal(code, got)
	}
	for _, op := range []string{"control", "release", "close"} {
		if code := owner.do(t, "POST", "/v1/browser/sessions/s-1/"+op, nil, nil); code != 200 {
			t.Fatal(op, code)
		}
	}
	if !strings.Contains(fb.opsSeen(), "control control close") {
		t.Fatal(fb.opsSeen())
	}
	agent := client{c: ipc.HTTPClient(agentSock)}
	if code := agent.do(t, "GET", "/v1/browser/sessions", nil, nil); code == 200 {
		t.Fatal("the sessions list is for the owner")
	}
}
