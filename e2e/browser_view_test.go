// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"time"

	"github.com/chromedp/chromedp"

	"github.com/gate4ai/gate4ai/e2e/testsite"
)

// Просмотр сеанса агента в настоящем браузере: browser.open отдаёт view_url,
// владелец видит сеанс в gate4ai browser sessions и в Web UI (кадр на canvas),
// берёт управление — клик и ввод на canvas доходят до страницы, агент тем
// временем получает session_held_by_user; вернул — агент видит сделанное.
// Chrome пакета запущен без порта отладки (pipe), в режиме headless.
func TestBrowserLiveView(t *testing.T) {
	t.Parallel()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		if chrome, err = exec.LookPath("chromium"); err != nil {
			t.Skip("needs Chrome")
		}
	}
	srv := httptest.NewServer(testsite.New().Handler()) // /live — крупные кнопка и поле
	defer srv.Close()
	g := startGate(t, "--ui", "127.0.0.1:0")
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile")
	cfg := fmt.Sprintf(`agents:
  claude-code: { profile: dev }
profiles:
  dev:
    connectors: [browser]
    rules:
      - { id: dev-browser-allow, action: "browser.*", effect: allow }
access:
  sites: [{ domain: "127.0.0.1", access: allow }]
connectors:
  browser:
    engine: cdp
    display: headless
    chrome_path: %q
    profile_dir: %q
`, chrome, profile)
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0o600)
	if r := g.run("", "", "policy", "set", cfgFile, "-m", "dev"); r.code != 0 {
		t.Fatal(r.out)
	}
	tok := g.run("", "", "agents", "token", "claude-code").json(t)["token"].(string)
	r := g.run(tok, "", "browser", "open", "--url", srv.URL+"/live")
	if r.code != 0 {
		t.Fatal(r.out)
	}
	res := r.json(t)["result"].(map[string]any)
	sid, view := res["session"].(string), res["view_url"].(string)
	if !strings.HasPrefix(view, "http://127.0.0.1:") || !strings.HasSuffix(view, "/browser/"+sid) {
		t.Fatalf("view_url: %q", view)
	}
	// Владелец: сеанс в списке с той же ссылкой; режим — headless с предупреждением.
	st := g.run("", "", "browser", "sessions", "--json").json(t)
	ss, _ := st["sessions"].([]any)
	if len(ss) != 1 || ss[0].(map[string]any)["view_url"] != view || st["display"] != "headless" || st["warning"] == "" {
		t.Fatalf("sessions: %v", st)
	}
	if runtime.GOOS == "linux" {
		// Chrome пакета — без порта отладки: CDP по pipe.
		cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
		found := false
		for _, f := range cmdlines {
			b, _ := os.ReadFile(f)
			a := strings.ReplaceAll(string(b), "\x00", " ")
			if strings.Contains(a, "--user-data-dir="+profile) && !strings.Contains(a, "--type=") {
				found = true
				if strings.Contains(a, "--remote-debugging-port") || !strings.Contains(a, "--remote-debugging-pipe") {
					t.Fatalf("chrome args: %s", a)
				}
			}
		}
		if !found {
			t.Fatal("the package Chrome is not running")
		}
	}

	login := strings.TrimSpace(g.run("", "", "ui", "--print").out)
	// Окно просмотрщика — большое: canvas целиком в видимой области,
	// иначе клик CDP ниже края окна headless (800×600 по умолчанию) ни во что не попадает.
	actx, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome), chromedp.WindowSize(1280, 1600))...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	frame := `(() => { const c = document.querySelector('[data-browser-canvas]'); return !!c && c.width > 100 })()`
	// frames — сколько кадров нарисовал просмотрщик (data-frames ставит app.js).
	frames := func(ctx context.Context) int {
		var n int
		chromedp.Evaluate(`parseInt(document.querySelector('[data-browser-canvas]').getAttribute('data-frames'), 10) || 0`, &n).Do(ctx)
		return n
	}
	// nextFrame — ждать кадр после текущего: действие в сеансе дошло до Chrome
	// агента и вернулось картинкой.
	nextFrame := func() chromedp.Action {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			n := frames(ctx)
			return chromedp.Poll(fmt.Sprintf(`(parseInt(document.querySelector('[data-browser-canvas]').getAttribute('data-frames'), 10) || 0) > %d`, n), nil, chromedp.WithPollingTimeout(15*time.Second)).Do(ctx)
		})
	}
	// keysFocused — поле клавиш просмотрщика в фокусе: иначе набор уйдёт мимо.
	keysFocused := chromedp.Poll(`document.activeElement === document.querySelector('[data-browser-keys]')`, nil, chromedp.WithPollingTimeout(10*time.Second))
	// viewerState — состояние просмотрщика для диагностики падения.
	viewerState := func() string {
		var s string
		js := "(() => { const a = document.activeElement, n = document.querySelector('[data-browser-note]'), c = document.querySelector('[data-browser-canvas]');" +
			" return 'activeElement=' + (a ? a.tagName + Object.keys(a.dataset).map(k => '[data-' + k + ']').join('') : 'none') +" +
			" ' note=' + JSON.stringify(n ? n.textContent : null) + ' frames=' + (c ? c.getAttribute('data-frames') : null) +" +
			" ' canvas=' + (c ? JSON.stringify(c.getBoundingClientRect()) : null) + ' viewport=' + innerWidth + 'x' + innerHeight +" +
			" ' hasFocus=' + document.hasFocus() + ' pointerdowns=' + window.__pointerdowns })()"
		chromedp.Run(ctx, chromedp.Evaluate(js, &s))
		return s
	}
	// click — клик по canvas в точке, заданной долями его размера. Точка должна
	// быть в видимой области окна: клик CDP за её краем не попадает ни в один элемент.
	click := func(fx, fy float64) chromedp.Action {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			var p []float64
			js := `(() => { const c = document.querySelector('[data-browser-canvas]'); c.scrollIntoView({ block: "nearest" });` +
				` const r = c.getBoundingClientRect(); return [r.left + r.width*%v, r.top + r.height*%v, innerWidth, innerHeight] })()`
			if err := chromedp.Evaluate(fmt.Sprintf(js, fx, fy), &p).Do(ctx); err != nil {
				return err
			}
			if p[0] < 0 || p[1] < 0 || p[0] >= p[2] || p[1] >= p[3] {
				return fmt.Errorf("click (%v, %v) of the canvas is at (%.0f, %.0f), outside the viewport %.0fx%.0f", fx, fy, p[0], p[1], p[2], p[3])
			}
			return chromedp.MouseClickXY(p[0], p[1]).Do(ctx)
		})
	}
	err = chromedp.Run(ctx,
		chromedp.Navigate(login),
		chromedp.WaitReady(`form[action="/logout"]`),
		chromedp.Navigate(view),
		chromedp.Poll(frame, nil),
		chromedp.Click(`//button[text()="Take control"]`),
		chromedp.WaitVisible(`//button[text()="Give back to the agent"]`),
		chromedp.Poll(frame, nil),
		// ws просмотрщика после перезагрузки страницы открыт: кадр пришёл и надпись «Connecting…» снята.
		chromedp.Poll(`document.querySelector('[data-browser-note]').textContent === ''`, nil, chromedp.WithPollingTimeout(15*time.Second)),
		// Диагностика: сколько pointerdown дошло до canvas (клик CDP мимо canvas его не даёт).
		chromedp.Evaluate(`(() => { window.__pointerdowns = 0; document.querySelector('[data-browser-canvas]').addEventListener('pointerdown', () => window.__pointerdowns++, true); return 0 })()`, new(int)),
		click(0.1, 0.1),
		nextFrame(), // клик дошёл до страницы агента (кнопка перерисовалась)
		click(0.3, 0.65),
		keysFocused,
		nextFrame(), // поле на странице агента в фокусе
		chromedp.KeyEvent("hi"),
		nextFrame(), // набранное отрисовано
	)
	if err != nil {
		t.Fatalf("%v (%s)", err, viewerState())
	}
	if r := g.run(tok, "", "browser", "observe", "--session", sid); r.code == 0 || !strings.Contains(r.out, "session_held_by_user") {
		t.Fatalf("the agent waits while the owner holds the session: %d %s", r.code, r.out)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`//button[text()="Give back to the agent"]`), chromedp.WaitVisible(`//button[text()="Take control"]`)); err != nil {
		t.Fatal(err)
	}
	// Клик и ввод доходят до страницы асинхронно (websocket → daemon → CDP):
	// ждём их в тексте страницы опросом, а не одним observe.
	deadline := time.Now().Add(10 * time.Second)
	for {
		r = g.run(tok, "", "browser", "observe", "--session", sid)
		if r.code == 0 && strings.Contains(r.out, "clicked 1") && strings.Contains(r.out, "typed hi") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the owner's click and typing did not reach the page: %d %s\nviewer: %s", r.code, r.out, viewerState())
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Без входа в Web UI ссылка не показывает сеанс.
	resp, err := http.Get(view)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || strings.Contains(string(page), "data-browser-view") || strings.Contains(string(page), srv.URL) {
		t.Fatal(resp.StatusCode, string(page))
	}
	if r := g.run(tok, "", "browser", "close", "--session", sid); r.code != 0 {
		t.Fatal(r.out)
	}
}
