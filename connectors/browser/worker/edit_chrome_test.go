// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"errors"
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

// editPage — черновик статьи: заголовок, тело в textarea, редактор с
// обработчиком paste (разбирает Markdown сам), рекламный блок и пароль.
const editPage = `<!doctype html><meta charset=utf-8><title>Черновик</title>
<input name=title placeholder="Заголовок">
<textarea name=text></textarea>
<div id=editor contenteditable=true data-testid=editor></div>
<input type=password name=pass value="hunter2">
<label><input type=checkbox name=agree checked> Согласен</label>
<button id=ember1234 onclick="window.sent = event.ctrlKey">Отправить</button>
<div class="adfox-banner"><a href="https://ads.adfox.ru/c?x=1">реклама</a><button>Подробнее</button></div>
<a href="https://example.org/?yclid=777">трекинг</a>
<a href="/next">дальше</a>
<p id=keys></p>
<script>
document.getElementById('editor').addEventListener('paste', e => {
  e.preventDefault();
  const t = e.clipboardData.getData('text/plain');
  document.getElementById('editor').innerText = 'parsed:' + t.replace(/^# /, '');
});
document.querySelector('[name=text]').addEventListener('keydown', e => {
  if (e.key === 'Enter' && e.ctrlKey) document.getElementById('keys').textContent = 'ctrl+enter';
});
</script>`

func chromeExecutor(t *testing.T, page string) (*Executor, *CDP, string, context.Context) {
	t.Helper()
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/next" {
			w.Write([]byte("<!doctype html><title>next</title><p class=done>готово</p>"))
			return
		}
		w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	cdpURL, proc, err := Launch(ctx, wrapper, filepath.Join(dir, "profile"))
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	t.Cleanup(func() { StopChrome(proc, 3*time.Second) })
	eng, err := DialCDP(ctx, cdpURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	x := &Executor{Engine: eng, Sites: domain.Sites{Domains: []string{"127.0.0.1"}}}
	if _, err := x.Open(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	return x, eng, srv.URL, ctx
}

// TestEditRealChrome — длинный текст одной командой, append, paste, сочетания
// клавиш и что записалось в поле (ответ act и observe).
func TestEditRealChrome(t *testing.T) {
	x, eng, base, ctx := chromeExecutor(t, editPage)
	act := func(r ActRequest) ActResponse {
		t.Helper()
		if err := CheckAct(r); err != nil {
			t.Fatal(r.Method, err)
		}
		res, err := x.Act(ctx, r)
		if err != nil {
			t.Fatal(r.Method, err)
		}
		return res
	}
	// Тело статьи ~24 КБ (кириллица и Markdown) — одной командой.
	body := "# Заголовок\n\n" + strings.Repeat("Абзац статьи с **жирным** и `кодом`.\n", 450) + "Конец."
	if len(body) < 24<<10 {
		t.Fatal(len(body))
	}
	start := time.Now()
	res := act(ActRequest{Selector: "textarea", Method: ActFill, Value: body})
	if time.Since(start) > 10*time.Second {
		t.Errorf("fill of %d bytes took %s", len(body), time.Since(start))
	}
	n := len([]rune(body))
	if res.Value == nil || res.Value.Length != n || !strings.HasPrefix(body, res.Value.Start) || len([]rune(res.Value.Start)) != ValueStart ||
		res.Value.End != string([]rune(body)[n-ValueEnd:]) {
		t.Fatalf("fill value: %+v", res.Value)
	}
	var got string
	eng.Eval(ctx, "document.querySelector('textarea').value", &got)
	if got != body {
		t.Fatalf("textarea has %d characters, want %d", len([]rune(got)), n)
	}
	// append дописывает, fill заменяет.
	act(ActRequest{Selector: "[name=title]", Method: ActFill, Value: "Тест"})
	if res := act(ActRequest{Selector: "[name=title]", Method: ActAppend, Value: "овый заголовок"}); res.Value == nil || res.Value.Start != "Тестовый заголовок" || res.Value.End != "" {
		t.Fatalf("append: %+v", res.Value)
	}
	// Ctrl+A и Backspace стирают, Ctrl+Enter доходит до страницы.
	act(ActRequest{Selector: "[name=title]", Method: ActPress, Value: "ctrl+a"})
	act(ActRequest{Selector: "[name=title]", Method: ActPress, Value: "Backspace"})
	eng.Eval(ctx, "document.querySelector('[name=title]').value", &got)
	if got != "" {
		t.Fatalf("after Ctrl+A, Backspace: %q", got)
	}
	act(ActRequest{Selector: "textarea", Method: ActPress, Value: "Ctrl+Enter"})
	if res := act(ActRequest{Selector: "#keys", Method: ActWait}); res.Text != "ctrl+enter" {
		t.Fatalf("Ctrl+Enter: %+v", res)
	}
	// paste: редактор получает событие paste с текстом и разбирает его сам.
	if res := act(ActRequest{Selector: "[data-testid=editor]", Method: ActPaste, Value: "# Привет"}); res.Value == nil || res.Value.Start != "parsed:Привет" {
		t.Fatalf("paste: %+v", res.Value)
	}
	// paste в textarea без обработчика — обычная вставка вместо значения.
	if res := act(ActRequest{Selector: "textarea", Method: ActPaste, Value: "коротко"}); res.Value == nil || res.Value.Start != "коротко" || res.Value.Length != 7 {
		t.Fatalf("paste into textarea: %+v", res.Value)
	}
	// Пароль: значение не отдаётся ни act, ни observe.
	if res := act(ActRequest{Selector: "[name=pass]", Method: ActAppend, Value: "x"}); res.Value == nil || !res.Value.Hidden || res.Value.Start != "" {
		t.Fatalf("password value: %+v", res.Value)
	}
	els, err := x.Elements(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Element{}
	for _, e := range els {
		by[e.Selector] = e
	}
	if e := by[`[name="text"]`]; e.Kind != "field" || e.Value != "коротко" || e.ValueLength != 7 {
		t.Errorf("textarea in observe: %+v", e)
	}
	if e := by[`[name="pass"]`]; e.Kind != "field" || e.Value != "" || e.ValueLength != 0 {
		t.Errorf("password in observe: %+v", e)
	}
	if e := by[`[name="agree"]`]; !e.Checked {
		t.Errorf("checkbox: %+v", e)
	}
	if e := by[`[data-testid="editor"]`]; e.Value != "parsed:Привет" {
		t.Errorf("editor: %+v (all %+v)", e, els)
	}
	for _, e := range els {
		if strings.Contains(e.Href, "adfox") || strings.Contains(e.Href, "yclid") || e.Text == "Подробнее" {
			t.Errorf("ad or tracker element shown: %+v", e)
		}
		if strings.HasPrefix(e.Selector, "#ember") {
			t.Errorf("generated id preferred: %+v", e)
		}
	}
	if _, ok := by[`[href="/next"]`]; !ok {
		t.Errorf("plain link is missing: %+v", els)
	}
	fields, err := x.Elements(ctx, "", []string{"fields"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range fields {
		if e.Kind != "field" {
			t.Errorf("kinds fields: %+v", e)
		}
	}
	if len(fields) != 5 {
		t.Errorf("fields: %+v", fields)
	}
	if _, err := x.Elements(ctx, "", []string{"images"}); err == nil {
		t.Error("unknown kind accepted")
	}

	// reload — та же страница заново (значения полей сброшены).
	if u, err := x.Reload(ctx); err != nil || u != base+"/" {
		t.Fatal(u, err)
	}
	eng.Eval(ctx, "document.querySelector('textarea').value", &got)
	if got != "" {
		t.Fatalf("after reload: %q", got)
	}

	// handoff: «человек» переходит по ссылке через секунду.
	HandoffPoll = 100 * time.Millisecond
	var ok bool
	eng.Eval(ctx, "setTimeout(() => location.href = '/next', 700), true", &ok)
	hr, err := x.Handoff(ctx, HandoffRequest{UntilSelector: ".done", Timeout: 10 * time.Second})
	if err != nil || hr.Status != "done" || hr.URL != base+"/next" {
		t.Fatal(hr, err)
	}
	if hr, err := x.Handoff(ctx, HandoffRequest{Timeout: 300 * time.Millisecond}); err != nil || hr.Status != "timeout" {
		t.Fatal(hr, err)
	}
	if _, err := x.Handoff(ctx, HandoffRequest{Timeout: time.Hour}); err == nil {
		t.Fatal("handoff over the limit accepted")
	}
	if err := CheckAct(ActRequest{Selector: "textarea", Method: ActPress, Value: "Ctrl+V"}); !errors.Is(err, ErrClipboard) {
		t.Fatal(err)
	}
}
