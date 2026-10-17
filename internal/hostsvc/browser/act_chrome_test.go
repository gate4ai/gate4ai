// SPDX-License-Identifier: AGPL-3.0-only

package browser

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

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/domain"
	"github.com/gate4ai/sdk-go/hostfuncs"
)

// actPage — форма как у SPA: поле с маской (значение пишет обработчик input),
// кнопки без id, результат появляется не сразу.
const actPage = `<!doctype html><meta charset=utf-8><title>Перевод</title>
<label for=phone>Номер телефона (счёт)</label><input id=phone>
<input name=sum placeholder="Сумма перевода, ₽">
<div hidden><button>Перевести</button></div>
<div class=card><button class=btn>Отмена</button><button class=btn>Перевести</button></div>
<p id=out></p>
<a id=away href="https://evil.example/">уйти</a>
<script>
const phone = document.getElementById('phone');
phone.addEventListener('input', () => { const d = phone.value.replace(/\D/g, ''); phone.value = d ? '+' + d : ''; });
document.querySelectorAll('.btn')[1].addEventListener('click', () => setTimeout(() => {
  document.getElementById('out').textContent = 'Перевод выполнен: ' + phone.value + ' ' + document.querySelector('[name=sum]').value + ' ' + (window.entered || '');
}, 300));
document.querySelector('[name=sum]').addEventListener('keydown', e => { if (e.key === 'Enter') window.entered = 'enter'; });
</script>`

// TestActRealChrome — browser_act в настоящем Chrome (headless) по CDP.
func TestActRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(actPage))
	}))
	defer srv.Close()
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cdpURL, proc, err := Launch(ctx, wrapper, filepath.Join(dir, "profile"))
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	defer proc.Kill()
	eng, err := DialCDP(ctx, cdpURL)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	x := &Executor{Engine: eng, Sites: domain.Sites{Domains: []string{"127.0.0.1"}}}
	if _, err := x.Open(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	act := func(r abi.BrowserRequest) abi.BrowserActResponse {
		t.Helper()
		res, err := x.Act(ctx, r)
		if err != nil {
			t.Fatal(r, err)
		}
		return res
	}
	act(abi.BrowserRequest{Selector: "input", Text: "номер телефона (счет)", Method: abi.ActFill, Value: "79001234567"})
	act(abi.BrowserRequest{Selector: "input", Text: "сумма", Method: abi.ActFill, Value: "10"})
	act(abi.BrowserRequest{Selector: "input", Text: "Сумма", Method: abi.ActPress, Value: "Enter"})
	// скрытая кнопка пропускается, «Отмена» не подходит по тексту, контейнер .card — длиннее кнопки
	act(abi.BrowserRequest{Selector: "button, div", Text: "перевести", Method: abi.ActClick})
	res := act(abi.BrowserRequest{Selector: "#out", Text: "выполнен", Method: abi.ActWait, TimeoutMS: 3000})
	if res.Text != "Перевод выполнен: +79001234567 10 enter" || !strings.HasPrefix(res.URL, srv.URL) {
		t.Fatalf("%+v", res)
	}
	// повторный fill заменяет значение, а не дописывает
	act(abi.BrowserRequest{Selector: "[name=sum]", Method: abi.ActFill, Value: "25"})
	var v string
	eng.Eval(ctx, "document.querySelector('[name=sum]').value", &v)
	if v != "25" {
		t.Fatal(v)
	}
	if _, err := x.Act(ctx, abi.BrowserRequest{Selector: "#nope", Method: abi.ActClick, TimeoutMS: 300}); !errors.Is(err, hostfuncs.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := x.Act(ctx, abi.BrowserRequest{Selector: "#away", Method: abi.ActClick}); err == nil {
		// клик увёл страницу с разрешённого домена (или переход ещё идёт) — проверим адрес
		time.Sleep(500 * time.Millisecond)
		if u, _ := eng.URL(ctx); strings.Contains(u, "evil.example") {
			if _, err := x.Act(ctx, abi.BrowserRequest{Selector: "body", Method: abi.ActWait}); err == nil {
				t.Fatal("act on a page off the allowlist must fail")
			}
		}
	}
}
