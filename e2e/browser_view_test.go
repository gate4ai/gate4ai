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
	actx, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome))...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	frame := `(() => { const c = document.querySelector('[data-browser-canvas]'); return !!c && c.width > 100 })()`
	// canvasPoint — точка на canvas по долям его размера (координаты окна).
	click := func(fx, fy float64) chromedp.Action {
		return chromedp.ActionFunc(func(ctx context.Context) error {
			var p []float64
			if err := chromedp.Evaluate(fmt.Sprintf(`(() => { const r = document.querySelector('[data-browser-canvas]').getBoundingClientRect(); return [r.left + r.width*%v, r.top + r.height*%v] })()`, fx, fy), &p).Do(ctx); err != nil {
				return err
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
		chromedp.Sleep(500*time.Millisecond), // ws просмотрщика после перезагрузки страницы
		click(0.1, 0.1),
		click(0.3, 0.65),
		chromedp.KeyEvent("hi"),
		chromedp.Sleep(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if r := g.run(tok, "", "browser", "observe", "--session", sid); r.code == 0 || !strings.Contains(r.out, "session_held_by_user") {
		t.Fatalf("the agent waits while the owner holds the session: %d %s", r.code, r.out)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`//button[text()="Give back to the agent"]`), chromedp.WaitVisible(`//button[text()="Take control"]`)); err != nil {
		t.Fatal(err)
	}
	r = g.run(tok, "", "browser", "observe", "--session", sid)
	if r.code != 0 || !strings.Contains(r.out, "clicked 1") || !strings.Contains(r.out, "typed hi") {
		t.Fatalf("the owner's click and typing reached the page: %d %s", r.code, r.out)
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
