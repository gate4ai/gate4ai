// SPDX-License-Identifier: AGPL-3.0-only

// Package testsite — тестовый сайт gate4ai: страницы, на которых сквозные тесты
// и коннектор e2e/test-connector проверяют Браузер и функции gate4ai. Поднимается
// локально (httptest в тестах, cmd/testsite — руками, dev/scripts/stand.sh);
// состояние — в памяти. Персональных данных нет; учётка фиксированная и
// напечатана на странице входа — это не секрет.
//
// Страницы:
//
//	/                — заголовок и таблица показаний (действие read коннектора)
//	/login           — форма входа (#login, #password, #bind); POST ставит сессию
//	/cabinet         — без сессии уводит на /login; кнопка #press — POST /cabinet/action,
//	                   результат («Заявка №N принята») виден на /cabinet (по нему — verify)
//	/cabinet/action  — POST: принять заявку
//	/reset           — сброс сессий и счётчика заявок (для тестов)
//	/api/items       — POST: принимает JSON (browser.post в TestBrowserAgentProfile)
//	/tags            — поле тегов, поле пароля, длинная страница (TestBrowserTypeAndScreenshot)
//	/publish, /other — страница с beforeunload и соседняя (TestBrowserAgentProfile)
//	/live            — кнопка и поле для живого просмотра (TestBrowserLiveView)
package testsite

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Тестовая учётка. Значения фейковые, напечатаны на /login.
const (
	User     = "user-e2e"
	Password = "fake-pass-e2e-4242"
)

// Row — строка таблицы на главной.
type Row struct{ Meter, Value string }

// Rows — данные таблицы на главной; их читает действие read коннектора.
var Rows = []Row{{"Электричество", "1 234"}, {"Холодная вода", "56"}, {"Горячая вода", "38"}}

// Site — состояние сайта.
type Site struct {
	// LoginURL — origin страницы входа, если он отличается от сайта (тест на
	// два origin: вход на localhost, кабинет на 127.0.0.1). "" — тот же сайт.
	LoginURL string
	// CabinetURL — origin кабинета для возврата после входа с другого origin.
	CabinetURL string

	mu      sync.Mutex
	gen     int             // поколение сессий: reset выходит из всех
	tickets map[string]bool // одноразовые билеты после входа (возврат на другой origin)
	presses int
}

// New — сайт с пустым состоянием.
func New() *Site { return &Site{gen: 1, tickets: map[string]bool{}} }

// Handler — все маршруты.
func (s *Site) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("GET /cabinet", s.cabinet)
	mux.HandleFunc("POST /cabinet/action", s.action)
	mux.HandleFunc("/reset", s.reset)
	mux.HandleFunc("GET /tags", page("Теги", tagsBody))
	mux.HandleFunc("GET /publish", page("Publish", publishBody))
	mux.HandleFunc("GET /other", page("Other", "<p>Other page</p>"))
	mux.HandleFunc("GET /live", page("Live", liveBody))
	mux.HandleFunc("POST /api/items", func(w http.ResponseWriter, r *http.Request) { // запрос с эффектом (browser.post)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
	})
	return mux
}

// Presses — сколько заявок принято.
func (s *Site) Presses() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.presses
}

// Reset выходит из всех сессий и обнуляет заявки.
func (s *Site) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.presses = 0
	s.tickets = map[string]bool{}
}

func (s *Site) reset(w http.ResponseWriter, r *http.Request) {
	s.Reset()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "reset\n")
}

func write(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><html lang=ru><meta charset=utf-8><meta name=robots content=noindex><title>%s</title>%s", html.EscapeString(title), body)
}

func page(title, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { write(w, title, body) }
}

func (s *Site) home(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("<h1>Тестовый сайт gate4ai</h1><p>Сайт для проверки Браузера и коннекторов gate4ai. Данные вымышленные.</p>")
	b.WriteString("<table id=readings><thead><tr><th>Счётчик</th><th>Показание</th></tr></thead><tbody>")
	for _, row := range Rows {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td></tr>", html.EscapeString(row.Meter), html.EscapeString(row.Value))
	}
	b.WriteString("</tbody></table><p><a href=/cabinet>Кабинет</a> · <a href=/login>Вход</a></p>")
	write(w, "Показания", b.String())
}

func (s *Site) loginForm(w http.ResponseWriter, r *http.Request) {
	s.loginPage(w, r.URL.Query().Get("back"), "")
}

func (s *Site) loginPage(w http.ResponseWriter, back, errText string) {
	if back == "" || !strings.HasPrefix(back, "/") {
		back = "/cabinet"
	}
	if errText != "" {
		errText = "<p id=error>" + html.EscapeString(errText) + "</p>"
	}
	write(w, "Вход", fmt.Sprintf(`<h1>Вход</h1>%s<form method=post action=/login>
<input type=hidden name=back value="%s">
<label>Логин <input id=login name=login autocomplete=username></label>
<label>Пароль <input id=password name=password type=password autocomplete=current-password></label>
<button id=bind>Войти</button></form>
<p class=hint>Тестовая учётка: логин <code>%s</code>, пароль <code>%s</code>.</p>`, errText, html.EscapeString(back), User, Password))
}

func (s *Site) loginPost(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	back := r.Form.Get("back")
	if r.Form.Get("login") != User || r.Form.Get("password") != Password {
		w.WriteHeader(http.StatusUnauthorized)
		s.loginPage(w, back, "Неверный логин или пароль")
		return
	}
	if back == "" || !strings.HasPrefix(back, "/") {
		back = "/cabinet"
	}
	s.mu.Lock()
	ticket := "t" + strconv.Itoa(len(s.tickets)+1) + "-" + strconv.Itoa(s.gen)
	s.tickets[ticket] = true
	s.mu.Unlock()
	http.Redirect(w, r, s.CabinetURL+back+"?t="+ticket, http.StatusFound)
}

// session — вошёл ли посетитель: cookie текущего поколения или одноразовый билет
// (тогда cookie ставится).
func (s *Site) session(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cookie := strconv.Itoa(s.gen)
	if t := r.URL.Query().Get("t"); t != "" && s.tickets[t] {
		delete(s.tickets, t)
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: cookie, Path: "/", HttpOnly: true})
		return true
	}
	c, err := r.Cookie("sid")
	return err == nil && c.Value == cookie
}

func (s *Site) toLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.LoginURL+"/login?back="+r.URL.Path, http.StatusFound)
}

func (s *Site) cabinet(w http.ResponseWriter, r *http.Request) {
	if !s.session(w, r) {
		s.toLogin(w, r)
		return
	}
	s.mu.Lock()
	n := s.presses
	s.mu.Unlock()
	result := "<p id=result>Заявок пока нет</p>"
	if n > 0 {
		result = fmt.Sprintf("<p id=result>Заявка №%d принята</p>", n)
	}
	write(w, "Кабинет", fmt.Sprintf(`<h1>Кабинет</h1><p>Кабинет: долг 1 234,56 ₽</p>
<form method=post action=/cabinet/action><button id=press>Отправить заявку</button></form>%s`, result))
}

func (s *Site) action(w http.ResponseWriter, r *http.Request) {
	if !s.session(w, r) {
		s.toLogin(w, r)
		return
	}
	s.mu.Lock()
	s.presses++
	s.mu.Unlock()
	http.Redirect(w, r, "/cabinet", http.StatusSeeOther)
}

// tagsBody — поле тегов (тег — по запятой или Enter в keydown, как «Ключевые
// слова» на Хабре), поле пароля и длинная страница.
const tagsBody = `<style>body{margin:0;background:#fff} .chip{display:block}</style>
<div id=chips></div><input id=kw placeholder="Ключевые слова">
<form id=search style="display:none"><input name=q></form>
<form id=login><input type=password id=pw value="pass-e2e-777" style="width:200px;height:40px;padding:0;background:#fff;color:#fff;border:0"><button type=button id=go>Отправить</button></form>
<div style="height:3000px"></div><p>низ</p>
<script>
const kw = document.getElementById('kw');
kw.addEventListener('keydown', e => {
  if (e.key !== ',' && e.key !== 'Enter') return;
  e.preventDefault();
  const v = kw.value.trim();
  if (v) { const s = document.createElement('span'); s.className = 'chip'; s.textContent = v; document.getElementById('chips').appendChild(s); }
  kw.value = '';
});
</script>`

// publishBody — страница с несохранённым текстом: beforeunload спрашивает «Покинуть?».
const publishBody = `<textarea id=q></textarea>
<script>addEventListener('beforeunload', e => { if (document.getElementById('q').value) { e.preventDefault(); e.returnValue = 'unsaved'; } })</script>`

// liveBody — крупные кнопка и поле: клики и ввод из живого просмотра сеанса.
const liveBody = `<body style="margin:0">
<button id=b style="position:absolute;left:0;top:0;width:50vw;height:40vh" onclick="this.textContent='clicked '+(++window.n)">click me</button>
<input id=i style="position:absolute;left:0;top:60vh;width:90vw;height:10vh">
<p id=o style="position:absolute;top:80vh"></p><script>window.n=0;i.oninput=()=>o.textContent='typed '+i.value</script>`
