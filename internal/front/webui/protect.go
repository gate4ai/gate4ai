// SPDX-License-Identifier: AGPL-3.0-only

package webui

// Защита входа — три способа, которые владелец сочетает в Настройках (нужно
// пройти все включённые, хотя бы один включён всегда): одноразовая ссылка с
// устройства (`gate4ai ui`, трей; по умолчанию — только она), пароль (хэш
// argon2id в State Store) и подтверждение входа в личном Telegram-боте — на
// другом устройстве, не на этом компьютере. Без пароля и Telegram ссылка сразу
// даёт сессию, как раньше. С ними ссылка открывает только попытку входа,
// привязанную к браузеру cookie; поэтому агент на этом же компьютере, выполнив
// `gate4ai ui --print`, готовой сессии не получает. Без ссылки вход начинается на
// постоянном адресе Web UI — и на этом компьютере, и удалённо (/ui/ на публичном
// адресе): сохранённая закладка работает и через неделю. От перебора пароля —
// задержка после неверных паролей, у удалённого входа свой счётчик. Настройки защиты хранятся не в
// конфигурации (её может менять агент через gate4ai.change_policy), а в State Store;
// API, который их отдаёт, нет.

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

// Protection — защита входа в Web UI.
type Protection struct {
	NoLink   bool   `json:"no_link,omitempty"`  // на этом компьютере вход без одноразовой ссылки
	Password string `json:"password,omitempty"` // $argon2id$v=19$m=…,t=…,p=…$соль$хэш; "" — без пароля
	Telegram bool   `json:"telegram,omitempty"` // подтверждение входа в Telegram-боте
}

// Extra — кроме ссылки нужен пароль и/или подтверждение в Telegram.
func (p Protection) Extra() bool { return p.Password != "" || p.Telegram }

// Custom — защита отличается от умолчания (только ссылка).
func (p Protection) Custom() bool { return p.NoLink || p.Extra() }

// linkRequired — нужна ли одноразовая ссылка.
func (p Protection) linkRequired(m mount) bool { return !p.NoLink }

// valid — хотя бы один способ входа остаётся.
func (p Protection) valid() error {
	if p.NoLink && !p.Extra() {
		return errors.New("должен остаться хотя бы один способ входа: ссылка, пароль или Telegram")
	}
	return nil
}

// Requires — что нужно для входа, кроме ссылки: password, telegram.
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

// ResetProtection снимает защиту входа (восстановление при забытом пароле или
// недоступном Telegram) и закрывает все сессии.
func (u *UI) ResetProtection() error {
	if err := u.D.State.Set(protectionKey, "{}"); err != nil {
		return err
	}
	u.dropSessions("", false)
	return nil
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

// attempt — попытка входа по одноразовой ссылке при включённой защите.
type attempt struct {
	remote   bool
	expires  time.Time
	csrf     string
	code     string // код попытки: на странице и в сообщении Telegram
	password bool   // нужен пароль и он ещё не введён
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

// loginPage — страница «нужен вход»: без попытки входа. Если ссылка не нужна —
// начало входа (пароль и/или кнопка «Войти»), иначе — как получить ссылку.
func (u *UI) loginPage(w http.ResponseWriter, m mount) {
	p := u.Protection()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(401)
	if !p.linkRequired(m) {
		_ = tpl.ExecuteTemplate(w, "login_step.html", view{Base: m.base, Remote: m.remote,
			Data: map[string]any{"Start": true, "Password": p.Password != "", "Telegram": map[bool]string{true: tgNeed}[p.Telegram]}})
		return
	}
	_ = tpl.ExecuteTemplate(w, "login.html", view{Base: m.base, Remote: m.remote})
}

func loginCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return fmt.Sprintf("%06d", n.Int64())
}

// newAttempt — попытка входа после одноразовой ссылки (вызывать под u.mu).
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
	a := &attempt{remote: m.remote, expires: now.Add(attemptTTL), csrf: random(), code: loginCode(), password: p.Password != ""}
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

// startSession выдаёт сессию; protected — вход прошёл защиту.
func (u *UI) startSession(w http.ResponseWriter, m mount, protected bool) {
	sid := random()
	u.mu.Lock()
	u.sessions[sid] = session{csrf: random(), expires: u.Now().Add(12 * time.Hour), remote: m.remote, protected: protected}
	u.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: m.cookie(), Value: sid, Path: m.base + "/", HttpOnly: true, Secure: m.remote,
		SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
}

// dropSessions закрывает все сессии, кроме keep, все попытки входа и
// неиспользованные ссылки; keep помечается protected.
func (u *UI) dropSessions(keep string, protected bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for sid, s := range u.sessions {
		if sid != keep {
			delete(u.sessions, sid)
		} else {
			s.protected = protected
			u.sessions[sid] = s
		}
	}
	u.logins = map[string]login{}
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
		l.failures = 0
		return
	}
	l.failures++
	if n := l.failures - freeFailures; n >= 0 {
		d := time.Second << min(n, 10)
		l.until = u.Now().Add(min(d, 15*time.Minute))
	}
}

// verifyPassword — пароль с учётом задержки после неверных попыток.
func (u *UI) verifyPassword(m mount, enc, pw string) error {
	u.mu.Lock()
	wait, ok := u.passwordAllowed(m)
	u.mu.Unlock()
	if !ok {
		return fmt.Errorf("слишком много неверных паролей: подождите %d с", int(wait.Seconds())+1)
	}
	good := checkPassword(enc, pw)
	u.mu.Lock()
	u.passwordResult(m, good)
	u.mu.Unlock()
	if !good {
		return errors.New("неверный пароль")
	}
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

// loginPost — ввод пароля и повторная отправка в Telegram на странице попытки входа.
func (u *UI) loginPost(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.originOK(m, r) {
			http.Error(w, "bad origin", 403)
			return
		}
		_ = r.ParseForm()
		u.mu.Lock()
		id, a := u.attemptOf(m, r)
		// Без ссылки попытка начинается кнопкой «Войти» на постоянном адресе (POST
		// с проверкой Origin: чужой сайт попытку не начнёт и Telegram не потревожит).
		// Повторное «Войти» начинает попытку заново (неверные пароли считает общая задержка).
		if p := u.Protection(); r.PostForm.Get("start") == "1" && !p.linkRequired(m) {
			delete(u.attempts, id)
			id = u.newAttempt(m, p)
			a = u.attempts[id]
			u.setAttemptCookie(w, m, id)
			r.PostForm.Set("csrf", a.csrf)
		}
		if a == nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(a.csrf)) != 1 {
			u.mu.Unlock()
			u.loginPage(w, m)
			return
		}
		needPw, tg := a.password, a.telegram
		u.mu.Unlock()
		msg := ""
		if needPw {
			if err := u.verifyPassword(m, u.Protection().Password, r.PostForm.Get("password")); err != nil {
				msg = err.Error()
				u.mu.Lock()
				if a.tries++; a.tries >= maxTries {
					delete(u.attempts, id)
					u.mu.Unlock()
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(401)
					_ = tpl.ExecuteTemplate(w, "login.html", view{Base: m.base, Remote: m.remote, Error: "Слишком много неверных паролей — начните вход заново."})
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
		u.loginPage(w, m)
		return
	}
	send := !a.password && a.telegram == tgNeed
	if send && u.telegramBusy(m, id) {
		// Одновременно ждёт ответа не больше одного запроса: кнопкой «Войти» из
		// интернета Telegram не засыпать.
		send, a.telegram, a.tgErr = false, tgError, "запрос на вход уже ждёт ответа в Telegram — ответьте на него или подождите 5 минут"
	}
	if send {
		a.telegram = tgSent
	}
	code := a.code
	u.mu.Unlock()
	if send {
		err := errors.New("Telegram-бот не настроен или не работает (Подтверждения → Каналы)")
		if c := u.confirmer(); c != nil {
			where := "на этом компьютере"
			if m.remote {
				where = "через адрес устройства (удалённо)"
			}
			text := fmt.Sprintf("🔐 Вход в Web UI gate4.ai %s, код %s.\nРазрешайте, только если вы входите сами и этот код сейчас у вас на экране.", where, code)
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
		u.startSession(w, m, true)
		http.Redirect(w, r, m.base+"/", http.StatusSeeOther)
		return
	}
	data := map[string]any{"Code": st.code, "Password": st.password, "Telegram": st.telegram, "TelegramError": st.tgErr,
		// Пока ждём нажатия в Telegram, страница обновляется сама (пароль уже введён).
		"Refresh": st.telegram == tgSent}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(401)
	_ = tpl.ExecuteTemplate(w, "login_step.html", view{Base: m.base, Remote: m.remote, CSRF: st.csrf, Error: errMsg, Data: data})
}

// ---- Настройки → Защита входа ----

// authorizeProtection — менять защиту можно только из сессии, открытой под
// текущей защитой; если задан пароль — ещё и с вводом текущего пароля.
func (u *UI) authorizeProtection(r *http.Request, p Protection) (string, error) {
	sid, s, ok := u.session(local, r)
	if !ok {
		return "", errors.New("login required")
	}
	if p.Custom() && !s.protected {
		return "", errors.New("эта сессия открыта без защиты входа: войдите заново (выход → gate4ai ui), чтобы изменить защиту")
	}
	if p.Password != "" {
		if err := u.verifyPassword(local, p.Password, r.PostForm.Get("current")); err != nil {
			return "", fmt.Errorf("текущий пароль: %v", err)
		}
	}
	return sid, nil
}

// saveProtection сохраняет защиту и закрывает все остальные сессии и попытки
// входа: дальше в Web UI войти можно только с новой защитой.
func (u *UI) saveProtection(sid string, p Protection) error {
	if err := p.valid(); err != nil {
		return err
	}
	b, _ := json.Marshal(p)
	if err := u.D.State.Set(protectionKey, string(b)); err != nil {
		return err
	}
	u.dropSessions(sid, p.Custom())
	return nil
}

func (u *UI) setLoginPassword(w http.ResponseWriter, r *http.Request) map[string]any {
	p := u.Protection()
	sid, err := u.authorizeProtection(r, p)
	if err != nil {
		return redirectErr("/settings#login", err)
	}
	f := r.PostForm
	flash := "Пароль снят."
	if f.Get("remove") == "1" {
		p.Password = ""
	} else {
		pw := f.Get("password")
		switch {
		case len([]rune(pw)) < 8:
			return redirectErr("/settings#login", errors.New("пароль — не короче 8 символов"))
		case pw != f.Get("repeat"):
			return redirectErr("/settings#login", errors.New("пароли не совпадают"))
		}
		flash = "Пароль задан: вход в Web UI теперь требует пароль. Остальные сессии закрыты."
		p.Password = hashPassword(pw)
	}
	if err := u.saveProtection(sid, p); err != nil {
		return redirectErr("/settings#login", err)
	}
	return redirect("/settings#login", flash)
}

func (u *UI) setLoginTelegram(w http.ResponseWriter, r *http.Request) map[string]any {
	p := u.Protection()
	sid, err := u.authorizeProtection(r, p)
	if err != nil {
		return redirectErr("/settings#login", err)
	}
	on := r.PostForm.Get("on") == "1"
	if on && u.confirmer() == nil {
		return redirectErr("/settings#login", errors.New("сначала подключите Telegram-бот: Подтверждения → Каналы → Telegram"))
	}
	p.Telegram = on
	if err := u.saveProtection(sid, p); err != nil {
		return redirectErr("/settings#login", err)
	}
	return redirect("/settings#login", map[bool]string{true: "Вход в Web UI теперь подтверждается в Telegram. Остальные сессии закрыты.",
		false: "Подтверждение входа в Telegram выключено."}[on])
}

func (u *UI) setLoginLink(w http.ResponseWriter, r *http.Request) map[string]any {
	p := u.Protection()
	sid, err := u.authorizeProtection(r, p)
	if err != nil {
		return redirectErr("/settings#login", err)
	}
	p.NoLink = r.PostForm.Get("on") != "1"
	if err := u.saveProtection(sid, p); err != nil {
		return redirectErr("/settings#login", err)
	}
	return redirect("/settings#login", map[bool]string{true: "Вход на этом компьютере — по одноразовой ссылке (gate4ai ui, трей).",
		false: "Одноразовая ссылка больше не нужна: входите на " + "http://" + u.Addr + "/ — с паролем и/или подтверждением в Telegram. Остальные сессии закрыты."}[!p.NoLink])
}
