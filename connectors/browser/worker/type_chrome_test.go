// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// typePage — поле тегов, как «Ключевые слова» на Хабре (тег — по запятой или
// Enter в keydown, а не по изменению value), список с поиском (подсказки по
// вводу, Enter выбирает подсвеченный вариант), длинная страница с красным
// блоком внизу, скрытая и видимая формы (с паролем).
const typePage = `<!doctype html><meta charset=utf-8><title>Теги</title>
<style>body{margin:0;background:#fff} #bottom{background:#f00;width:200px;height:100px}</style>
<div class=tags><span id=chips></span><input id=kw placeholder="Ключевые слова"></div>
<div class=hubs><input id=hub role=combobox aria-expanded=false placeholder="Хабы"><ul id=list role=listbox hidden></ul><p id=picked></p></div>
<form id=search style="display:none"><input name=q></form>
<form id=login><input type=password id=pw value="secret-123" style="width:200px;height:40px;background:#fff;color:#fff;border:0"><button type=button>Войти</button></form>
<div style="height:3000px"></div>
<div id=bottom>низ</div>
<script>
window.ev = {keydown: 0, keypress: 0, input: 0, keyup: 0};
const kw = document.getElementById('kw');
for (const t of Object.keys(window.ev)) kw.addEventListener(t, () => window.ev[t]++);
kw.addEventListener('keydown', e => {
  if (e.key !== ',' && e.key !== 'Enter') return;
  e.preventDefault();
  const v = kw.value.trim();
  if (v) { const s = document.createElement('span'); s.className = 'chip'; s.textContent = v; document.getElementById('chips').appendChild(s); }
  kw.value = '';
});
const hub = document.getElementById('hub'), list = document.getElementById('list');
const hubs = ['Openshift', 'Базы данных', 'Сжатие данных', 'Алгоритмы'];
hub.addEventListener('input', () => setTimeout(() => {
  const q = hub.value.toLowerCase();
  list.innerHTML = '';
  for (const h of hubs.filter(h => q && h.toLowerCase().includes(q))) {
    const li = document.createElement('li'); li.setAttribute('role', 'option'); li.textContent = h; list.appendChild(li);
  }
  if (list.firstChild) list.firstChild.setAttribute('aria-selected', 'true');
  list.hidden = !list.firstChild;
  hub.setAttribute('aria-expanded', String(!list.hidden));
}, 100));
hub.addEventListener('keydown', e => {
  if (e.key !== 'Enter') return;
  const sel = list.querySelector('[aria-selected=true]');
  if (sel) document.getElementById('picked').textContent = sel.textContent;
  hub.value = ''; list.hidden = true; hub.setAttribute('aria-expanded', 'false');
});
</script>`

// chromeEngines — headless Chrome и движки CDP и Stagehand к нему.
func chromeEngines(t *testing.T, ctx context.Context, page string) (string, map[string]Engine) {
	t.Helper()
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu --window-size=1000,700 \"$@\"\n"), 0o755)
	cdpURL, proc, err := Launch(ctx, wrapper, filepath.Join(dir, "profile"), chromeFlags(Settings{Engine: "stagehand"})...)
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	t.Cleanup(func() { StopChrome(proc, 3*time.Second) })
	cdp, err := DialCDP(ctx, cdpURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cdp.Close() })
	sh, err := DialStagehand(ctx, StagehandConfig{CDPURL: cdpURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sh.Close() })
	return srv.URL + "/", map[string]Engine{"cdp": cdp, "stagehand": sh}
}

// TestTypeAndScreenshotRealChrome — type и press в поле тегов и списке с
// поиском, снимки элемента, окна и всей страницы на обоих движках.
func TestTypeAndScreenshotRealChrome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	u, engines := chromeEngines(t, ctx, typePage)
	for name, eng := range engines {
		t.Run(name, func(t *testing.T) {
			x := &Executor{Engine: eng, Sites: domain.Sites{Domains: []string{"127.0.0.1"}}}
			if _, err := x.Open(ctx, u); err != nil {
				t.Fatal(err)
			}
			act := func(r ActRequest) ActResponse {
				t.Helper()
				if err := CheckAct(r); err != nil {
					t.Fatal(err)
				}
				res, err := x.Act(ctx, r)
				if err != nil {
					t.Fatal(r, err)
				}
				return res
			}
			chips := func() string {
				var s string
				eng.Eval(ctx, "[...document.querySelectorAll('.chip')].map(c => c.textContent).join('|')", &s)
				return s
			}
			// Три тега одним вызовом: запятые — настоящие нажатия, последний — по Enter.
			res := act(ActRequest{Selector: "#kw", Method: ActType, Value: "a, b, c", Submit: "Enter"})
			if chips() != "a|b|c" {
				t.Fatal("chips:", chips())
			}
			if res.Changes == nil || len(res.Changes.Added) != 3 || res.Changes.Added[0].Text != "a" || res.Changes.Added[2].Text != "c" {
				t.Fatalf("changes: %+v", res.Changes)
			}
			if res.Value == nil || res.Value.Length != 0 {
				t.Fatalf("value: %+v", res.Value)
			}
			var ev map[string]int
			eng.Eval(ctx, "window.ev", &ev)
			if ev["keydown"] != 8 || ev["keyup"] != 8 || ev["input"] < 4 || ev["keypress"] < 4 {
				t.Fatal("key events:", ev)
			}
			// press запятой: тег из того, что уже в поле.
			act(ActRequest{Selector: "#kw", Method: ActType, Value: "сжатие"})
			res = act(ActRequest{Selector: "#kw", Method: ActPress, Value: ","})
			if chips() != "a|b|c|сжатие" || res.Changes == nil || len(res.Changes.Added) != 1 || res.Changes.Added[0].Text != "сжатие" {
				t.Fatalf("press comma: %s %+v", chips(), res.Changes)
			}
			// Список с поиском: ввести, проверить первый вариант, выбрать, проверить.
			res = act(ActRequest{Selector: "#hub", Method: ActType, Value: "данн", ExpectSelector: "[role=option]", ExpectText: "Базы данных"})
			if res.Value == nil || res.Value.Start != "данн" || res.Changes == nil || len(res.Changes.Opened) != 1 || res.Changes.Opened[0].Role != "listbox" ||
				!strings.HasPrefix(res.Changes.Opened[0].Text, "Базы данных") || len(res.Changes.Expanded) != 1 {
				t.Fatalf("search: %+v %+v", res.Value, res.Changes)
			}
			first := act(ActRequest{Selector: "[role=option][aria-selected=true]", Method: ActWait})
			if first.Text != "Базы данных" {
				t.Fatal("first option:", first.Text)
			}
			res = act(ActRequest{Selector: "#hub", Method: ActPress, Value: "Enter", ExpectSelector: "#picked", ExpectText: "Базы данных"})
			if res.Changes == nil || len(res.Changes.Closed) != 1 || res.Changes.Closed[0].Role != "listbox" || len(res.Changes.Collapsed) != 1 {
				t.Fatalf("pick: %+v", res.Changes)
			}

			// Снимки: окно, вся страница, элемент внизу длинной страницы.
			view, err := x.Capture(ctx, ShotRequest{})
			if err != nil || view.Mode != ShotViewport || view.Width == 0 || view.Height == 0 || view.Height >= 3000 {
				t.Fatal("viewport:", view.Width, view.Height, err)
			}
			full, err := x.Capture(ctx, ShotRequest{FullPage: true})
			if err != nil || full.Mode != ShotFullPage || full.Height < 3000 {
				t.Fatal("full page:", full.Width, full.Height, err)
			}
			bottom, err := x.Capture(ctx, ShotRequest{Selector: "#bottom"})
			if err != nil || bottom.Width != 200 || bottom.Height != 100 || redShare(t, bottom.PNG) < 0.8 {
				t.Fatal("element at the bottom:", bottom.Width, bottom.Height, err)
			}
			// form: первая форма скрыта — снимается видимая.
			form, err := x.Capture(ctx, ShotRequest{Selector: "form"})
			if err != nil || form.Width == 0 {
				t.Fatal("form:", err)
			}
			// Поле пароля закрашено и без введённого секрета.
			pw, err := x.Capture(ctx, ShotRequest{Selector: "#pw"})
			if err != nil {
				t.Fatal(err)
			}
			if light := lightPixels(t, pw.PNG); light > 0 {
				t.Fatalf("the password field is not covered: %d light pixels", light)
			}
			if _, err := x.Capture(ctx, ShotRequest{Selector: "#search"}); !errors.Is(err, ErrNotFound) ||
				!strings.Contains(err.Error(), "matches 1 element(s), none is visible: form#search: display:none") {
				t.Fatal(err)
			}
			if _, err := x.Capture(ctx, ShotRequest{Selector: "#nope"}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "matches nothing") {
				t.Fatal(err)
			}
		})
	}
}

// redShare — доля красных пикселей снимка.
func redShare(t *testing.T, b []byte) float64 {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	n, all := 0, 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			all++
			if r>>8 > 200 && g>>8 < 60 && bl>>8 < 60 {
				n++
			}
		}
	}
	return float64(n) / float64(all)
}
