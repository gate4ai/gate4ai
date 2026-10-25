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

// multiSelectPage — самописный multi-select, как «Хабы» на Хабре: варианты —
// div без роли и обработчиков (их ловит делегированный mousedown меню, видно
// только cursor: pointer), выбранные значения — «чипсы» с крестиком-svg
// (обработчик click на документе); у второго вида чипсов cursor: pointer у
// самой чипсы, а крестик — иконка внутри неё. «Аудитория» — список с
// обработчиком click на каждом варианте.
const multiSelectPage = `<!doctype html><meta charset=utf-8><title>Публикация</title>
<style>.opt, .x { cursor: pointer } .chip2 { cursor: pointer; padding: 2px } .chip, .chip2 { display: inline-block; margin: 2px }</style>
<h3>Хабы</h3>
<div id=chips><span class=chip data-v="Python">Python<svg class=x width=12 height=12 viewBox="0 0 12 12"><path d="M1 1L11 11M11 1L1 11" stroke=black /></svg></span></div>
<div id=chips2><span class=chip2 data-v="Rust">Rust <svg width=12 height=12 viewBox="0 0 12 12"><path d="M1 1L11 11M11 1L1 11" stroke=black /></svg></span></div>
<div class=menu><div class=opt>Go</div><div class=opt>Python</div><div class=opt>Rust</div></div>
<h3>Аудитория</h3>
<ul class=aud><li><span>Новички</span></li><li><span>Профессионалы</span></li></ul>
<p id=out></p>
<script>
const chip = v => { const s = document.createElement('span'); s.className = 'chip'; s.dataset.v = v;
  s.innerHTML = v + '<svg class=x width=12 height=12 viewBox="0 0 12 12"><path d="M1 1L11 11M11 1L1 11" stroke=black /></svg>';
  document.getElementById('chips').appendChild(s); };
document.querySelector('.menu').addEventListener('mousedown', e => { const o = e.target.closest('.opt'); if (o) chip(o.textContent); });
document.addEventListener('click', e => { const x = e.target.closest('.x'); if (x) x.closest('.chip').remove(); });
document.querySelector('.chip2').addEventListener('click', e => { if (e.target.closest('svg')) e.currentTarget.remove(); });
for (const li of document.querySelectorAll('.aud li')) li.addEventListener('click', () => { document.getElementById('out').textContent = 'Аудитория: ' + li.textContent; });
</script>`

// TestMultiSelectRealChrome — критерий задачи: observe --elements видит
// варианты самописного списка и крестики выбранных значений; act --text
// находит самый внутренний элемент при любом селекторе; не найдя — называет
// кандидатов и их селекторы.
func TestMultiSelectRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(multiSelectPage))
	}))
	defer srv.Close()
	sites := domain.Sites{Domains: []string{"127.0.0.1"}}
	for _, engine := range []string{"cdp"} {
		t.Run(engine, func(t *testing.T) {
			dir := t.TempDir()
			wrapper := filepath.Join(dir, "chrome")
			os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			svc := New()
			defer svc.Close()
			s, err := svc.Acquire(ctx, Settings{ChromePath: wrapper, ProfileDir: filepath.Join(dir, "profile")}, 0)
			if err != nil {
				t.Skip("chrome did not start:", err)
			}
			defer s.Release()
			if _, err := s.Open(ctx, srv.URL+"/", sites); err != nil {
				t.Fatal(err)
			}
			els, err := s.Elements(ctx, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			find := func(pred func(Element) bool) *Element {
				for i := range els {
					if pred(els[i]) {
						return &els[i]
					}
				}
				return nil
			}
			for _, v := range []string{"Go", "Python", "Rust"} {
				if find(func(e Element) bool { return e.Tag == "div" && e.Text == v }) == nil {
					t.Errorf("option %s is not in elements: %+v", v, els)
				}
			}
			for _, v := range []string{"Новички", "Профессионалы"} {
				if find(func(e Element) bool { return e.Tag == "li" && e.Text == v }) == nil {
					t.Errorf("audience %s (a click handler) is not in elements: %+v", v, els)
				}
			}
			cross := find(func(e Element) bool { return e.Tag == "svg" && e.Text == "Python" })
			icon := find(func(e Element) bool { return e.Tag == "svg" && e.Icon && strings.HasPrefix(e.Text, "Rust") })
			if cross == nil || icon == nil {
				t.Fatalf("the remove crosses of the chosen values: %+v", els)
			}
			// Вариант — по тексту при любом селекторе (div, body *): самый внутренний.
			if _, err := s.Act(ctx, ActRequest{Selector: "body *", Text: "Go", Method: ActClick}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Act(ctx, ActRequest{Selector: "ul.aud", Text: "Профессионалы", Method: ActClick, ExpectText: "Аудитория: Профессионалы"}); err != nil {
				t.Fatal("a container selector with the text of an inner element:", err)
			}
			// Крестик выбранного значения убирает его.
			if _, err := s.Act(ctx, ActRequest{Selector: cross.Selector, Method: ActClick}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Act(ctx, ActRequest{Selector: icon.Selector, Method: ActClick}); err != nil {
				t.Fatal(err)
			}
			var chips string
			if err := s.Engine.Eval(ctx, `[...document.querySelectorAll('.chip, .chip2')].map(c => c.dataset.v).join(',')`, &chips); err != nil || chips != "Go" {
				t.Fatalf("chips after the clicks: %q %v", chips, err)
			}
			// Не нашёл — кандидаты с этим текстом и их селекторы.
			_, err = s.Act(ctx, ActRequest{Selector: "li.option", Text: "Rust", Method: ActClick, TimeoutMS: 300})
			if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), `visible elements with this text: div "Rust" — selector`) {
				t.Fatal("candidates:", err)
			}
		})
	}
}
