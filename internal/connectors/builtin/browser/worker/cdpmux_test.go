// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// pipeChrome — фейковый Chrome на CDP-pipe: сообщения через \0, одно
// подключение. Цели T1…, сессии S1…; attachToTarget сначала шлёт событие
// attachedToTarget, потом ответ (как Chrome); setAutoAttach браузера
// подключается к новым целям сам.
type pipeChrome struct {
	headless bool

	mu       sync.Mutex
	w        io.Writer
	n        int
	auto     bool
	got      []map[string]any // все запросы
	sessions map[string]string
}

func (f *pipeChrome) emit(m map[string]any) {
	b, _ := json.Marshal(m)
	f.w.Write(append(b, 0))
}

func (f *pipeChrome) serve(r io.Reader, w io.Writer) {
	f.w = w
	f.sessions = map[string]string{}
	br := bufio.NewReader(r)
	for {
		b, err := br.ReadBytes(0)
		if err != nil {
			return
		}
		var m map[string]any
		json.Unmarshal(b[:len(b)-1], &m)
		f.mu.Lock()
		f.got = append(f.got, m)
		p, _ := m["params"].(map[string]any)
		var res any = map[string]any{}
		switch m["method"] {
		case "Browser.getVersion":
			ua := "Mozilla/5.0 (X11; Linux x86_64) Chrome/155.0.0.0 Safari/537.36"
			if f.headless {
				ua = strings.Replace(ua, "Chrome/", "HeadlessChrome/", 1) // product у --headless=new — тот же «Chrome/…»
			}
			res = map[string]any{"product": "Chrome/155.0.0.0", "userAgent": ua, "protocolVersion": "1.3"}
		case "Target.createTarget":
			f.n++
			id := "T" + itoa(f.n)
			res = map[string]any{"targetId": id}
			f.emit(map[string]any{"method": "Target.targetCreated", "params": map[string]any{"targetInfo": map[string]any{"targetId": id, "type": "page", "url": "about:blank"}}})
			if f.auto {
				s := "A" + itoa(f.n)
				f.sessions[s] = id
				f.emit(map[string]any{"method": "Target.attachedToTarget", "params": map[string]any{"sessionId": s, "targetInfo": map[string]any{"targetId": id, "type": "page"}, "waitingForDebugger": true}})
			}
		case "Target.attachToTarget":
			f.n++
			s := "S" + itoa(f.n)
			f.sessions[s] = p["targetId"].(string)
			f.emit(map[string]any{"method": "Target.attachedToTarget", "params": map[string]any{"sessionId": s, "targetInfo": map[string]any{"targetId": p["targetId"], "type": "page"}}})
			res = map[string]any{"sessionId": s}
		case "Target.setAutoAttach":
			f.auto, _ = p["autoAttach"].(bool)
		case "Runtime.evaluate":
			if strings.Contains(p["expression"].(string), "getHighEntropyValues") {
				res = map[string]any{"result": map[string]any{"value": map[string]any{"platform": "Linux", "architecture": "x86", "bitness": "64",
					"brands": []any{map[string]any{"brand": "Google Chrome", "version": "155"}}, "fullVersionList": []any{map[string]any{"brand": "Google Chrome", "version": "155.0.0.0"}}}}}
			}
		case "Page.navigate":
			if s, _ := m["sessionId"].(string); s != "" {
				f.emit(map[string]any{"method": "Page.frameNavigated", "sessionId": s, "params": map[string]any{"url": p["url"]}})
			}
		}
		out := map[string]any{"id": m["id"], "result": res}
		if s, ok := m["sessionId"]; ok {
			out["sessionId"] = s
		}
		f.emit(out)
		f.mu.Unlock()
	}
}

func (f *pipeChrome) requests(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, m := range f.got {
		if m["method"] == method {
			out = append(out, m)
		}
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// startPipeChrome — Mux поверх фейкового Chrome в памяти.
func startPipeChrome(t *testing.T, headless bool) (*Mux, *pipeChrome) {
	t.Helper()
	toChromeR, toChromeW := io.Pipe()
	fromChromeR, fromChromeW := io.Pipe()
	f := &pipeChrome{headless: headless}
	go f.serve(toChromeR, fromChromeW)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := StartMux(ctx, fromChromeR, toChromeW)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(); toChromeW.Close(); fromChromeW.Close() })
	return m, f
}

// muxConn — клиент Mux: запросы со своими id, события — в канал.
type muxConn struct {
	c      *websocket.Conn
	events chan map[string]any
	resp   chan map[string]any
}

func dialMux(t *testing.T, m *Mux) *muxConn {
	t.Helper()
	ctx := context.Background()
	ws, err := BrowserWSURL(ctx, m.URL())
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	mc := &muxConn{c: c, events: make(chan map[string]any, 100), resp: make(chan map[string]any, 100)}
	go func() {
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]any
			json.Unmarshal(b, &msg)
			if _, ok := msg["id"]; ok {
				mc.resp <- msg
			} else {
				mc.events <- msg
			}
		}
	}()
	t.Cleanup(func() { c.Close(websocket.StatusNormalClosure, "") })
	return mc
}

func (mc *muxConn) call(t *testing.T, id int, method string, params map[string]any, session string) map[string]any {
	t.Helper()
	msg := map[string]any{"id": id, "method": method, "params": params}
	if session != "" {
		msg["sessionId"] = session
	}
	b, _ := json.Marshal(msg)
	if err := mc.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-mc.resp:
		if r["id"] != float64(id) {
			t.Fatalf("%s: response id %v, want %d", method, r["id"], id)
		}
		res, _ := r["result"].(map[string]any)
		return res
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no response", method)
	}
	return nil
}

// drain — события клиента за короткое время.
func (mc *muxConn) drain(d time.Duration) []map[string]any {
	var out []map[string]any
	t := time.After(d)
	for {
		select {
		case e := <-mc.events:
			out = append(out, e)
		case <-t:
			return out
		}
	}
}

func methods(evs []map[string]any) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e["method"].(string))
	}
	return out
}

func TestMuxNeedsToken(t *testing.T) {
	m, _ := startPipeChrome(t, false)
	base := strings.TrimSuffix(m.URL(), "/t/"+m.token)
	for _, path := range []string{"/json/version", "/devtools/browser", "/t/wrong/json/version", "/t/" + m.token[:10] + "/json/version"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d without the token, want 404", path, resp.StatusCode)
		}
	}
	ws, err := BrowserWSURL(context.Background(), m.URL())
	if err != nil || !strings.Contains(ws, "/t/"+m.token+"/") {
		t.Fatal(ws, err)
	}
}

// Два клиента: свои id, свои сессии; события сессии — только владельцу,
// события целей — подписанным, уже известные цели — новому подписчику.
func TestMuxRoutesClients(t *testing.T) {
	m, f := startPipeChrome(t, false)
	a, b := dialMux(t, m), dialMux(t, m)
	tg := a.call(t, 1, "Target.createTarget", map[string]any{"url": "about:blank"}, "")["targetId"].(string)
	sa := a.call(t, 2, "Target.attachToTarget", map[string]any{"targetId": tg, "flatten": true}, "")["sessionId"].(string)
	sb := b.call(t, 1, "Target.attachToTarget", map[string]any{"targetId": tg, "flatten": true}, "")["sessionId"].(string)
	if sa == sb {
		t.Fatal("one session for two clients")
	}
	a.drain(100 * time.Millisecond)
	b.drain(100 * time.Millisecond)
	a.call(t, 3, "Page.navigate", map[string]any{"url": "https://a.example/"}, sa)
	if ev := a.drain(200 * time.Millisecond); len(ev) != 1 || ev[0]["sessionId"] != sa {
		t.Fatal("the session event goes to its owner:", ev)
	}
	if ev := b.drain(200 * time.Millisecond); len(ev) != 0 {
		t.Fatal("another client's session event leaked:", ev)
	}
	// Обнаружение целей: b подписывается — получает уже известную цель.
	b.call(t, 2, "Target.setDiscoverTargets", map[string]any{"discover": true}, "")
	if ev := b.drain(200 * time.Millisecond); len(ev) != 1 || ev[0]["method"] != "Target.targetCreated" {
		t.Fatal("known targets on subscribe:", methods(ev))
	}
	a.call(t, 4, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
	if ev := b.drain(200 * time.Millisecond); len(ev) != 1 || ev[0]["method"] != "Target.targetCreated" {
		t.Fatal("a new target to the subscriber:", methods(ev))
	}
	if ev := a.drain(200 * time.Millisecond); len(ev) != 0 {
		t.Fatal("target events to a client that did not subscribe:", methods(ev))
	}
	if n := len(f.requests("Target.setDiscoverTargets")); n != 1 {
		t.Fatal("the mux subscribes once, clients only locally:", n)
	}
	// Клиент ушёл — его сессия отцеплена.
	a.c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for len(f.requests("Target.detachFromTarget")) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	d := f.requests("Target.detachFromTarget")
	if len(d) != 1 || d[0]["params"].(map[string]any)["sessionId"] != sa {
		t.Fatal("the session of a gone client is detached:", d)
	}
}

// Auto-attach браузера: новые вкладки — подписчику, его сессии.
func TestMuxAutoAttach(t *testing.T) {
	m, f := startPipeChrome(t, false)
	ext, other := dialMux(t, m), dialMux(t, m)
	ext.call(t, 1, "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}, "")
	other.call(t, 1, "Target.createTarget", map[string]any{"url": "about:blank"}, "")
	ev := ext.drain(200 * time.Millisecond)
	var sid string
	for _, e := range ev {
		if e["method"] == "Target.attachedToTarget" {
			sid = e["params"].(map[string]any)["sessionId"].(string)
		}
	}
	if sid == "" {
		t.Fatal("auto-attach goes to its subscriber:", methods(ev))
	}
	if ev := other.drain(200 * time.Millisecond); len(ev) != 0 {
		t.Fatal("auto-attach leaked to another client:", methods(ev))
	}
	ext.c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for len(f.requests("Target.setAutoAttach")) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if r := f.requests("Target.setAutoAttach"); len(r) != 2 || r[1]["params"].(map[string]any)["autoAttach"] != false {
		t.Fatal("auto-attach is turned off with its last subscriber:", r)
	}
}

// Headless: UA без «HeadlessChrome» — в /json/version и на каждой вкладке,
// до ответа клиенту.
func TestMuxHeadlessUA(t *testing.T) {
	m, f := startPipeChrome(t, true)
	if !m.Headless() {
		t.Fatal("headless not detected")
	}
	resp, err := http.Get(m.URL() + "/json/version")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if ua, _ := v["User-Agent"].(string); strings.Contains(ua, "Headless") || !strings.Contains(ua, "Chrome/155") {
		t.Fatal(v)
	}
	c := dialMux(t, m)
	tg := c.call(t, 1, "Target.createTarget", map[string]any{"url": "about:blank"}, "")["targetId"].(string)
	s := c.call(t, 2, "Target.attachToTarget", map[string]any{"targetId": tg, "flatten": true}, "")["sessionId"].(string)
	c.call(t, 3, "Page.navigate", map[string]any{"url": "https://a.example/"}, s)
	// Маска пришла в Chrome раньше первой команды клиента на вкладке.
	f.mu.Lock()
	defer f.mu.Unlock()
	masked := false
	for _, r := range f.got {
		if r["sessionId"] != s {
			continue
		}
		if r["method"] == "Page.navigate" {
			break
		}
		if r["method"] == "Emulation.setUserAgentOverride" {
			p := r["params"].(map[string]any)
			md, _ := p["userAgentMetadata"].(map[string]any)
			masked = !strings.Contains(p["userAgent"].(string), "Headless") && md["fullVersionList"] != nil
		}
	}
	if !masked {
		t.Fatal("no user agent override on the tab before its first command")
	}
}

// Launch: Chrome без порта отладки — pipe в fd 3 / 4, адрес — Mux с токеном.
func TestLaunchDedicatedProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pipe launch is for Unix")
	}
	dir := t.TempDir()
	exe, _ := os.Executable()
	script := filepath.Join(dir, "chrome")
	argsFile := filepath.Join(dir, "args")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > "+argsFile+"\nGATE4AI_FAKE_PIPE_CHROME=1 exec '"+exe+"'\n"), 0o755)
	profile := filepath.Join(dir, "profile")
	u, proc, err := LaunchEnv(context.Background(), script, profile, []string{"GATE4AI_TEST_ENV=1"})
	if err != nil {
		t.Fatal(err)
	}
	defer StopChrome(proc, 3*time.Second)
	b, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--user-data-dir=" + profile, "--remote-debugging-pipe"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	for _, bad := range []string{"--remote-debugging-port", "headless", "--remote-allow-origins", "--enable-unsafe-extension-debugging"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("%s in %s", bad, b)
		}
	}
	if !strings.Contains(u, "/t/") {
		t.Fatal("the address has no token:", u)
	}
	// Через Mux до процесса Chrome и обратно.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dialBrowser(ctx, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var v map[string]any
	if err := c.call(ctx, "", "Browser.getVersion", nil, &v); err != nil || v["product"] != "Chrome/155.0.0.0" {
		t.Fatal(v, err)
	}
	// Chrome вышел — Mux закрыт, адрес не отвечает.
	StopChrome(proc, time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := BrowserWSURL(context.Background(), u); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mux outlived Chrome")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// fakePipeChromeMain — процесс фейкового Chrome для TestLaunchDedicatedProfile.
func fakePipeChromeMain() {
	if os.Getenv("GATE4AI_TEST_ENV") != "1" {
		os.Exit(3) // env из LaunchEnv не дошёл
	}
	(&pipeChrome{}).serve(os.NewFile(3, "in"), os.NewFile(4, "out"))
	os.Exit(0)
}
