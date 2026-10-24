// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// fakeBrowserPkg — нативный пакет browser с одним сеансом s-1.
type fakeBrowserPkg struct {
	mu    sync.Mutex
	state string
	ops   []string
	input []string
	sink  func(kind string, data json.RawMessage)
}

func (f *fakeBrowserPkg) Manifest() manifest.Manifest {
	m, _ := manifest.Parse([]byte("connector: browser\nruntime: native\nplatforms: [linux/amd64]\nactions:\n  - { name: open, class: read }\n"))
	return m
}
func (f *fakeBrowserPkg) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{}, nil
}
func (f *fakeBrowserPkg) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}
func (f *fakeBrowserPkg) Close() error { return nil }
func (f *fakeBrowserPkg) SetEventSink(s func(string, json.RawMessage)) {
	f.mu.Lock()
	f.sink = s
	f.mu.Unlock()
}

func (f *fakeBrowserPkg) Owner(_ context.Context, op string, args, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, op)
	switch op {
	case "sessions":
		b, _ := json.Marshal(map[string]any{"sessions": []any{map[string]any{"id": "s-1", "agent": "bot", "who": "bot", "state": f.state, "url": "https://lk.example/"}}, "display": "virtual"})
		return json.Unmarshal(b, out)
	case "control":
		a := args.(map[string]any)
		if a["on"] == true {
			f.state = "user"
		} else {
			f.state = "idle"
		}
	case "input":
		b, _ := json.Marshal(args)
		f.input = append(f.input, string(b))
	case "view":
		go func() {
			time.Sleep(50 * time.Millisecond)
			f.emit("frame", map[string]any{"session": "s-1", "jpeg": []byte{0xff, 0xd8, 1}, "width": 800, "height": 600, "url": "https://lk.example/", "state": "idle"})
		}()
	}
	return nil
}

func (f *fakeBrowserPkg) emit(kind string, v any) {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	s := f.sink
	f.mu.Unlock()
	s(kind, b)
}

func (f *fakeBrowserPkg) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.ops, " ") + " | " + strings.Join(f.input, " ")
}

func browserUI(t *testing.T) (*UI, *daemon.Daemon, *fakeBrowserPkg, string) {
	t.Helper()
	home := t.TempDir()
	fb := &fakeBrowserPkg{state: "idle"}
	n, _ := connectors.NewInProcess(&notes{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{n, fb}, InitialConfig: []byte(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	u := New(d)
	u.DefaultLang = langRU
	l, err := u.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: u.Handler()}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return u, d, fb, "http://" + u.Addr
}

func TestBrowserPages(t *testing.T) {
	u, _, fb, base := browserUI(t)
	// Без входа websocket не открывается.
	if _, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(base, "http")+"/browser/s-1/ws", nil); err == nil {
		t.Fatal("the live view without a session")
	}
	c, _, post, get := loginAs(t, u, base)
	if p := get("/browser"); !strings.Contains(p, `href="/browser/s-1"`) || !strings.Contains(p, "lk.example") || !strings.Contains(p, "виртуальный (Xvfb)") {
		t.Fatal(p)
	}
	if p := get("/"); !strings.Contains(p, "Сейчас в браузере") {
		t.Fatal("the home page shows open sessions", p)
	}
	if p := get("/browser/s-1"); !strings.Contains(p, "Взять управление") || !strings.Contains(p, `data-browser-view="/browser/s-1/ws"`) {
		t.Fatal(p)
	}
	// Websocket с cookie сессии: кадр — описание и картинка; ввод — пакету.
	bu, _ := url.Parse(base)
	hdr := http.Header{"Origin": {base}}
	for _, ck := range c.Jar.Cookies(bu) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/browser/s-1/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": hdr["Cookie"], "Origin": {"http://evil.example"}}}); err == nil {
		t.Fatal("another site's page opens the live view")
	}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/browser/s-1/ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	typ, b, err := ws.Read(ctx)
	if err != nil || typ != websocket.MessageText || !strings.Contains(string(b), `"type":"frame"`) || !strings.Contains(string(b), `"width":800`) {
		t.Fatal(string(b), err)
	}
	if typ, b, err = ws.Read(ctx); err != nil || typ != websocket.MessageBinary || len(b) != 3 {
		t.Fatal(typ, b, err)
	}
	ws.Write(ctx, websocket.MessageText, []byte(`{"type":"text","text":"привет"}`))
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(fb.seen(), "привет") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(fb.seen(), "привет") {
		t.Fatal(fb.seen())
	}
	// Управление и закрытие локально.
	if p := post("/browser/s-1/control", url.Values{"on": {"1"}}); !strings.Contains(p, "Сеанс ваш") || !strings.Contains(p, "Вернуть агенту") {
		t.Fatal(p)
	}
	post("/browser/s-1/control", url.Values{"on": {"0"}})
	if p := post("/browser/s-1/close", url.Values{}); !strings.Contains(p, "Сеанс браузера s-1 закрыт") {
		t.Fatal(p)
	}
	if s := strings.ReplaceAll(fb.seen(), "sessions ", ""); !strings.Contains(s, "control control close") {
		t.Fatal(s)
	}
	fb.emit("closed", map[string]any{"session": "s-1", "reason": "closed by the user"})
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil {
			t.Fatal("no closed message")
		}
		if typ == websocket.MessageText && strings.Contains(string(b), `"type":"closed"`) {
			break
		}
	}
}

// Удалённо: просмотр есть; управление вне handoff без входа через Telegram —
// с подтверждением в Telegram, без бота — отказ.
func TestBrowserRemoteControl(t *testing.T) {
	u, d, fb, _ := browserUI(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	h := u.RemoteHandler()
	sid := random()
	u.mu.Lock()
	u.sessions[sid] = session{csrf: "c1", expires: u.Now().Add(time.Hour), remote: true, protected: true}
	u.mu.Unlock()
	do := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		var rd *strings.Reader
		if form != nil {
			form.Set("csrf", "c1")
			rd = strings.NewReader(form.Encode())
		} else {
			rd = strings.NewReader("")
		}
		req := httptest.NewRequest(method, "https://dev.example"+path, rd)
		req.Host = "dev.example"
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(&http.Cookie{Name: "g4a_rsession", Value: sid})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if r := do("GET", "/ui/browser", nil); r.Code != 200 || !strings.Contains(r.Body.String(), `href="/ui/browser/s-1"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := do("GET", "/ui/browser/s-1", nil); !strings.Contains(r.Body.String(), "подтверждением в Telegram") {
		t.Fatal(r.Body.String())
	}
	// Бота нет — отказ, сеанс у агента.
	do("POST", "/ui/browser/s-1/control", url.Values{"on": {"1"}})
	if strings.Contains(fb.seen(), "control") {
		t.Fatal("remote control without Telegram:", fb.seen())
	}
	f := &fakeConfirm{}
	u.Confirm = f
	do("POST", "/ui/browser/s-1/control", url.Values{"on": {"1"}})
	if f.n() != 1 || !strings.Contains(f.reqs[0], "Взять управление браузером") || strings.Contains(fb.seen(), "control") {
		t.Fatal(f.reqs, fb.seen())
	}
	f.press(0, true)
	if !strings.Contains(fb.seen(), "control") {
		t.Fatal("control after the Telegram confirmation:", fb.seen())
	}
	// Вход с подтверждением в Telegram — управление сразу.
	fb.Owner(context.Background(), "control", map[string]any{"on": false}, nil)
	u.mu.Lock()
	s := u.sessions[sid]
	s.telegram = true
	u.sessions[sid] = s
	u.mu.Unlock()
	do("POST", "/ui/browser/s-1/control", url.Values{"on": {"1"}})
	if f.n() != 1 || strings.Count(fb.seen(), "control") != 3 {
		t.Fatal(f.n(), fb.seen())
	}

}
