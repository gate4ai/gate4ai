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

// browser — новый браузер: открывает Web UI по адресу.
type browser struct {
	t    *testing.T
	c    *http.Client
	base string
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar, Transport: metaRefresh{}}, base: base}
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

// setupPassword — первый пароль через форму на этом компьютере.
func (b *browser) setupPassword(pw, repeat string) (int, string) {
	b.t.Helper()
	return b.post("/login", url.Values{"setup": {"1"}, "password": {pw}, "repeat": {repeat}})
}

// signInWith — вход по постоянному адресу: «Войти» с паролем.
func (b *browser) signInWith(pw string) (int, string) {
	b.t.Helper()
	return b.post("/login", url.Values{"start": {"1"}, "password": {pw}})
}

// remoteSession — удалённый запрос; ok — выдана cookie сессии /ui/.
func remoteDo(u *UI, method, path string, form url.Values) (string, bool) {
	var rd *strings.Reader
	if form != nil {
		rd = strings.NewReader(form.Encode())
	} else {
		rd = strings.NewReader("")
	}
	rq, _ := http.NewRequest(method, "https://dev.example"+path, rd)
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

// Свежая установка: пароля нет, первый, кто открыл localhost, задаёт его;
// удалённо пароль не задать; второй — «уже задан».
func TestFirstPassword(t *testing.T) {
	u, d, _, base := setup(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	if p := u.Protection(); p.Set() || len(p.Requires()) != 0 {
		t.Fatal("fresh install must have no password")
	}
	b := newBrowser(t, base)
	st, page := b.get("/")
	if st != 401 || !strings.Contains(page, `name="setup"`) || strings.Contains(page, `name="start"`) {
		t.Fatal("fresh install must ask for the first password", st, page)
	}
	// Удалённо — только подсказка, формы нет; задать пароль оттуда нельзя.
	if page, ok := remoteDo(u, "GET", "/ui/", nil); ok || strings.Contains(page, `name="setup"`) || !strings.Contains(page, "gate4ai ui") {
		t.Fatal("remote must not offer the first password", page)
	}
	if _, ok := remoteDo(u, "POST", "/ui/login", url.Values{"setup": {"1"}, "password": {"remote horse"}, "repeat": {"remote horse"}}); ok || u.Protection().Set() {
		t.Fatal("remote set the first password")
	}
	// Обычный вход без пароля невозможен.
	if st, _ := b.signInWith("anything"); st != 401 || b.loggedIn() {
		t.Fatal("sign-in before the password exists")
	}
	// Чужой сайт пароль не задаст.
	req, _ := http.NewRequest("POST", base+"/login", strings.NewReader("setup=1&password=evil+horse&repeat=evil+horse"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 || u.Protection().Set() {
		t.Fatal("cross-origin first password", r.StatusCode)
	}
	if _, page := b.setupPassword("short", "short"); !strings.Contains(page, "не короче 8") || u.Protection().Set() {
		t.Fatal("short password accepted", page)
	}
	if _, page := b.setupPassword("correct horse", "correct horsee"); !strings.Contains(page, "не совпадают") || u.Protection().Set() {
		t.Fatal("mismatch accepted")
	}
	if st, _ := b.setupPassword("correct horse", "correct horse"); st != 200 || !b.loggedIn() {
		t.Fatal("first password did not sign in", st)
	}
	p := u.Protection()
	if !strings.HasPrefix(p.Password, "$argon2id$") || strings.Contains(p.Password, "correct horse") || !checkPassword(p.Password, "correct horse") {
		t.Fatal("password must be stored as argon2id hash", p.Password)
	}
	if _, page := (&browser{t: t, c: &http.Client{}, base: base}).get("/settings"); strings.Contains(page, "argon2id$") {
		t.Fatal("hash leaked")
	}
	// Второй — опоздал: пароль уже задан, сессии нет, форма входа.
	b2 := newBrowser(t, base)
	if st, page := b2.setupPassword("evil horse", "evil horse"); st != 401 || b2.loggedIn() || !strings.Contains(page, "уже задан") {
		t.Fatal("second first-password accepted", st, page)
	}
	if !checkPassword(u.Protection().Password, "correct horse") {
		t.Fatal("password overwritten")
	}
	if st, page := b2.get("/"); st != 401 || !strings.Contains(page, `name="start"`) || strings.Contains(page, `name="setup"`) {
		t.Fatal("with a password the page must offer signing in", page)
	}
	// Сброс: пароля нет, сессии закрыты, снова форма первого пароля.
	if err := u.ResetProtection(); err != nil || u.Protection().Set() || b.loggedIn() {
		t.Fatal("reset")
	}
	if _, page := b2.get("/"); !strings.Contains(page, `name="setup"`) {
		t.Fatal("after reset the first password is asked again")
	}
}

func TestLoginPassword(t *testing.T) {
	u, d, _, base := setup(t)
	advance := clock(u)
	d.AddPublicURL(func() string { return "https://dev.example" })
	b0 := newBrowser(t, base)
	b0.setupPassword("correct horse", "correct horse")
	// Постоянный адрес: форма пароля, GET попытку не начинает.
	b := newBrowser(t, base)
	st, page := b.get("/")
	if st != 401 || !strings.Contains(page, `name="start"`) || !strings.Contains(page, `name="password"`) {
		t.Fatal("no login form at the permanent address", st, page)
	}
	u.mu.Lock()
	n := len(u.attempts)
	u.mu.Unlock()
	if n != 0 {
		t.Fatal("GET started an attempt")
	}
	if st, page := b.signInWith("wrong password"); st != 401 || b.loggedIn() || !strings.Contains(page, "неверный пароль") {
		t.Fatal("wrong password logged in", st, page)
	}
	// Пароль без «Войти» (нет попытки в cookie) — не вход.
	b2 := newBrowser(t, base)
	if st, _ := b2.post("/login", url.Values{"password": {"correct horse"}}); st != 401 || b2.loggedIn() {
		t.Fatal("password without an attempt logged in")
	}
	if st, _ := b.signInWith("correct horse"); st != 200 || !b.loggedIn() {
		t.Fatal("correct password rejected", st)
	}
	// Чужой сайт попытку входа не начинает.
	req, _ := http.NewRequest("POST", base+"/login", strings.NewReader("start=1&password=correct+horse"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatal("cross-origin login start", r.StatusCode)
	}
	// Слишком много неверных паролей в одной попытке — попытка закрывается.
	b3 := newBrowser(t, base)
	_, page = b3.signInWith("guess")
	for i := 1; i < maxTries; i++ {
		_, page = b3.submitPassword(page, "guess")
	}
	if strings.Contains(page, `name="csrf"`) || !strings.Contains(page, "начните вход заново") {
		t.Fatal("attempt must end after too many wrong passwords", page)
	}
	// Подряд неверные пароли из разных браузеров — задержка для всех.
	b4 := newBrowser(t, base)
	if _, page = b4.signInWith("correct horse"); !strings.Contains(page, "подождите") || b4.loggedIn() {
		t.Fatal("no delay after failures", page)
	}
	advance(2 * time.Second)
	if _, _ = b4.submitPassword(page, "correct horse"); !b4.loggedIn() {
		t.Fatal("login after delay")
	}
	// Попытка входа живёт 5 минут.
	b5 := newBrowser(t, base)
	_, page = b5.signInWith("guess")
	advance(attemptTTL + time.Second)
	if _, _ = b5.submitPassword(page, "correct horse"); b5.loggedIn() {
		t.Fatal("expired attempt accepted")
	}
	// Сессия живёт 12 часов.
	advance(sessionTTL)
	if b.loggedIn() {
		t.Fatal("session outlived sessionTTL")
	}

	// Удалённо — тоже по постоянному адресу (закладка /ui/), свой счётчик перебора.
	if _, ok := remoteDo(u, "POST", "/ui/login", url.Values{"start": {"1"}, "password": {"correct horse"}}); !ok {
		t.Fatal("remote login by password")
	}
	for i := 0; i < freeFailures; i++ {
		if _, ok := remoteDo(u, "POST", "/ui/login", url.Values{"start": {"1"}, "password": {"guess"}}); ok {
			t.Fatal("wrong remote password")
		}
	}
	if p, ok := remoteDo(u, "POST", "/ui/login", url.Values{"start": {"1"}, "password": {"correct horse"}}); ok || !strings.Contains(p, "подождите") {
		t.Fatal("no remote delay after failures", p)
	}
	b6 := newBrowser(t, base)
	if b6.signInWith("correct horse"); !b6.loggedIn() {
		t.Fatal("remote failures locked the local login")
	}
	advance(2 * time.Second)
	if _, ok := remoteDo(u, "POST", "/ui/login", url.Values{"start": {"1"}, "password": {"correct horse"}}); !ok {
		t.Fatal("remote login after delay")
	}

	// Сменить пароль — только с текущим; снять нельзя; смена закрывает другие сессии.
	_, _, post := protectedSession(t, u, base, "correct horse")
	if p := post("/settings/login/password", url.Values{"current": {"nope"}, "password": {"battery staple"}, "repeat": {"battery staple"}}); !strings.Contains(p, "текущий пароль") || checkPassword(u.Protection().Password, "battery staple") {
		t.Fatal("changed without current password")
	}
	advance(time.Hour) // задержка после неверного пароля
	if p := post("/settings/login/password", url.Values{"current": {"correct horse"}, "password": {"short"}, "repeat": {"short"}}); !strings.Contains(p, "не короче 8") {
		t.Fatal("short password accepted", p)
	}
	advance(time.Hour)
	post("/settings/login/password", url.Values{"current": {"correct horse"}, "password": {"battery staple"}, "repeat": {"battery staple"}})
	if !checkPassword(u.Protection().Password, "battery staple") {
		t.Fatal("password not changed")
	}
	if b6.loggedIn() {
		t.Fatal("other sessions must be closed when the password changes")
	}
	advance(time.Hour)
	_, _, post = protectedSession(t, u, base, "battery staple")
	if p := post("/settings/login/password", url.Values{"current": {"battery staple"}, "remove": {"1"}}); !strings.Contains(p, "не короче 8") || !u.Protection().Set() {
		t.Fatal("password removed", p)
	}
}

// protectedSession — вход с паролем; post — действие формы этой сессии.
func protectedSession(t *testing.T, u *UI, base, pw string) (*browser, string, func(string, url.Values) string) {
	t.Helper()
	b := newBrowser(t, base)
	b.signInWith(pw)
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

func TestLoginTelegram(t *testing.T) {
	u, _, _, base := setup(t)
	newBrowser(t, base).setupPassword("correct horse", "correct horse")
	_, _, post := protectedSession(t, u, base, "correct horse")
	if p := post("/settings/login/telegram", url.Values{"on": {"1"}, "current": {"correct horse"}}); !strings.Contains(p, "подключите Telegram-бот") || u.Protection().Telegram {
		t.Fatal("telegram confirmation enabled without a bot")
	}
	f := &fakeConfirm{}
	u.Confirm = f
	// Включить — только с текущим паролем.
	if p := post("/settings/login/telegram", url.Values{"on": {"1"}}); !strings.Contains(p, "текущий пароль") || u.Protection().Telegram {
		t.Fatal("telegram enabled without current password")
	}
	if p := post("/settings/login/telegram", url.Values{"on": {"1"}, "current": {"correct horse"}}); !strings.Contains(p, "подтверждается в Telegram") {
		t.Fatal("not enabled", p)
	}
	if r := u.Protection().Requires(); len(r) != 2 {
		t.Fatal("both factors", r)
	}

	// Telegram спрашивается только после пароля; GET ничего не отправляет.
	b := newBrowser(t, base)
	b.get("/login")
	if f.n() != 0 {
		t.Fatal("GET must not ask Telegram")
	}
	b.signInWith("wrong password")
	if f.n() != 0 {
		t.Fatal("Telegram must be asked only after the password")
	}
	st, page := b.signInWith("correct horse")
	code := regexp1(`id="code">(\d{6})<`, page)
	if st != 401 || code == "" || f.n() != 1 || !strings.Contains(f.reqs[0], code) || b.loggedIn() || !strings.Contains(page, "Подтвердите вход") {
		t.Fatal("login must wait for Telegram", st, f.reqs)
	}
	if !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatal("waiting page must refresh itself")
	}
	// Пока один запрос ждёт ответа, второй в Telegram не уходит.
	b2 := newBrowser(t, base)
	if _, page := b2.signInWith("correct horse"); f.n() != 1 || !strings.Contains(page, "уже ждёт ответа") {
		t.Fatal("second Telegram request while one is pending", f.n())
	}
	f.press(0, true)
	if b.get("/login"); !b.loggedIn() { // страница ожидания обновляется сама
		t.Fatal("confirmed login has no session")
	}
	// Повтор отправки после ответа на первый запрос.
	_, page = b2.get("/login")
	b2.post("/login", url.Values{"csrf": {csrfRe.FindStringSubmatch(page)[1]}, "retry": {"1"}})
	if f.n() != 2 {
		t.Fatal("retry after the first request was answered")
	}
	// Отклонено в Telegram — сессии нет и не будет.
	f.press(1, false)
	if _, page := b2.get("/login"); !strings.Contains(page, "отклонён") || b2.loggedIn() {
		t.Fatal("rejected login")
	}
	f.press(1, true) // повторное нажатие после отказа
	if b2.get("/login"); b2.loggedIn() {
		t.Fatal("login after rejection")
	}
	// Выключить — тоже с текущим паролем; сессия входа с Telegram годится.
	_, _, post3 := protectedSessionTG(t, u, base, f, "correct horse")
	post3("/settings/login/telegram", url.Values{"on": {"0"}, "current": {"correct horse"}})
	if u.Protection().Telegram {
		t.Fatal("telegram not disabled")
	}
	// Восстановление: сброс снимает всё и закрывает сессии.
	if err := u.ResetProtection(); err != nil || u.Protection().Set() || b.loggedIn() {
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

// Удалённый вход с Telegram: подтверждение в сообщении называет удалённый вход.
func TestRemoteLoginTelegram(t *testing.T) {
	u, d, _, base := setup(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	f := &fakeConfirm{}
	u.Confirm = f
	newBrowser(t, base).setupPassword("correct horse", "correct horse")
	d.State.Set(protectionKey, `{"password":"`+u.Protection().Password+`","telegram":true}`)
	if page, ok := remoteDo(u, "POST", "/ui/login", url.Values{"start": {"1"}, "password": {"correct horse"}}); ok || f.n() != 1 || !strings.Contains(f.reqs[0], "удалённо") && !strings.Contains(f.reqs[0], "remotely") {
		t.Fatal("remote login must wait for Telegram", ok, f.reqs, page)
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
