// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// explorePage — кабинет как у SPA: кнопки без id, поле с подписью, данные
// подгружаются fetch при загрузке и XHR по кнопке.
const explorePage = `<!doctype html><meta charset=utf-8><title>Кабинет</title>
<nav><a href="/bills">Счета</a><a href="/profile">Профиль</a></nav>
<div class=row><button class=btn>Обновить</button><button class=btn disabled>Оплатить</button></div>
<label for=acc>Лицевой счёт</label><input id=acc>
<input name=q placeholder="Поиск">
<input type=hidden name=csrf value=x>
<div hidden><button>Скрытая</button></div>
<span role=button aria-label="Закрыть">×</span>
<p id=out></p>
<div id=policy>Полис ОМС **** 2521<span class=tip style="opacity:0;position:absolute">7755 8108 2800 2521</span><span style="font-size:0">7755</span><span style="position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)">7755 8108</span><button title="полис">Показать<span style="color:transparent">7755</span></button></div>
<div style="display:table"><div style="display:table-row"><div style="display:table-cell">Гемоглобин</div><div style="display:table-cell"></div><div style="display:table-cell">140</div></div></div>
<script>
fetch('/api/bill?id=1&ehrId=42').then(r => r.json()).then(b => { document.getElementById('out').textContent = 'Долг ' + b.debt; });
fetch(location.origin.replace('127.0.0.1', 'localhost') + '/metrics').catch(() => {});
document.querySelector('.btn').addEventListener('click', () => {
  // как HttpClient в Angular: ответ берётся как json, responseText недоступен
  const x = new XMLHttpRequest(); x.open('POST', '/api/refresh'); x.responseType = 'json'; x.send('{"account":"1"}');
  setTimeout(() => { const d = document.createElement('div'); d.setAttribute('role', 'dialog');
    d.innerHTML = '<h2>Запись</h2><p>Кабинет 12</p><button>Закрыть</button>'; document.body.appendChild(d); }, 700);
});
</script>`

// TestExploreRealChrome — observe с элементами и network в настоящем Chrome (headless).
func TestExploreRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/bill":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"debt":10,"session_id":"abc","ehrId":42}`))
		case "/api/refresh":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(explorePage))
		}
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
	defer StopChrome(proc, 3*time.Second)
	eng, err := DialCDP(ctx, cdpURL)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	x := &Executor{Engine: eng, Sites: domain.Sites{Domains: []string{"127.0.0.1"}}, RedactParams: []string{"ehrId"}}
	if _, err := x.Open(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	// Порт отладки не выдаёт автоматизацию странице.
	var webdriver bool
	if err := eng.Eval(ctx, "navigator.webdriver === true", &webdriver); err != nil || webdriver {
		t.Fatal("navigator.webdriver is true: sites see automation", err)
	}
	// Скрытое стилями (полный номер полиса в подсказке, нулевой шрифт, обрезка,
	// прозрачный цвет) агенту не видно — ни в тексте, ни в элементах, ни в outline.
	_, _, pageText, _, err := x.Observe(ctx, 0)
	if err != nil || !strings.Contains(pageText, "Полис ОМС **** 2521") || strings.Contains(pageText, "7755") {
		t.Fatalf("page text: %q %v", pageText, err)
	}
	if !strings.Contains(pageText, "Гемоглобин\t\t140") {
		t.Errorf("table cells keep empty ones: %q", pageText)
	}
	if txt, err := x.TextIn(ctx, "#policy"); err != nil || strings.Contains(txt, "7755") || !strings.Contains(txt, "**** 2521") {
		t.Fatalf("text in: %q %v", txt, err)
	}
	if nodes, err := x.Outline(ctx, ""); err != nil || strings.Contains(fmt.Sprint(nodes), "7755") {
		t.Fatalf("outline leaks hidden text: %+v %v", nodes, err)
	}
	if els, err := x.Elements(ctx, "#policy"); err != nil || len(els) != 1 || strings.Contains(els[0].Text, "7755") {
		t.Fatalf("elements: %+v %v", els, err)
	}
	if res, err := x.Act(ctx, ActRequest{Selector: "#policy button", Method: ActWait}); err != nil || strings.Contains(res.Text, "7755") {
		t.Fatalf("act text: %+v %v", res, err)
	}
	els, err := x.Elements(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Element{}
	for _, e := range els {
		got[e.Text] = e
	}
	for text, sel := range map[string]string{"Счета": "a:nth-of-type(1)", "Обновить": "div:nth-of-type(1) > button:nth-of-type(1)",
		"Лицевой счёт": "#acc", "Поиск": `[name="q"]`, "Закрыть": `[aria-label="Закрыть"]`} {
		e, ok := got[text]
		if !ok || e.Selector != sel {
			t.Errorf("%s: %+v (all: %+v)", text, e, els)
		}
	}
	if !got["Оплатить"].Disabled || got["Счета"].Href != srv.URL+"/bills" {
		t.Errorf("%+v", els)
	}
	if _, ok := got["Скрытая"]; ok || len(els) != 8 {
		t.Errorf("hidden elements are skipped: %+v", els)
	}
	if nav, err := x.Elements(ctx, "nav"); err != nil || len(nav) != 2 {
		t.Fatal("elements inside nav:", nav, err)
	}
	// Клик с ожиданием результата: диалог появляется через 0,7 с.
	if _, err := x.Act(ctx, ActRequest{Selector: got["Обновить"].Selector, Method: ActClick, ExpectSelector: "[role=dialog]", ExpectText: "кабинет"}); err != nil {
		t.Fatal(err)
	}
	if err := x.WaitFor(ctx, "[role=dialog]", "", 2000); err != nil {
		t.Fatal(err)
	}
	if txt, err := x.TextIn(ctx, "[role=dialog]"); err != nil || !strings.Contains(txt, "Кабинет 12") || strings.Contains(txt, "Счета") {
		t.Fatal("text of the dialog only:", txt, err)
	}
	nodes, err := x.Outline(ctx, "")
	if err != nil || !slices.ContainsFunc(nodes, func(n Node) bool {
		return n.Selector == `[role="dialog"]` && strings.Contains(n.Text, "Кабинет 12")
	}) {
		t.Fatalf("outline: %+v %v", nodes, err)
	}
	// Клик без эффекта — ошибка, а не ok.
	if _, err := x.Act(ctx, ActRequest{Selector: "a", Text: "Профиль", Method: ActWait, ExpectSelector: "#nothing", ExpectMS: 300}); !errors.Is(err, ErrNoEffect) {
		t.Fatal("no effect:", err)
	}
	var reqs []Request
	for i := 0; i < 30; i++ {
		if reqs, err = x.Network(ctx, "/api/", false); err != nil {
			t.Fatal(err)
		}
		if len(reqs) == 2 && reqs[1].Body != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// По CDP Network: у запроса при загрузке есть метод и тело; ehrId скрыт в адресе и ответе.
	if len(reqs) != 2 || !strings.HasSuffix(reqs[0].URL, "/api/bill?id=1&ehrId=***") || reqs[0].Method != "GET" ||
		reqs[0].Body != `{"debt":10,"ehrId":"***","session_id":"***"}` || reqs[1].Method != "POST" || reqs[1].Kind != "xhr" ||
		reqs[1].Body != `{"ok":true}` || !strings.HasPrefix(reqs[1].MIME, "application/json") {
		t.Fatalf("%+v", reqs)
	}
	// Чужие сайты — только по запросу.
	if all, _ := x.Network(ctx, "/metrics", false); len(all) != 0 {
		t.Fatal("third-party requests by default:", all)
	}
	if all, _ := x.Network(ctx, "/metrics", true); len(all) != 1 {
		t.Fatal("all sites:", all)
	}
	// Страница не тронута перехватом.
	var hooked bool
	if eng.Eval(ctx, "!!window.__gate4aiNet", &hooked); hooked {
		t.Fatal("the page is patched: sites can see it")
	}
	// После перехода — запросы новой страницы.
	if _, err := x.Open(ctx, srv.URL+"/bills"); err != nil {
		t.Fatal(err)
	}
	reqs, _ = x.Network(ctx, "/api/", false)
	if len(reqs) != 1 || !strings.Contains(reqs[0].URL, "/api/bill") {
		t.Fatalf("%+v", reqs)
	}
}

func TestRedactBody(t *testing.T) {
	for _, c := range []struct{ url, body, want string }{
		{"https://a/api/bill", `{"debt":10,"user":{"accessToken":"x","name":"Ann"},"cards":[{"card_number":"4111"}]}`,
			`{"cards":[{"card_number":"***"}],"debt":10,"user":{"accessToken":"***","name":"Ann"}}`},
		{"https://a/api/x", `{"shipping":"1","pinned":true,"pin":"1234"}`, `{"pin":"***","pinned":true,"shipping":"1"}`},
		{"https://a/oauth/token", `{"x":1}`, "[hidden: response of a sign-in or token request]"},
		{"https://a/api/x", `{"refresh_token":"abc","items":[1,2`, `{"refresh_token":"***","items":[1,2`},
		{"https://a/api/x", `plain text`, `plain text`},
	} {
		if got := RedactBody(c.url, c.body); got != c.want {
			t.Errorf("%s %s: %s", c.url, c.body, got)
		}
	}
}
