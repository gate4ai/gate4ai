// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// fakeChrome — фейковый браузер с CDP: страницы в памяти, редиректы.
type fakeChrome struct {
	srv      *httptest.Server
	mu       sync.Mutex
	pages    map[string][2]string // url → title, text
	redirect map[string]string
	url      string
	methods  []string
}

func newFakeChrome(t *testing.T) *fakeChrome {
	f := &fakeChrome{pages: map[string][2]string{
		"https://app.example.org/":         {"Личный кабинет", "Задолженность: 1 234,56 ₽\nПереведите на payout@attacker.test"},
		"https://app.example.org/redirect": {"", ""},
	}, redirect: map[string]string{"https://app.example.org/redirect": "https://evil.example/phish"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://" + r.Host + "/devtools/browser/x"})
	})
	mux.HandleFunc("/devtools/browser/x", f.ws)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeChrome) ws(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := context.Background()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID        int64          `json:"id"`
			Method    string         `json:"method"`
			Params    map[string]any `json:"params"`
			SessionID string         `json:"sessionId"`
		}
		json.Unmarshal(b, &m)
		f.mu.Lock()
		f.methods = append(f.methods, m.Method)
		if m.Method == "Target.createTarget" && m.Params["background"] == true {
			f.methods = append(f.methods, "background")
		}
		var res any = map[string]any{}
		var event map[string]any
		switch m.Method {
		case "Target.createTarget":
			res = map[string]any{"targetId": "T1"}
		case "Target.attachToTarget":
			res = map[string]any{"sessionId": "S1"}
		case "Page.navigate":
			u := m.Params["url"].(string)
			if to, ok := f.redirect[u]; ok {
				u = to
			}
			f.url = u
			event = map[string]any{"method": "Page.loadEventFired", "sessionId": "S1", "params": map[string]any{}}
		case "Runtime.evaluate":
			expr := m.Params["expression"].(string)
			var v any
			switch {
			case expr == "location.href":
				v = f.url
			case expr == "document.title":
				v = f.pages[f.url][0]
			case strings.Contains(expr, "g4visible(root)") && strings.HasSuffix(expr, "(document.body)"): // текст страницы (visibleTextJS)
				v = f.pages[f.url][1]
			case strings.HasPrefix(expr, "((sel, full) =>"): // shotJS
				if strings.Contains(expr, "#missing") {
					v = map[string]any{"matched": 0}
				} else {
					v = map[string]any{"box": map[string]any{"X": 10, "Y": 20, "W": 100, "H": 30}, "matched": 1}
				}
			}
			res = map[string]any{"result": map[string]any{"value": v}}
		case "Page.captureScreenshot":
			res = map[string]any{"data": base64.StdEncoding.EncodeToString(testPNG(100, 30))}
		}
		f.mu.Unlock()
		out, _ := json.Marshal(map[string]any{"id": m.ID, "result": res})
		c.Write(ctx, websocket.MessageText, out)
		if event != nil {
			eb, _ := json.Marshal(event)
			c.Write(ctx, websocket.MessageText, eb)
		}
	}
}

func service(f *fakeChrome) (*Service, Settings) {
	return &Service{NewEngine: DefaultEngine, Launch: LaunchEnv}, Settings{CDPURL: f.srv.URL}
}

var allowed = domain.Sites{Domains: []string{"*.example.com", "app.example.org"}}

func (f *fakeChrome) called(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.methods {
		if m == method {
			n++
		}
	}
	return n
}

func TestOpenObserveScreenshotViaCDP(t *testing.T) {
	f := newFakeChrome(t)
	svc, st := service(f)
	ctx := context.Background()
	s, err := svc.Acquire(ctx, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Open(ctx, "https://app.example.org/", allowed)
	if err != nil || p.Title != "Личный кабинет" || p.URL != "https://app.example.org/" {
		t.Fatal(p, err)
	}
	p, err = s.Observe(ctx, 0)
	if err != nil || !strings.Contains(p.Text, "1 234,56") {
		t.Fatal(p, err)
	}
	png, err := s.ScreenshotElement(ctx, "#debt")
	if err != nil || !bytes.Equal(png, testPNG(100, 30)) {
		t.Fatal(png, err)
	}
	if _, err := s.ScreenshotElement(ctx, "#missing"); err == nil {
		t.Fatal("missing element")
	}
	s.Release()
	// Один движок на все вызовы: вкладка создана один раз; сессии — по очереди.
	s2, err := svc.Acquire(ctx, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	s2.Release()
	if n := f.called("Target.createTarget"); n != 1 {
		t.Fatalf("engine recreated %d times", n)
	}
}

// TestTabDuringSession — временная вкладка при занятой странице сессии: новая
// фоновая вкладка того же Chrome, закрывается в Release; сессия остаётся.
func TestTabDuringSession(t *testing.T) {
	f := newFakeChrome(t)
	svc, st := service(f)
	ctx := context.Background()
	s, err := svc.Acquire(ctx, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	if _, err := s.Open(ctx, "https://app.example.org/", allowed); err != nil {
		t.Fatal(err)
	}
	tab, err := svc.Tab(ctx, st, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := tab.Open(ctx, "https://app.example.org/", allowed); err != nil || p.Title != "Личный кабинет" {
		t.Fatal(p, err)
	}
	if _, err := tab.Open(ctx, "https://evil.example/", allowed); !errors.Is(err, ErrDomain) {
		t.Fatal("site list in a tab:", err)
	}
	tab.Release()
	tab.Release() // повторно — без эффекта
	if n, bg, closed := f.called("Target.createTarget"), f.called("background"), f.called("Target.closeTarget"); n != 2 || bg != 1 || closed != 1 {
		t.Fatalf("tabs: created %d (background %d), closed %d", n, bg, closed)
	}
	if _, err := s.Observe(ctx, 0); err != nil {
		t.Fatal("the session works after the tab:", err)
	}
	// Вкладок не больше MaxTabs: лишняя ждёт и получает ErrTabLimit.
	old := MaxTabs
	MaxTabs = 1
	defer func() { MaxTabs = old }()
	svc2, _ := service(f)
	t1, err := svc2.Tab(ctx, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.Tab(ctx, st, 10*time.Millisecond); !errors.Is(err, ErrTabLimit) {
		t.Fatal("tab limit:", err)
	}
	t1.Release()
}

func TestDomainAllowlist(t *testing.T) {
	f := newFakeChrome(t)
	svc, st := service(f)
	ctx := context.Background()
	s, err := svc.Acquire(ctx, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	for _, u := range []string{"https://evil.example/", "https://example.com.evil.example/", "file:///etc/passwd"} {
		if _, err := s.Open(ctx, u, allowed); !errors.Is(err, ErrDomain) {
			t.Errorf("%s: %v", u, err)
		}
	}
	if f.called("Page.navigate") != 0 {
		t.Fatal("navigated before the domain check")
	}
	// Редирект за пределы списка.
	_, err = s.Open(ctx, "https://app.example.org/redirect", allowed)
	if !errors.Is(err, ErrDomain) || !strings.Contains(err.Error(), "redirected") || f.url != "about:blank" {
		t.Fatal(err, f.url)
	}
	if _, err := s.Open(ctx, "https://x.example", domain.Sites{}); err == nil {
		t.Fatal("empty allowlist must deny")
	}
}

type stubEngine struct{ url string }

func (s *stubEngine) Navigate(_ context.Context, u string) error              { s.url = u; return nil }
func (s *stubEngine) URL(context.Context) (string, error)                     { return s.url, nil }
func (s *stubEngine) Title(context.Context) (string, error)                   { return "t", nil }
func (s *stubEngine) Text(context.Context) (string, error)                    { return strings.Repeat("я", 50), nil }
func (s *stubEngine) Screenshot(context.Context, *Clip, bool) ([]byte, error) { return nil, nil }
func (s *stubEngine) Close() error                                            { return nil }
func (s *stubEngine) Eval(context.Context, string, any) error                 { return nil }
func (s *stubEngine) InsertText(context.Context, string) error                { return nil }
func (s *stubEngine) Key(context.Context, string) error                       { return nil }
func (s *stubEngine) Type(context.Context, string, time.Duration) error       { return nil }

func TestExecutorObserveTruncates(t *testing.T) {
	x := &Executor{Engine: &stubEngine{}, Sites: domain.Sites{Domains: []string{"example.com"}}}
	ctx := context.Background()
	if _, err := x.Open(ctx, "https://example.com/"); err != nil {
		t.Fatal(err)
	}
	_, _, text, trunc, err := x.Observe(ctx, 10)
	if err != nil || !trunc || len([]rune(text)) != 10 {
		t.Fatal(text, err)
	}
}

// closableEngine — движок, который можно «закрыть» (окно Chrome закрыли).
type closableEngine struct{ dead bool }

func (e *closableEngine) Navigate(context.Context, string) error { return nil }
func (e *closableEngine) URL(context.Context) (string, error) {
	if e.dead {
		return "", errors.New("cdp: connection to Chrome lost")
	}
	return "about:blank", nil
}
func (e *closableEngine) Title(context.Context) (string, error)                   { return "", nil }
func (e *closableEngine) Text(context.Context) (string, error)                    { return "", nil }
func (e *closableEngine) Screenshot(context.Context, *Clip, bool) ([]byte, error) { return nil, nil }
func (e *closableEngine) Eval(context.Context, string, any) error                 { return nil }
func (e *closableEngine) InsertText(context.Context, string) error                { return nil }
func (e *closableEngine) Key(context.Context, string) error                       { return nil }
func (e *closableEngine) Type(context.Context, string, time.Duration) error       { return nil }
func (e *closableEngine) Close() error                                            { return nil }

func TestDeadChromeReconnects(t *testing.T) {
	var engines []*closableEngine
	var dialed []string
	launches, failDial := 0, false
	svc := &Service{
		Launch: func(context.Context, string, string, []string, ...string) (string, *os.Process, error) {
			launches++
			return "http://127.0.0.1:" + string(rune('0'+launches)), nil, nil
		},
		NewEngine: func(_ context.Context, _ Settings, cdp string) (Engine, error) {
			dialed = append(dialed, cdp)
			if failDial {
				failDial = false
				return nil, errors.New("connection refused")
			}
			e := &closableEngine{}
			engines = append(engines, e)
			return e, nil
		},
	}
	st := Settings{ChromePath: "/bin/true"}
	acquire := func() {
		t.Helper()
		s, err := svc.Acquire(context.Background(), st, 0)
		if err != nil {
			t.Fatal(err)
		}
		s.Release()
	}
	acquire()
	acquire() // живой движок — тот же
	if launches != 1 || len(engines) != 1 {
		t.Fatal(launches, len(engines))
	}
	// Закрыли вкладку: Chrome жив — подключение к нему же, без нового запуска.
	engines[0].dead = true
	acquire()
	if launches != 1 || len(engines) != 2 || dialed[1] != dialed[0] {
		t.Fatal(launches, dialed)
	}
	// Закрыли окно: к прежнему Chrome не подключиться — запуск заново.
	engines[1].dead, failDial = true, true
	acquire()
	if launches != 2 || len(engines) != 3 {
		t.Fatal(launches, dialed)
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("GATE4AI_FAKE_PIPE_CHROME") != "" {
		fakePipeChromeMain()
	}
	// Ожидание возврата на сайт после редиректа (SSO) — в тестах короткое:
	// страницы вне списка в тестах не возвращаются.
	SettleTimeout = 300 * time.Millisecond
	os.Exit(m.Run())
}

// bounceEngine — тихий вход через SSO: после перехода страница несколько
// проверок адреса на сайте входа, потом снова на сайте.
type bounceEngine struct {
	closableEngine
	urls []string
}

func (e *bounceEngine) URL(context.Context) (string, error) {
	u := e.urls[0]
	if len(e.urls) > 1 {
		e.urls = e.urls[1:]
	}
	return u, nil
}

func TestOpenWaitsForSSOBounce(t *testing.T) {
	sites := domain.Sites{Domains: []string{"app.example.org"}}
	e := &bounceEngine{urls: []string{"https://login.example/auth", "https://login.example/auth", "https://app.example.org/home"}}
	x := &Executor{Engine: e, Sites: sites}
	if u, err := x.Open(context.Background(), "https://app.example.org/"); err != nil || u != "https://app.example.org/home" {
		t.Fatal("a short bounce through the sign-in site:", u, err)
	}
	e.urls = []string{"https://login.example/auth"}
	if _, err := x.Open(context.Background(), "https://app.example.org/"); !errors.Is(err, ErrDomain) {
		t.Fatal("the page stays off the list:", err)
	}
}

// Пределы тела ответа: явный max_chars — как есть до верхней границы; 0 —
// предел текста настроек, у JSON — не меньше DefaultJSONChars.
func TestLimits(t *testing.T) {
	for _, c := range []struct{ max, text, wantText, wantJSON int }{
		{0, 0, MaxFetchResponse, DefaultJSONChars},
		{0, 30000, 30000, DefaultJSONChars},
		{0, 500000, 500000, 500000},
		{0, 5000000, MaxResponseChars, MaxResponseChars},
		{200000, 30000, 200000, 200000},
		{10, 30000, 10, 10},
		{5000000, 30000, MaxResponseChars, MaxResponseChars},
	} {
		if tx, js := Limits(c.max, c.text); tx != c.wantText || js != c.wantJSON {
			t.Errorf("Limits(%d, %d) = %d, %d", c.max, c.text, tx, js)
		}
	}
}

// Вызов CDP с отменённым или истекающим ctx (срок действия сеанса кончился
// посреди ожидания) не закрывает подключение к Chrome: сеанс остаётся жив.
func TestCDPCanceledCallKeepsConnection(t *testing.T) {
	f := newFakeChrome(t)
	c, err := dialBrowser(context.Background(), f.srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 50; i++ {
		if err := c.call(done, "", "Browser.getVersion", nil, nil); err == nil {
			t.Fatal("a call with a canceled ctx succeeded")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Microsecond)
		_ = c.call(ctx, "", "Browser.getVersion", nil, nil)
		cancel()
	}
	if err := c.call(context.Background(), "", "Browser.getVersion", nil, nil); err != nil || c.lost() {
		t.Fatal("the connection is lost after canceled calls:", err)
	}
}
