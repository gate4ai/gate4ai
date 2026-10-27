// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
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

// setPassword — пароль Web UI в тестах (в жизни первый генерирует EnsurePassword,
// дальше его меняют в Настройках).
func setPassword(t *testing.T, u *UI, pw string) {
	t.Helper()
	if err := u.D.State.Set(protectionKey, `{"password":"`+hashPassword(pw)+`"}`); err != nil {
		t.Fatal(err)
	}
}

// signInWith — вход по постоянному адресу: «Войти» с паролем.
func (b *browser) signInWith(pw string) (int, string) {
	b.t.Helper()
	return b.post("/login", url.Values{"start": {"1"}, "password": {pw}})
}

// remoteLogin — вход по паролю удалённо: cookie сессии /ui/.
func remoteLogin(t *testing.T, u *UI, pw string) *http.Cookie {
	t.Helper()
	rq, _ := http.NewRequest("POST", "https://dev.example/ui/login", strings.NewReader(url.Values{"start": {"1"}, "password": {pw}}.Encode()))
	rq.Host = "dev.example"
	rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := &recorder{h: http.Header{}}
	u.RemoteHandler().ServeHTTP(rec, rq)
	for _, c := range (&http.Response{Header: rec.h}).Cookies() {
		if c.Name == "g4a_rsession" && c.Value != "" {
			return c
		}
	}
	t.Fatal("no remote session", rec.buf.String())
	return nil
}

// remoteGet — страница удалённого Web UI в сессии ck.
func remoteGet(u *UI, ck *http.Cookie, path string) string {
	rq, _ := http.NewRequest("GET", "https://dev.example"+path, nil)
	rq.Host = "dev.example"
	rq.AddCookie(ck)
	rec := &recorder{h: http.Header{}}
	u.RemoteHandler().ServeHTTP(rec, rq)
	return rec.buf.String()
}

// remoteDo — удалённый запрос; ok — выдана cookie сессии /ui/.
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

// Свежая установка: пароль генерирует daemon (EnsurePassword) — сложный, на
// диске только хэш, открытый текст виден владельцу (InitialPassword) до первого
// входа. Формы «задайте пароль» нет ни на этом компьютере, ни удалённо.
func TestGeneratedPassword(t *testing.T) {
	u, d, _, base := setup(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	if p := u.Protection(); p.Set() || len(p.Requires()) != 0 || u.InitialPassword() != "" {
		t.Fatal("fresh install must have no password before EnsurePassword")
	}
	// До генерации войти нельзя ничем — ни пустым паролем, ни формой первого пароля.
	b := newBrowser(t, base)
	if st, page := b.get("/"); st != 401 || !strings.Contains(page, `name="start"`) || strings.Contains(page, `name="setup"`) {
		t.Fatal("must ask for the password, never offer to set it", st, page)
	}
	if st, _ := b.post("/login", url.Values{"setup": {"1"}, "password": {"evil horse"}, "repeat": {"evil horse"}}); st != 401 || b.loggedIn() || u.Protection().Set() {
		t.Fatal("the first-password form must be gone")
	}
	if st, _ := b.signInWith(""); st != 401 || b.loggedIn() {
		t.Fatal("signed in with an empty password")
	}
	pw, err := u.EnsurePassword()
	if err != nil || !regexp.MustCompile(`^[a-z2-9]{5}(-[a-z2-9]{5}){3}$`).MatchString(pw) || strings.ContainsAny(pw, "01lio") {
		t.Fatal("generated password", pw, err)
	}
	if again, _ := u.EnsurePassword(); again != "" || !checkPassword(u.Protection().Password, pw) {
		t.Fatal("EnsurePassword must not replace an existing password")
	}
	if u.InitialPassword() != pw || len(u.Protection().Requires()) != 1 {
		t.Fatal("the generated password must be shown until the first sign-in")
	}
	p := u.Protection()
	if !strings.HasPrefix(p.Password, "$argon2id$") || strings.Contains(p.Password, pw) {
		t.Fatal("password must be stored as argon2id hash", p.Password)
	}
	if v, _ := d.State.Get(protectionKey); strings.Contains(v, pw) {
		t.Fatal("plaintext password on disk")
	}
	// Удалённо — та же форма пароля, без формы первого пароля.
	if page, ok := remoteDo(u, "GET", "/ui/", nil); ok || strings.Contains(page, `name="setup"`) || !strings.Contains(page, `name="start"`) {
		t.Fatal("remote must ask for the password", page)
	}
	if _, ok := remoteDo(u, "POST", "/ui/login", url.Values{"setup": {"1"}, "password": {"remote horse"}, "repeat": {"remote horse"}}); ok || !checkPassword(u.Protection().Password, pw) {
		t.Fatal("remote replaced the password")
	}
	// Чужой сайт попытку не начнёт.
	req, _ := http.NewRequest("POST", base+"/login", strings.NewReader("start=1&password="+pw))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Fatal("cross-origin sign-in", r.StatusCode)
	}
	// Вход сгенерированным паролем; после него пароль больше не показывается.
	if st, _ := b.signInWith("wrong-wrong-wrong-wrong"); st != 401 || b.loggedIn() {
		t.Fatal("wrong password signed in")
	}
	if st, _ := b.signInWith(pw); st != 200 || !b.loggedIn() {
		t.Fatal("generated password did not sign in", st)
	}
	if u.InitialPassword() != "" {
		t.Fatal("the password is still shown after the first sign-in")
	}
	if _, page := (&browser{t: t, c: &http.Client{}, base: base}).get("/settings"); strings.Contains(page, "argon2id$") {
		t.Fatal("hash leaked")
	}
	// Сброс: новый пароль, старый не подходит, сессии закрыты, Telegram выключен.
	d.State.Set(protectionKey, `{"password":"`+u.Protection().Password+`","telegram":true}`)
	pw2, err := u.ResetLogin()
	if err != nil || pw2 == "" || pw2 == pw || u.InitialPassword() != pw2 || u.Protection().Telegram || b.loggedIn() {
		t.Fatal("reset", pw2, err)
	}
	if st, _ := b.signInWith(pw); st != 401 || b.loggedIn() {
		t.Fatal("old password works after reset")
	}
	if st, _ := b.signInWith(pw2); st != 200 || !b.loggedIn() || u.InitialPassword() != "" {
		t.Fatal("new password after reset", st)
	}
	// Смена пароля в Настройках тоже прячет сгенерированный.
	u.ResetLogin()
	_, _, post := protectedSession(t, u, base, u.InitialPassword())
	if u.InitialPassword() != "" {
		t.Fatal("shown after sign-in")
	}
	u.mu.Lock()
	u.initial = "stale"
	u.mu.Unlock()
	post("/settings/login/password", url.Values{"current": {u.InitialPassword()}, "password": {"correct horse"}, "repeat": {"correct horse"}})
	if u.InitialPassword() != "" && checkPassword(u.Protection().Password, "correct horse") {
		t.Fatal("still shown after a password change")
	}
}

func TestLoginPassword(t *testing.T) {
	u, d, _, base := setup(t)
	advance := clock(u)
	d.AddPublicURL(func() string { return "https://dev.example" })
	setPassword(t, u, "correct horse")
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
	setPassword(t, u, "correct horse")
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
	// Восстановление: сброс снимает Telegram, меняет пароль и закрывает сессии.
	if pw, err := u.ResetLogin(); err != nil || pw == "" || u.Protection().Telegram || !checkPassword(u.Protection().Password, pw) || b.loggedIn() {
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
	u, d, _, _ := setup(t)
	d.AddPublicURL(func() string { return "https://dev.example" })
	f := &fakeConfirm{}
	u.Confirm = f
	setPassword(t, u, "correct horse")
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

// Пароль, отданный скрипту или агенту (gate4ai ui без терминала), после входа
// напоминает о себе на каждой странице, пока его не сменят или не включат Telegram.
func TestRelayedPasswordNotice(t *testing.T) {
	u, d, _, base := setup(t)
	pw, _ := u.EnsurePassword()
	// Человек в терминале: без пометки.
	if err := u.MarkRelayed(); err != nil || !u.Protection().Relayed {
		t.Fatal("mark relayed", err)
	}
	b, _, post := protectedSession(t, u, base, pw)
	_, page := b.get("/")
	if !strings.Contains(page, "data-notice") || !strings.Contains(page, "прошёл через чат") {
		t.Fatal("no notice after signing in with a relayed password", page)
	}
	// «Настройки → Вход» — ссылка на раздел; отдельного хвоста «Вход» после текста нет.
	notice := regexp.MustCompile(`(?s)<div class="flash" role="status" data-notice>(.*?)</div>`).FindStringSubmatch(page)
	if notice == nil || !strings.Contains(notice[1], `<a href="/settings/login">Настройки → Вход</a>`) || strings.HasSuffix(strings.TrimSpace(notice[1]), "Вход</a>") {
		t.Fatal("the notice must link Settings → Signing in inside the text:", notice)
	}
	if _, page := b.get("/journal"); !strings.Contains(page, "data-notice") {
		t.Fatal("the notice must be on every page")
	}
	// Удалённо — та же плашка со ссылкой на тот же раздел под /ui/.
	d.AddPublicURL(func() string { return "https://dev.example" })
	if page := remoteGet(u, remoteLogin(t, u, pw), "/ui/journal"); !strings.Contains(page, `<a href="/ui/settings/login">Настройки → Вход</a>`) {
		t.Fatal("remote notice", page)
	}
	post("/settings/login/password", url.Values{"current": {pw}, "password": {"correct horse"}, "repeat": {"correct horse"}})
	b2, _, _ := protectedSession(t, u, base, "correct horse")
	if _, page := b2.get("/"); strings.Contains(page, "data-notice") || u.Protection().Relayed {
		t.Fatal("the notice must go away after a password change", page)
	}
	// Пароль уже использован (вошли): пометка больше не ставится.
	if err := u.MarkRelayed(); err != nil || u.Protection().Relayed {
		t.Fatal("relayed after the password was used", err)
	}
}

// Перезапуск daemon до первого входа: открытый текст утерян, и ShownPassword
// (`gate4ai ui --print`, установщик) генерирует новый — прежний перестаёт
// действовать; после входа или смены пароля новый не генерируется, а у
// установок без признака fresh (до его появления) пароль не трогается.
func TestPasswordRegeneratedUntilFirstSignIn(t *testing.T) {
	u, d, _, base := setup(t)
	pw, _ := u.EnsurePassword()
	if shown, err := u.ShownPassword(); err != nil || shown != pw {
		t.Fatal("shown while in memory", shown, err)
	}
	u.MarkRelayed()
	// «Перезапуск»: новый UI над тем же State Store.
	u2 := New(d)
	if again, _ := u2.EnsurePassword(); again != "" || u2.InitialPassword() != "" {
		t.Fatal("restart must not generate at start", again)
	}
	pw2, err := u2.ShownPassword()
	if err != nil || pw2 == "" || pw2 == pw || u2.InitialPassword() != pw2 || !u2.Protection().Relayed || !u2.Protection().Fresh {
		t.Fatal("regenerate after restart", pw2, err, u2.Protection())
	}
	if again, _ := u2.ShownPassword(); again != pw2 {
		t.Fatal("must not regenerate while in memory")
	}
	b := newBrowser(t, base)
	if st, _ := b.signInWith(pw); st != 401 || b.loggedIn() {
		t.Fatal("the previous password still works")
	}
	if st, _ := b.signInWith(pw2); st != 200 || !b.loggedIn() {
		t.Fatal("new password", st)
	}
	// Вошли: признак снят, после перезапуска показать нечего и генерировать не надо.
	if p := u.Protection(); p.Fresh || !checkPassword(p.Password, pw2) {
		t.Fatal("fresh after sign-in", p)
	}
	u3 := New(d)
	if shown, err := u3.ShownPassword(); err != nil || shown != "" || !checkPassword(u3.Protection().Password, pw2) {
		t.Fatal("regenerated after the first sign-in", shown, err)
	}
	// Смена пароля в настройках тоже снимает признак.
	pw3, _ := u.ResetLogin()
	_, _, post := protectedSession(t, u, base, pw3)
	u.mu.Lock()
	p := u.Protection()
	p.Fresh = true
	bb, _ := json.Marshal(p)
	d.State.Set(protectionKey, string(bb))
	u.mu.Unlock()
	post("/settings/login/password", url.Values{"current": {pw3}, "password": {"correct horse"}, "repeat": {"correct horse"}})
	if p := u.Protection(); p.Fresh || !checkPassword(p.Password, "correct horse") {
		t.Fatal("fresh after a password change", p)
	}
	// Запись прежней версии без признака: пароль не трогается.
	d.State.Set(protectionKey, `{"password":"`+u.Protection().Password+`"}`)
	if shown, err := New(d).ShownPassword(); err != nil || shown != "" || !checkPassword(u.Protection().Password, "correct horse") {
		t.Fatal("legacy record regenerated", shown, err)
	}
}
