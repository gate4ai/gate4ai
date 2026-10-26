// SPDX-License-Identifier: AGPL-3.0-only

package webui

// Защита входа — пароль (хэш argon2id в State Store), обязательный всегда, и по
// желанию владельца подтверждение входа в личном Telegram-боте — на другом
// устройстве, не на этом компьютере. Вход начинается на постоянном адресе Web UI
// — и на этом компьютере, и удалённо (/ui/ на публичном адресе): сохранённая
// закладка работает и через неделю; дальше — сессия в cookie. Пароля ещё нет
// (свежая установка, сброс) — daemon генерирует его сам при старте: на диске
// только хэш, открытый текст живёт в памяти daemon и отдаётся владельцу через
// сокет владельца (gate4ai ui, установщик, трей), пока им не вошли. Формы
// «задайте пароль» нет: с первой секунды, в том числе с публичного адреса, Web
// UI требует пароль. Попытка входа привязана к браузеру cookie. От перебора
// пароля — задержка после неверных паролей, у удалённого входа свой счётчик.
// Настройки защиты хранятся не в конфигурации (её может менять агент через
// gate4ai.change_policy), а в State Store; API, который их отдаёт, нет.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// protectionKey — ключ настроек защиты в kv State Store.
const protectionKey = "ui.login"

// Protection — защита входа в Web UI. Прежнее поле no_link (вход без
// одноразовой ссылки) больше не читается: ссылки нет.
type Protection struct {
	Password string `json:"password,omitempty"` // $argon2id$v=19$m=…,t=…,p=…$соль$хэш; "" — пароль ещё не сгенерирован
	Telegram bool   `json:"telegram,omitempty"` // подтверждение входа в Telegram-боте
}

// Set — пароль задан. "" бывает только до EnsurePassword при старте daemon:
// такой пароль не подходит никогда.
func (p Protection) Set() bool { return p.Password != "" }

// passwordAlphabet — символы сгенерированного пароля: без похожих (0/o, 1/l/i),
// только строчные — его печатают с экрана и набирают на телефоне.
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// generatePassword — сложный пароль Web UI: 4 группы по 5 символов
// (≈99 бит), вид xxxxx-xxxxx-xxxxx-xxxxx.
func generatePassword() string {
	var b strings.Builder
	for g := range 4 {
		if g > 0 {
			b.WriteByte('-')
		}
		for range 5 {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
			b.WriteByte(passwordAlphabet[n.Int64()])
		}
	}
	return b.String()
}

// EnsurePassword — пароль Web UI есть с первой секунды: если его нет (свежая
// установка, сброс), daemon генерирует его сам. В State Store — только хэш;
// открытый текст возвращается и хранится в памяти (InitialPassword) до первого
// входа или смены пароля. Пароль уже есть — "" (перезапуск: показать нечего,
// забытый пароль сбрасывает ResetLogin).
func (u *UI) EnsurePassword() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	p := u.Protection()
	if p.Set() {
		return "", nil
	}
	pw := generatePassword()
	p.Password = hashPassword(pw)
	b, _ := json.Marshal(p)
	if err := u.D.State.Set(protectionKey, string(b)); err != nil {
		return "", err
	}
	u.initial = pw
	return pw, nil
}

// InitialPassword — сгенерированный пароль, которым ещё не входили; "" — его
// уже использовали, сменили или daemon перезапускался. Только для сокета
// владельца: в журнал и ответы агентам не попадает.
func (u *UI) InitialPassword() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.initial
}

// forgetInitial — пароль использован или сменён: показывать больше нечего (под u.mu).
func (u *UI) forgetInitial() { u.initial = "" }

// minPassword — минимальная длина пароля Web UI.
const minPassword = 8

// newPassword проверяет новый пароль и его повтор из формы.
func newPassword(pw, repeat string) error {
	switch {
	case len([]rune(pw)) < minPassword:
		return errors.New("the password must be at least 8 characters")
	case pw != repeat:
		return errors.New("the passwords do not match")
	}
	return nil
}

// Requires — что нужно для входа: password, telegram.
func (p Protection) Requires() []string {
	out := []string{}
	if p.Password != "" {
		out = append(out, "password")
	}
	if p.Telegram {
		out = append(out, "telegram")
	}
	return out
}

// Protection — текущая защита входа. Повреждённая запись закрывает вход
// (пароль, который не подходит никогда): сбросить — `gate4ai daemon --reset-ui-login`.
func (u *UI) Protection() Protection {
	var p Protection
	if v, ok := u.D.State.Get(protectionKey); ok {
		if err := json.Unmarshal([]byte(v), &p); err != nil {
			return Protection{Password: "invalid"}
		}
	}
	return p
}

// ResetLogin — восстановление при забытом пароле или недоступном Telegram:
// снимает подтверждение в Telegram, генерирует новый пароль и закрывает все
// сессии и попытки входа. Возвращает новый пароль (его печатает
// `gate4ai ui --reset-password`); он же — InitialPassword до первого входа.
// Сброс виден владельцу: уведомление в Telegram, если бот подключён.
func (u *UI) ResetLogin() (string, error) {
	if err := u.D.State.Set(protectionKey, "{}"); err != nil {
		return "", err
	}
	u.dropSessions("")
	pw, err := u.EnsurePassword()
	if err != nil {
		return "", err
	}
	if u.Channels != nil {
		if tg := u.Channels.Telegram(); tg != nil {
			go tg.Notice("⚠️ Защита входа в Web UI gate4.ai сброшена: новый пароль показан на компьютере (gate4ai ui), подтверждение входа в Telegram выключено, сессии закрыты. Если это были не вы — войдите и смените пароль в «Настройки → Вход».")
		}
	}
	return pw, nil
}

// kdf — параметры argon2id для новых хэшей (RFC 9106; в тестах меньше).
var kdf = struct {
	time, memory uint32
	threads      uint8
}{3, 64 * 1024, 2}

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	h := argon2.IDKey([]byte(pw), salt, kdf.time, kdf.memory, kdf.threads, 32)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, kdf.memory, kdf.time, kdf.threads, b64(salt), b64(h))
}

func checkPassword(enc, pw string) bool {
	parts := strings.Split(enc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m > 1<<21 || t == 0 || t > 64 || p == 0 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) < 16 {
		return false
	}
	return subtle.ConstantTimeCompare(argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want))), want) == 1
}

// LoginConfirmer — подтверждение входа на другом устройстве (Telegram-бот).
type LoginConfirmer interface {
	ConfirmLogin(id, text string, done func(ok bool)) error
}

func (u *UI) confirmer() LoginConfirmer {
	if u.Confirm != nil {
		return u.Confirm
	}
	if t := u.Channels.Telegram(); t != nil {
		return t
	}
	return nil
}

// Состояние подтверждения в Telegram у попытки входа.
const (
	tgNone     = ""         // не нужно
	tgNeed     = "need"     // нужно, ещё не отправлено (сначала пароль)
	tgSent     = "sent"     // отправлено, ждём нажатия
	tgOK       = "ok"       // вход разрешён
	tgRejected = "rejected" // вход отклонён
	tgError    = "error"    // не отправилось (tgErr — почему)
)

const (
	attemptTTL   = 5 * time.Minute
	maxAttempts  = 32 // одновременных попыток входа
	maxTries     = 5  // неверных паролей на одну попытку
	freeFailures = 5  // подряд неверных паролей без задержки (на все попытки)
)

// attempt — попытка входа этого браузера: пароль, затем Telegram.
type attempt struct {
	remote   bool
	expires  time.Time
	csrf     string
	code     string // код попытки: на странице и в сообщении Telegram
	password bool   // пароль ещё не введён
	telegram string
	tgErr    string
	tries    int
}

func (m mount) attemptCookie() string {
	if m.remote {
		return "g4a_rlogin"
	}
	return "g4a_login"
}

func (u *UI) setAttemptCookie(w http.ResponseWriter, m mount, id string) {
	http.SetCookie(w, &http.Cookie{Name: m.attemptCookie(), Value: id, Path: m.base + "/", HttpOnly: true, Secure: m.remote,
		SameSite: http.SameSiteStrictMode, MaxAge: int(attemptTTL.Seconds())})
}

// loginPage — страница «нужен вход»: без попытки входа, начало входа (форма
// пароля) — одинаковая на этом компьютере и удалённо.
func (u *UI) loginPage(w http.ResponseWriter, r *http.Request, m mount) {
	u.loginPageErr(w, r, m, "")
}

func (u *UI) loginPageErr(w http.ResponseWriter, r *http.Request, m mount, errMsg string) {
	p := u.Protection()
	v := view{Path: m.base + "/", Error: tr(u.lang(r), errMsg)}
	v.Data = map[string]any{"Start": true, "Telegram": map[bool]string{true: tgNeed}[p.Telegram]}
	u.login401(w, r, m, "login_step.html", v)
}

// login401 — страница входа name с кодом 401: вход ещё не выполнен.
func (u *UI) login401(w http.ResponseWriter, r *http.Request, m mount, name string, v view) {
	v.Base, v.Remote, v.Lang = m.base, m.remote, u.lang(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(401)
	_ = tplIn(v.Lang).ExecuteTemplate(w, name, v)
}

// errWrongPassword — неверный пароль Web UI.
var errWrongPassword = errors.New("wrong password")

func loginCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

// newAttempt — новая попытка входа (вызывать под u.mu).
func (u *UI) newAttempt(m mount, p Protection) string {
	now := u.Now()
	oldest := ""
	for id, a := range u.attempts {
		if now.After(a.expires) {
			delete(u.attempts, id)
		} else if oldest == "" || a.expires.Before(u.attempts[oldest].expires) {
			oldest = id
		}
	}
	if len(u.attempts) >= maxAttempts {
		delete(u.attempts, oldest)
	}
	a := &attempt{remote: m.remote, expires: now.Add(attemptTTL), csrf: random(), code: loginCode(), password: true}
	if p.Telegram {
		a.telegram = tgNeed
	}
	id := random()
	u.attempts[id] = a
	return id
}

// attemptOf — попытка входа этого браузера (вызывать под u.mu).
func (u *UI) attemptOf(m mount, r *http.Request) (string, *attempt) {
	c, err := r.Cookie(m.attemptCookie())
	if err != nil {
		return "", nil
	}
	a, ok := u.attempts[c.Value]
	if !ok || a.remote != m.remote || u.Now().After(a.expires) {
		delete(u.attempts, c.Value)
		return "", nil
	}
	return c.Value, a
}

// startSession выдаёт сессию; telegram — вход подтверждён в Telegram (удалённое
// управление браузером без отдельного подтверждения).
func (u *UI) startSession(w http.ResponseWriter, m mount, telegram bool) {
	sid := random()
	u.mu.Lock()
	u.sessions[sid] = session{csrf: random(), expires: u.Now().Add(sessionTTL), remote: m.remote, telegram: telegram}
	u.forgetInitial() // паролем вошли: показывать его больше незачем
	u.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: m.cookie(), Value: sid, Path: m.base + "/", HttpOnly: true, Secure: m.remote,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
}

// sessionTTL — срок сессии Web UI.
const sessionTTL = 12 * time.Hour

// dropSessions закрывает все сессии, кроме keep, и все попытки входа.
func (u *UI) dropSessions(keep string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for sid := range u.sessions {
		if sid != keep {
			delete(u.sessions, sid)
		}
	}
	u.attempts = map[string]*attempt{}
}

// limiter — защита от перебора пароля: после freeFailures неверных подряд каждая
// следующая попытка ждёт вдвое дольше (до 15 минут); верный пароль сбрасывает.
// У локального и удалённого входа счётчики свои: перебор из интернета не
// закрывает вход на самом компьютере.
type limiter struct {
	failures int       // неверных паролей подряд
	until    time.Time // до этого времени пароль не проверяется
}

func (u *UI) limiter(m mount) *limiter {
	if m.remote {
		return &u.limits[1]
	}
	return &u.limits[0]
}

// passwordAllowed — не действует ли задержка после неверных паролей (под u.mu).
func (u *UI) passwordAllowed(m mount) (time.Duration, bool) {
	if d := u.limiter(m).until.Sub(u.Now()); d > 0 {
		return d, false
	}
	return 0, true
}

// passwordResult учитывает результат ввода пароля (под u.mu).
func (u *UI) passwordResult(m mount, ok bool) {
	l := u.limiter(m)
	if ok {
		l.failures, l.until = 0, time.Time{}
		return
	}
	l.failures++
	if n := l.failures - freeFailures; n >= 0 {
		d := time.Second << min(n, 10)
		l.until = u.Now().Add(min(d, 15*time.Minute))
	}
}

// pwSlots — одновременных проверок пароля (argon2id — 64 МиБ на проверку):
// сотни параллельных запросов из интернета не съедят память daemon.
var pwSlots = make(chan struct{}, 2)

// pwSlotWait — сколько запрос ждёт свободного слота проверки пароля.
var pwSlotWait = 3 * time.Second

// verifyPassword — пароль с учётом задержки после неверных попыток. Проверка
// и учёт атомарны: попытка заранее считается неверной (до argon2id), верный
// пароль это откатывает. Так параллельные запросы не проходят задержку все
// сразу — за окно задержки проверяется не больше одного пароля.
func (u *UI) verifyPassword(m mount, enc, pw string) error {
	select {
	case pwSlots <- struct{}{}:
		defer func() { <-pwSlots }()
	case <-time.After(pwSlotWait):
		return errors.New("Too many login attempts at once: wait and try again")
	}
	u.mu.Lock()
	wait, ok := u.passwordAllowed(m)
	if ok {
		u.passwordResult(m, false) // резерв: неверный, пока не доказано обратное
	}
	u.mu.Unlock()
	if !ok {
		return M("Too many wrong passwords: wait %d s", int(wait.Seconds())+1)
	}
	if !checkPassword(enc, pw) {
		return errWrongPassword
	}
	u.mu.Lock()
	u.passwordResult(m, true)
	u.mu.Unlock()
	return nil
}

// telegramBusy — другая попытка входа этого же места (локально / удалённо) ждёт
// ответа в Telegram (под u.mu).
func (u *UI) telegramBusy(m mount, id string) bool {
	now := u.Now()
	for aid, a := range u.attempts {
		if aid != id && a.remote == m.remote && a.telegram == tgSent && !now.After(a.expires) {
			return true
		}
	}
	return false
}

// loginPost — начало попытки входа («Войти» с паролем), ввод пароля и
// повторная отправка в Telegram на странице попытки входа.
func (u *UI) loginPost(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.originOK(m, r) {
			http.Error(w, "bad origin", 403)
			return
		}
		_ = r.ParseForm()
		u.mu.Lock()
		id, a := u.attemptOf(m, r)
		// Попытка начинается кнопкой «Войти» на постоянном адресе (POST с проверкой
		// Origin: чужой сайт попытку не начнёт и Telegram не потревожит). Повторное
		// «Войти» начинает попытку заново (неверные пароли считает общая задержка).
		if p := u.Protection(); r.PostForm.Get("start") == "1" {
			delete(u.attempts, id)
			id = u.newAttempt(m, p)
			a = u.attempts[id]
			u.setAttemptCookie(w, m, id)
			r.PostForm.Set("csrf", a.csrf)
		}
		if a == nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(a.csrf)) != 1 {
			u.mu.Unlock()
			u.loginPage(w, r, m)
			return
		}
		needPw, tg := a.password, a.telegram
		u.mu.Unlock()
		msg := ""
		if needPw {
			if err := u.verifyPassword(m, u.Protection().Password, r.PostForm.Get("password")); err != nil {
				msg = say(u.lang(r), err)
				u.mu.Lock()
				if a.tries++; a.tries >= maxTries {
					delete(u.attempts, id)
					u.mu.Unlock()
					u.loginPageErr(w, r, m, "Too many wrong passwords — start signing in again.")
					return
				}
				u.mu.Unlock()
			} else {
				u.mu.Lock()
				a.password = false
				u.mu.Unlock()
			}
		} else if tg == tgError && r.PostForm.Has("retry") {
			u.mu.Lock()
			a.telegram, a.tgErr = tgNeed, ""
			u.mu.Unlock()
		}
		u.loginStep(w, r, m, id, msg)
	}
}

// loginStep — следующий шаг попытки входа: отправить подтверждение в Telegram,
// выдать сессию, когда всё пройдено, или показать, чего ещё не хватает.
func (u *UI) loginStep(w http.ResponseWriter, r *http.Request, m mount, id, errMsg string) {
	u.mu.Lock()
	a, ok := u.attempts[id]
	if !ok {
		u.mu.Unlock()
		u.loginPage(w, r, m)
		return
	}
	send := !a.password && a.telegram == tgNeed
	if send && u.telegramBusy(m, id) {
		// Одновременно ждёт ответа не больше одного запроса: кнопкой «Войти» из
		// интернета Telegram не засыпать.
		send, a.telegram, a.tgErr = false, tgError, tr(u.lang(r), "a sign-in request is already waiting in Telegram — answer it or wait 5 minutes")
	}
	if send {
		a.telegram = tgSent
	}
	code := a.code
	u.mu.Unlock()
	if send {
		var err error = M("The Telegram bot is not set up or does not work (Settings → Notifications)")
		if c := u.confirmer(); c != nil {
			where := Word("on this computer")
			if m.remote {
				where = Word("through the device address (remotely)")
			}
			text := M("Sign-in to the gate4.ai Web UI %s, code %s.\nAllow only if you are signing in yourself and this code is on your screen now.", where, code).In(u.lang(r))
			err = c.ConfirmLogin(id, text, func(ok bool) {
				u.mu.Lock()
				defer u.mu.Unlock()
				if a, found := u.attempts[id]; found && a.telegram == tgSent {
					a.telegram = map[bool]string{true: tgOK, false: tgRejected}[ok]
				}
			})
		}
		if err != nil {
			u.mu.Lock()
			a.telegram, a.tgErr = tgError, err.Error()
			u.mu.Unlock()
		}
	}
	u.mu.Lock()
	done := !a.password && (a.telegram == tgNone || a.telegram == tgOK)
	if done {
		delete(u.attempts, id)
	}
	st := *a
	u.mu.Unlock()
	if done {
		http.SetCookie(w, &http.Cookie{Name: m.attemptCookie(), Value: "", Path: m.base + "/", MaxAge: -1, Secure: m.remote})
		u.startSession(w, m, a.telegram == tgOK)
		http.Redirect(w, r, m.base+u.takeNext(w, r, m), http.StatusSeeOther)
		return
	}
	data := map[string]any{"Code": st.code, "Password": st.password, "Telegram": st.telegram, "TelegramError": st.tgErr,
		// Пока ждём нажатия в Telegram, страница обновляется сама (пароль уже введён).
		"Refresh": st.telegram == tgSent}
	u.login401(w, r, m, "login_step.html", view{Path: m.base + "/", CSRF: st.csrf, Error: tr(u.lang(r), errMsg), Data: data})
}

// ---- Настройки → Защита входа ----

// authorizeProtection — менять защиту можно только из сессии и с вводом
// текущего пароля.
func (u *UI) authorizeProtection(r *http.Request, p Protection) (string, error) {
	sid, _, ok := u.session(local, r)
	if !ok {
		return "", errors.New("login required")
	}
	if p.Set() {
		if err := u.verifyPassword(local, p.Password, r.PostForm.Get("current")); err != nil {
			if errors.Is(err, errWrongPassword) {
				return "", errors.New("the current password is wrong")
			}
			return "", M("Current password: %s", err)
		}
	}
	return sid, nil
}

// saveProtection сохраняет защиту и закрывает все остальные сессии и попытки
// входа: дальше в Web UI войти можно только с новой защитой.
func (u *UI) saveProtection(sid string, p Protection) error {
	if !p.Set() {
		return errors.New("the password is required: it cannot be removed, only changed")
	}
	b, _ := json.Marshal(p)
	if err := u.D.State.Set(protectionKey, string(b)); err != nil {
		return err
	}
	u.dropSessions(sid)
	u.mu.Lock()
	u.forgetInitial()
	u.mu.Unlock()
	return nil
}

func (u *UI) setLoginPassword(w http.ResponseWriter, r *http.Request) map[string]any {
	p := u.Protection()
	sid, err := u.authorizeProtection(r, p)
	if err != nil {
		return redirectErr("/settings/login", err)
	}
	f := r.PostForm
	if err := newPassword(f.Get("password"), f.Get("repeat")); err != nil {
		return redirectErr("/settings/login", err)
	}
	p.Password = hashPassword(f.Get("password"))
	if err := u.saveProtection(sid, p); err != nil {
		return redirectErr("/settings/login", err)
	}
	return redirect("/settings/login", "The password is changed. Other sessions are closed.")
}

func (u *UI) setLoginTelegram(w http.ResponseWriter, r *http.Request) map[string]any {
	// Из мастера подключения бота — обратно в мастер.
	back := "/settings/login"
	if r.PostForm.Get("from") == "telegram" {
		back = "/approvals/telegram?for=login#done"
	}
	p := u.Protection()
	sid, err := u.authorizeProtection(r, p)
	if err != nil {
		return redirectErr(back, err)
	}
	on := r.PostForm.Get("on") == "1"
	if on && u.confirmer() == nil {
		return redirectErr(back, errors.New("connect a Telegram bot first: Settings → Notifications → Telegram"))
	}
	p.Telegram = on
	if err := u.saveProtection(sid, p); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, map[bool]string{true: "Signing in to the Web UI is now confirmed in Telegram. Other sessions are closed.",
		false: "Sign-in confirmation in Telegram is off."}[on])
}
