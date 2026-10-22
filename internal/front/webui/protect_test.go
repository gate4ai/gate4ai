// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() { kdf.time, kdf.memory, kdf.threads = 1, 1024, 1 } // быстрый argon2id в тестах

// fakeConfirm — Telegram-бот: запоминает запросы подтверждения входа.
type fakeConfirm struct {
	mu   sync.Mutex
	reqs []string
	done []func(bool)
}

func (f *fakeConfirm) ConfirmLogin(id, text string, done func(bool)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, text)
	f.done = append(f.done, done)
	return nil
}

func (f *fakeConfirm) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }
func (f *fakeConfirm) press(i int, ok bool) {
	f.mu.Lock()
	d := f.done[i]
	f.mu.Unlock()
	d(ok)
}

// clock — сдвигаемое время для u.Now (обработчики читают его из других горутин).
func clock(u *UI) func(time.Duration) {
	var off atomic.Int64
	u.Now = func() time.Time { return time.Now().Add(time.Duration(off.Load())) }
	return func(d time.Duration) { off.Add(int64(d)) }
}

// browser — новый браузер: открывает ссылку gate4ai ui.
type browser struct {
	t    *testing.T
	c    *http.Client
	base string
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar}, base: base}
}

func (b *browser) get(u string) (int, string) {
	b.t.Helper()
	if strings.HasPrefix(u, "/") {
		u = b.base + u
	}
	r, err := b.c.Get(u)
	if err != nil {
		b.t.Fatal(err)
	}
	return r.StatusCode, body(b.t, r)
}

func (b *browser) post(path string, v url.Values) (int, string) {
	b.t.Helper()
	r, err := b.c.PostForm(b.base+path, v)
	if err != nil {
		b.t.Fatal(err)
	}
	return r.StatusCode, body(b.t, r)
}

// loggedIn — у браузера есть сессия.
func (b *browser) loggedIn() bool {
	st, p := b.get("/")
	return st == 200 && strings.Contains(p, `action="/logout"`)
}

// submitPassword — ввод пароля на странице попытки входа.
func (b *browser) submitPassword(page, pw string) (int, string) {
	b.t.Helper()
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		b.t.Fatal("no login form", page)
	}
	return b.post("/login", url.Values{"csrf": {m[1]}, "password": {pw}})
}

func TestLoginPassword(t *testing.T) {
	u, _, _, base := setup(t)
	advance := clock(u)
	// Без защиты ссылка сразу даёт сессию.
	other := newBrowser(t, base)
	other.get(u.LoginURL())
	if !other.loggedIn() || len(u.Protection().Requires()) != 0 {
		t.Fatal("no protection: link must log in")
	}
	_, _, post, _ := loginAs(t, u, base)
	if p := post("/settings/login/password", url.Values{"password": {"short"}, "repeat": {"short"}}); !strings.Contains(p, "не короче 8") {
		t.Fatal("short password accepted", p)
	}
	if p := post("/settings/login/password", url.Values{"password": {"correct horse"}, "repeat": {"correct horsee"}}); !strings.Contains(p, "не совпадают") {
		t.Fatal("mismatch accepted")
	}
	if p := post("/settings/login/password", url.Values{"password": {"correct horse"}, "repeat": {"correct horse"}}); !strings.Contains(p, "Пароль задан") {
		t.Fatal("password not set", p)
	}
	p := u.Protection()
	if !strings.HasPrefix(p.Password, "$argon2id$") || strings.Contains(p.Password, "correct horse") {
		t.Fatal("password must be stored as argon2id hash", p.Password)
	}
	if other.loggedIn() {
		t.Fatal("other sessions must be closed when protection changes")
	}
	if _, page := (&browser{t: t, c: &http.Client{}, base: base}).get("/settings"); strings.Contains(page, "argon2id$") {
		t.Fatal("hash leaked")
	}

	// Ссылка без пароля сессию не даёт.
	b := newBrowser(t, base)
	link := u.LoginURL()
	st, page := b.get(link)
	if st != 401 || !strings.Contains(page, `name="password"`) || b.loggedIn() {
		t.Fatal("link without password gave a session", st)
	}
	// Повторное использование ссылки — не новая попытка.
	b2 := newBrowser(t, base)
	if st, page := b2.get(link); st != 401 || strings.Contains(page, `name="password"`) {
		t.Fatal("link reused")
	}
	if st, page := b2.post("/login", url.Values{"password": {"correct horse"}}); st != 401 || b2.loggedIn() || strings.Contains(page, `name="password"`) {
		t.Fatal("password without a link logged in")
	}
	// Неверный пароль, затем верный.
	_, page = b.get("/login")
	if _, page = b.submitPassword(page, "wrong password"); !strings.Contains(page, "неверный пароль") || b.loggedIn() {
		t.Fatal("wrong password accepted")
	}
	if st, _ := b.submitPassword(page, "correct horse"); st != 200 || !b.loggedIn() {
		t.Fatal("correct password rejected", st)
	}
	// Ссылка в чужом браузере не продолжает чужую попытку: попытка — в cookie.
	b3 := newBrowser(t, base)
	_, page = b3.get(u.LoginURL())
	for i := 0; i < maxTries; i++ {
		_, page = b3.submitPassword(page, "guess")
	}
	if strings.Contains(page, `name="password"`) || !strings.Contains(page, "начните вход заново") {
		t.Fatal("attempt must end after too many wrong passwords", page)
	}
	// Подряд неверные пароли по разным ссылкам — задержка для всех.
	b4 := newBrowser(t, base)
	_, page = b4.get(u.LoginURL())
	if _, page = b4.submitPassword(page, "correct horse"); !strings.Contains(page, "подождите") || b4.loggedIn() {
		t.Fatal("no delay after failures")
	}
	advance(2 * time.Second)
	if _, _ = b4.submitPassword(page, "correct horse"); !b4.loggedIn() {
		t.Fatal("login after delay")
	}
	// Попытка входа живёт 5 минут.
	b5 := newBrowser(t, base)
	_, page = b5.get(u.LoginURL())
	advance(attemptTTL + time.Second)
	if _, _ = b5.submitPassword(page, "correct horse"); b5.loggedIn() {
		t.Fatal("expired attempt accepted")
	}

	// Сменить и снять пароль — только с текущим паролем.
	_, _, post2 := protectedSession(t, u, base, "correct horse")
	if p := post2("/settings/login/password", url.Values{"remove": {"1"}, "current": {"nope"}}); !strings.Contains(p, "текущий пароль") || u.Protection().Password == "" {
		t.Fatal("removed without current password")
	}
	advance(time.Hour) // задержка после неверного пароля
	post2("/settings/login/password", url.Values{"current": {"correct horse"}, "password": {"battery staple"}, "repeat": {"battery staple"}})
	if !checkPassword(u.Protection().Password, "battery staple") {
		t.Fatal("password not changed")
	}
	post2("/settings/login/password", url.Values{"remove": {"1"}, "current": {"battery staple"}})
	if u.Protection().Custom() {
		t.Fatal("password not removed")
	}
	b6 := newBrowser(t, base)
	if b6.get(u.LoginURL()); !b6.loggedIn() {
		t.Fatal("after removal the link must log in again")
	}
}

// protectedSession — вход с паролем; post — действие формы этой сессии.
func protectedSession(t *testing.T, u *UI, base, pw string) (*browser, string, func(string, url.Values) string) {
	t.Helper()
	b := newBrowser(t, base)
	_, page := b.get(u.LoginURL())
	b.submitPassword(page, pw)
	_, page = b.get("/settings")
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("not logged in")
	}
	return b, m[1], func(path string, v url.Values) string {
		t.Helper()
		v.Set("csrf", m[1])
		_, p := b.post(path, v)
		return p
	}
}

func TestLoginTelegram(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, post, _ := loginAs(t, u, base)
	if p := post("/settings/login/telegram", url.Values{"on": {"1"}}); !strings.Contains(p, "подключите Telegram-бот") || u.Protection().Custom() {
		t.Fatal("telegram confirmation enabled without a bot")
	}
	f := &fakeConfirm{}
	u.Confirm = f
	if p := post("/settings/login/telegram", url.Values{"on": {"1"}}); !strings.Contains(p, "подтверждается в Telegram") {
		t.Fatal("not enabled", p)
	}

	b := newBrowser(t, base)
	st, page := b.get(u.LoginURL())
	code := regexp1(`id="code">(\d{6})<`, page)
	if st != 401 || code == "" || f.n() != 1 || !strings.Contains(f.reqs[0], code) || b.loggedIn() {
		t.Fatal("login must wait for Telegram", st, f.reqs)
	}
	if !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatal("waiting page must refresh itself")
	}
	f.press(0, true)
	if b.get("/login"); !b.loggedIn() { // страница ожидания обновляется сама
		t.Fatal("confirmed login has no session")
	}
	// Отклонено в Telegram — сессии нет и не будет.
	b2 := newBrowser(t, base)
	b2.get(u.LoginURL())
	f.press(1, false)
	if _, page := b2.get("/login"); !strings.Contains(page, "отклонён") || b2.loggedIn() {
		t.Fatal("rejected login")
	}
	f.press(1, true) // повторное нажатие после отказа
	if b2.get("/login"); b2.loggedIn() {
		t.Fatal("login after rejection")
	}
	// Сессия, открытая до включения защиты, защиту не меняет.
	u.D.State.Set(protectionKey, `{"telegram":true}`)
	_, _, post3 := loginAsUnprotected(t, u, base)
	if p := post3("/settings/login/telegram", url.Values{"on": {"0"}}); !strings.Contains(p, "открыта без защиты") || !u.Protection().Telegram {
		t.Fatal("unprotected session changed protection", p)
	}
}

// loginAsUnprotected — сессия, открытая без защиты (например, до её включения).
func loginAsUnprotected(t *testing.T, u *UI, base string) (*browser, string, func(string, url.Values) string) {
	t.Helper()
	b := newBrowser(t, base)
	sid := random()
	u.mu.Lock()
	u.sessions[sid] = session{csrf: random(), expires: u.Now().Add(time.Hour)}
	csrf := u.sessions[sid].csrf
	u.mu.Unlock()
	bu, _ := url.Parse(base)
	b.c.Jar.SetCookies(bu, []*http.Cookie{{Name: "g4a_session", Value: sid, Path: "/"}})
	return b, csrf, func(path string, v url.Values) string {
		v.Set("csrf", csrf)
		_, p := b.post(path, v)
		return p
	}
}

func TestLoginPasswordAndTelegram(t *testing.T) {
	u, _, _, base := setup(t)
	f := &fakeConfirm{}
	u.Confirm = f
	_, _, post, _ := loginAs(t, u, base)
	post("/settings/login/password", url.Values{"password": {"correct horse"}, "repeat": {"correct horse"}})
	// Пароль задан: включить Telegram — только с текущим паролем.
	if p := post("/settings/login/telegram", url.Values{"on": {"1"}}); !strings.Contains(p, "текущий пароль") || u.Protection().Telegram {
		t.Fatal("telegram enabled without current password")
	}
	post("/settings/login/telegram", url.Values{"on": {"1"}, "current": {"correct horse"}})
	if r := u.Protection().Requires(); len(r) != 2 {
		t.Fatal("both factors", r)
	}

	b := newBrowser(t, base)
	_, page := b.get(u.LoginURL())
	if f.n() != 0 {
		t.Fatal("Telegram must be asked only after the password")
	}
	_, page = b.submitPassword(page, "correct horse")
	if f.n() != 1 || b.loggedIn() || !strings.Contains(page, "Подтвердите вход") {
		t.Fatal("password alone logged in", f.n())
	}
	f.press(0, true)
	if b.get("/login"); !b.loggedIn() {
		t.Fatal("password + Telegram did not log in")
	}
	// Telegram без пароля не пускает.
	b2 := newBrowser(t, base)
	b2.get(u.LoginURL())
	if f.n() != 1 || b2.loggedIn() {
		t.Fatal("no password: no Telegram request, no session")
	}
	// Восстановление: сброс снимает защиту и закрывает сессии.
	if err := u.ResetProtection(); err != nil || u.Protection().Custom() || b.loggedIn() {
		t.Fatal("reset")
	}
}

func regexp1(re, s string) string {
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestRemoteLoginProtected(t *testing.T) {
	u, d, _, _ := setup(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	d.State.Set(protectionKey, `{"password":"`+hashPassword("correct horse")+`"}`)
	l, _ := u.RemoteLoginURL()
	req, _ := http.NewRequest("GET", l, nil)
	req.Host = "dev.example"
	rec := &recorder{h: http.Header{}}
	u.RemoteHandler().ServeHTTP(rec, req)
	for _, c := range (&http.Response{Header: rec.h}).Cookies() {
		if c.Name == "g4a_rsession" {
			t.Fatal("remote link without password gave a session")
		}
	}
	if rec.code != http.StatusSeeOther || rec.h.Get("Location") != "/ui/login" {
		t.Fatal("remote link must start a login attempt", rec.code, rec.h.Get("Location"))
	}
}

// Ссылка входа живёт loginTTL: и локальная, и удалённая (через relay); свежая открывается.
func TestLoginLinkExpires(t *testing.T) {
	u, d, _, base := setup(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	advance := clock(u)
	localSession := func(l string) bool {
		b := newBrowser(t, base)
		b.get(l)
		return b.loggedIn()
	}
	remoteSession := func(l string) bool {
		req, _ := http.NewRequest("GET", l, nil)
		req.Host = "dev.example"
		rec := &recorder{h: http.Header{}}
		u.RemoteHandler().ServeHTTP(rec, req)
		for _, c := range (&http.Response{Header: rec.h}).Cookies() {
			if c.Name == "g4a_rsession" {
				return true
			}
		}
		return false
	}
	local := u.LoginURL()
	rem, err := u.RemoteLoginURL()
	if err != nil {
		t.Fatal(err)
	}
	advance(loginTTL + time.Second)
	if localSession(local) {
		t.Fatal("local link works after loginTTL")
	}
	if remoteSession(rem) {
		t.Fatal("remote link works after loginTTL")
	}
	local = u.LoginURL()
	rem, _ = u.RemoteLoginURL()
	advance(loginTTL - time.Second)
	if !localSession(local) {
		t.Fatal("fresh local link")
	}
	if !remoteSession(rem) {
		t.Fatal("fresh remote link")
	}
}

func TestLoginWithoutLink(t *testing.T) {
	u, d, _, base := setup(t)
	advance := clock(u)
	_, _, post, _ := loginAs(t, u, base)
	// Ссылка — единственный способ: выключить нельзя.
	if p := post("/settings/login/link", url.Values{"on": {"0"}}); !strings.Contains(p, "хотя бы один способ") || u.Protection().NoLink {
		t.Fatal("link disabled with no other way to log in", p)
	}
	post("/settings/login/password", url.Values{"password": {"correct horse"}, "repeat": {"correct horse"}})
	_, _, post = protectedSession(t, u, base, "correct horse")
	if p := post("/settings/login/link", url.Values{"on": {"0"}}); !strings.Contains(p, "текущий пароль") || u.Protection().NoLink {
		t.Fatal("link disabled without current password")
	}
	post("/settings/login/link", url.Values{"on": {"0"}, "current": {"correct horse"}})
	if !u.Protection().NoLink {
		t.Fatal("link not disabled")
	}
	_, _, post = protectedSession(t, u, base, "correct horse") // старая сессия закрыта; ссылка тоже ведёт ко входу
	// Последний оставшийся способ не снимается.
	if p := post("/settings/login/password", url.Values{"remove": {"1"}, "current": {"correct horse"}}); !strings.Contains(p, "хотя бы один способ") || u.Protection().Password == "" {
		t.Fatal("removed the last way to log in")
	}

	// Постоянный адрес: форма пароля без ссылки, GET попытку не начинает.
	b2 := newBrowser(t, base)
	st, page := b2.get("/")
	if st != 401 || !strings.Contains(page, `name="start"`) || !strings.Contains(page, `name="password"`) {
		t.Fatal("no login form at the permanent address", st, page)
	}
	if st, _ := b2.post("/login", url.Values{"start": {"1"}, "password": {"wrong password"}}); st != 401 || b2.loggedIn() {
		t.Fatal("wrong password logged in")
	}
	if b2.post("/login", url.Values{"start": {"1"}, "password": {"correct horse"}}); !b2.loggedIn() {
		t.Fatal("password at the permanent address did not log in")
	}
	// Чужой сайт попытку входа не начинает.
	req, _ := http.NewRequest("POST", base+"/login", strings.NewReader("start=1&password=correct+horse"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatal("cross-origin login start", r.StatusCode)
	}
	// Удалённо — тоже по постоянному адресу (закладка /ui/), без ссылки.
	d.AddPublicURL(func() string { return "https://dev.example" })
	remote := func(pw string) (string, bool) {
		rq, _ := http.NewRequest("POST", "https://dev.example/ui/login", strings.NewReader(url.Values{"start": {"1"}, "password": {pw}}.Encode()))
		rq.Host = "dev.example"
		rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := &recorder{h: http.Header{}}
		u.RemoteHandler().ServeHTTP(rec, rq)
		for _, c := range (&http.Response{Header: rec.h}).Cookies() {
			if c.Name == "g4a_rsession" && c.Value != "" {
				return rec.buf.String(), true
			}
		}
		return rec.buf.String(), false
	}
	if _, ok := remote("correct horse"); !ok {
		t.Fatal("remote login by password without a link")
	}
	// Перебор из интернета: задержка удалённо, вход на компьютере не закрыт.
	for i := 0; i < freeFailures; i++ {
		if _, ok := remote("guess"); ok {
			t.Fatal("wrong remote password")
		}
	}
	if p, ok := remote("correct horse"); ok || !strings.Contains(p, "подождите") {
		t.Fatal("no remote delay after failures", p)
	}
	b5 := newBrowser(t, base)
	if b5.post("/login", url.Values{"start": {"1"}, "password": {"correct horse"}}); !b5.loggedIn() {
		t.Fatal("remote failures locked the local login")
	}
	advance(2 * time.Second)
	if _, ok := remote("correct horse"); !ok {
		t.Fatal("remote login after delay")
	}

	// Только Telegram, без ссылки: подтверждение — по кнопке «Войти», не по GET.
	f := &fakeConfirm{}
	u.Confirm = f
	_, _, post3 := protectedSession(t, u, base, "correct horse")
	post3("/settings/login/telegram", url.Values{"on": {"1"}, "current": {"correct horse"}})
	_, _, post3 = protectedSessionTG(t, u, base, f, "correct horse")
	post3("/settings/login/password", url.Values{"remove": {"1"}, "current": {"correct horse"}})
	if p := u.Protection(); p.Password != "" || !p.Telegram || !p.NoLink {
		t.Fatal("telegram only", p)
	}
	b4 := newBrowser(t, base)
	n := f.n()
	if b4.get("/login"); f.n() != n {
		t.Fatal("GET must not ask Telegram")
	}
	b4.post("/login", url.Values{"start": {"1"}})
	if f.n() != n+1 || b4.loggedIn() {
		t.Fatal("start must ask Telegram and wait")
	}
	// Пока один запрос ждёт ответа, второй в Telegram не уходит.
	b6 := newBrowser(t, base)
	if _, page := b6.post("/login", url.Values{"start": {"1"}}); f.n() != n+1 || !strings.Contains(page, "уже ждёт ответа") {
		t.Fatal("second Telegram request while one is pending", f.n())
	}
	f.press(n, true)
	if b4.get("/login"); !b4.loggedIn() {
		t.Fatal("telegram-only login")
	}
	_, page = b6.get("/login")
	b6.post("/login", url.Values{"csrf": {csrfRe.FindStringSubmatch(page)[1]}, "retry": {"1"}})
	if f.n() != n+2 {
		t.Fatal("retry after the first request was answered")
	}
}

// protectedSessionTG — вход с паролем и подтверждением в Telegram.
func protectedSessionTG(t *testing.T, u *UI, base string, f *fakeConfirm, pw string) (*browser, string, func(string, url.Values) string) {
	t.Helper()
	b := newBrowser(t, base)
	b.post("/login", url.Values{"start": {"1"}, "password": {pw}})
	f.press(f.n()-1, true)
	b.get("/login")
	_, page := b.get("/settings")
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("not logged in", page)
	}
	return b, m[1], func(path string, v url.Values) string {
		t.Helper()
		v.Set("csrf", m[1])
		_, p := b.post(path, v)
		return p
	}
}

// TestPasswordLimiterParallel: параллельные неверные пароли не проходят
// задержку все сразу (проверка и учёт атомарны), а проверок argon2id
// одновременно не больше pwSlots.
func TestPasswordLimiterParallel(t *testing.T) {
	u, _, _, _ := setup(t)
	saved := kdf
	kdf.memory = 8 * 1024 // проверка заметно дольше старта горутин
	enc := hashPassword("correct horse")
	kdf = saved
	var wg sync.WaitGroup
	var wrong, delayed atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			switch err := u.verifyPassword(remote, enc, "guess"); {
			case err == nil:
				t.Error("wrong password accepted")
			case errors.Is(err, errWrongPassword):
				wrong.Add(1)
			default:
				delayed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wrong.Load() > freeFailures || wrong.Load()+delayed.Load() != 20 {
		t.Fatalf("checked %d passwords in parallel past the limiter (delayed %d)", wrong.Load(), delayed.Load())
	}
	// Слоты заняты — запрос не ждёт argon2id бесконечно и не считает пароль.
	for i := 0; i < cap(pwSlots); i++ {
		pwSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(pwSlots); i++ {
			<-pwSlots
		}
	}()
	old := pwSlotWait
	pwSlotWait = 20 * time.Millisecond
	defer func() { pwSlotWait = old }()
	if err := u.verifyPassword(local, enc, "correct horse"); err == nil || !strings.Contains(err.Error(), "at once") {
		t.Fatal("no semaphore on password checks:", err)
	}
}
