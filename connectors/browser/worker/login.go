// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Ошибки входа по учётке (их текст не содержит значений полей).
var (
	// ErrWrongOrigin — страница не на origin привязки учётки (bind).
	ErrWrongOrigin = errors.New("wrong_origin")
	// ErrFormMismatch — поле формы не найдено или не подходит для ввода.
	ErrFormMismatch = errors.New("form_mismatch")
	// ErrSecretOnPage — на странице введён секрет: читать её нельзя до перехода.
	ErrSecretOnPage = errors.New("secret_on_page")
)

// Статусы входа.
const (
	LoggedIn     = "logged_in"      // адрес начинается с признака успеха
	Submitted    = "submitted"      // форма отправлена, признака успеха нет или он не совпал
	StillOnLogin = "still_on_login" // через таймаут страница всё ещё на origin входа
)

// DefaultLoginTimeout — сколько login ждёт ухода со страницы входа.
const DefaultLoginTimeout = 15 * time.Second

// LoginField — поле формы входа: константа или секрет. Секрет по умолчанию
// вводится только в input[type=password]; AnyType — в любое текстовое поле
// (владелец явно указал type: text).
type LoginField struct {
	Selector string
	Value    string
	Secret   bool
	AnyType  bool
}

// LoginForm — простой вход: поля по порядку, затем отправка (клик по
// SubmitSelector или клавиша SubmitPress в последнем поле).
type LoginForm struct {
	Bind           []string // origin, где разрешён ввод
	Fields         []LoginField
	SubmitSelector string
	SubmitPress    string
	SuccessPrefix  string // признак успеха: адрес после входа начинается с него
	Timeout        time.Duration
}

// LoginResult — итог ввода: адрес после него, origin страницы ввода, статус входа.
type LoginResult struct {
	URL    string
	Origin string
	Status string
}

// checkFieldJS — первый видимый элемент по селектору (как в actJS): найден,
// видим (не прозрачен, в пределах страницы), редактируем, для секрета — input[type=password]. Элемент ищется в
// верхнем документе, поэтому его фрейм — того же origin, что и страница.
const checkFieldJS = `((sel, password) => {
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none' && parseFloat(st.opacity) > 0; };
  let all; try { all = [...document.querySelectorAll(sel)]; } catch (_) { return 'bad selector'; }
  if (!all.length) return 'not found';
  const e = all.find(vis);
  if (!e) return 'not visible';
  // в пределах страницы: поле за краем (left: -9999px) — ловушка автозаполнения
  const r = e.getBoundingClientRect(), d = document.documentElement;
  if (r.right + scrollX <= 0 || r.bottom + scrollY <= 0 || r.left + scrollX >= Math.max(d.scrollWidth, innerWidth) || r.width < 2 || r.height < 2) return 'not visible (outside the page)';
  const text = e.localName === 'input' && !['hidden', 'submit', 'button', 'checkbox', 'radio', 'file', 'image', 'reset', 'range', 'color'].includes(e.type);
  if (!text && e.localName !== 'textarea' && !e.isContentEditable) return 'not a text field';
  if (e.disabled || e.readOnly) return 'disabled';
  if (password && !(e.localName === 'input' && e.type === 'password')) return 'not a password field (input[type=password]); the owner allows other fields with type: text';
  return '';
})`

// markJS помечает документ: на нём введён секрет. Свойство нельзя удалить
// или изменить скриптом страницы, а новый документ (переход) его не имеет.
const markJS = `((n) => {
  try { Object.defineProperty(window, '__gate4aiSecret', {value: n, configurable: false, writable: false, enumerable: false}); } catch (_) {}
  return window.__gate4aiSecret === n;
})`

// fillFieldJS — фокус и выделение содержимого первого видимого элемента (ввод
// затем заменяет его, как fill в act).
const fillFieldJS = `((sel) => {
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none'; };
  const e = [...document.querySelectorAll(sel)].find(vis);
  if (!e) return false;
  e.scrollIntoView({block: 'center'});
  e.focus();
  if (typeof e.select === 'function') e.select();
  else { const r = document.createRange(); r.selectNodeContents(e); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
  return true;
})`

func call(js string, args ...any) string {
	b, _ := json.Marshal(args)
	return js + "(..." + string(b) + ")"
}

// SecretOnPage — на текущем документе введён секрет (до перехода верхнего
// документа). Если проверить нельзя — считается, что введён.
func (x *Executor) SecretOnPage(ctx context.Context) bool {
	if x.sticky {
		return true
	}
	if x.secret == "" {
		return false
	}
	var same bool
	if err := x.Engine.Eval(ctx, "window.__gate4aiSecret === "+itoaStr(x.secret), &same); err != nil || same {
		return true
	}
	x.secret, x.secretSel = "", nil // новый документ: введённого секрета на нём нет
	return false
}

func itoaStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// readable — страницу можно читать (observe, network, extract, снимки).
func (x *Executor) readable(ctx context.Context) error {
	if x.SecretOnPage(ctx) {
		return fmt.Errorf("%w: a secret was entered on this page; reading it is blocked until the page navigates (click, press, wait and fill_secret still work)", ErrSecretOnPage)
	}
	return nil
}

// mark помечает текущий документ перед вводом секрета.
func (x *Executor) mark(ctx context.Context) error {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	n := hex.EncodeToString(raw[:])
	var ok bool
	if err := x.Engine.Eval(ctx, call(markJS, n), &ok); err != nil {
		return err
	}
	if ok {
		x.secret = n
	} else {
		x.sticky = true // страница заняла свойство: режим держится до перехода через Open
	}
	return nil
}

// onOrigin — адрес текущей страницы, если её origin — один из bind.
func (x *Executor) onOrigin(ctx context.Context, bind []string) (string, error) {
	u, err := x.Engine.URL(ctx)
	if err != nil {
		return "", err
	}
	if !domain.OriginIn(u, bind) {
		return u, fmt.Errorf("%w: the page is at %s, not at %s: open the sign-in page first", ErrWrongOrigin, displayURL(u), strings.Join(bind, ", "))
	}
	return u, nil
}

// displayURL — адрес без query и fragment (в них бывают токены).
func displayURL(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

// checkFields — все поля подходят для ввода (иначе ничего не вводится).
func (x *Executor) checkFields(ctx context.Context, fields []LoginField) error {
	for _, f := range fields {
		var reason string
		if err := x.Engine.Eval(ctx, call(checkFieldJS, f.Selector, f.Secret && !f.AnyType), &reason); err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("%w: %s: %s", ErrFormMismatch, f.Selector, reason)
		}
	}
	return nil
}

// fill вводит значение поля: фокус, очистка, Input.insertText.
func (x *Executor) fill(ctx context.Context, f LoginField) error {
	if err := x.pace(ctx, "fill_secret"); err != nil {
		return err
	}
	var ok bool
	if err := x.Engine.Eval(ctx, call(fillFieldJS, f.Selector), &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s: disappeared before input", ErrFormMismatch, f.Selector)
	}
	if err := x.Engine.InsertText(ctx, f.Value); err != nil {
		return errors.New("input failed") // текст ошибки движка может повторять ввод
	}
	x.secretSel = append(x.secretSel, f.Selector)
	return nil
}

// coverJS закрывает чёрными блоками поля с введённым секретом и все поля
// пароля (снимок страницы с секретом); "" — убрать блоки.
const coverJS = `((sels, tag) => {
  document.querySelectorAll('[data-g4-cover="' + tag + '"]').forEach(e => e.remove());
  if (!sels) return 0;
  let n = 0;
  const all = new Set();
  for (const s of sels.concat(['input[type=password]'])) { try { document.querySelectorAll(s).forEach(e => all.add(e)); } catch (_) {} }
  for (const e of all) {
    const r = e.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    const c = document.createElement('div');
    c.setAttribute('data-g4-cover', tag);
    c.style.cssText = 'position:fixed;z-index:2147483647;pointer-events:none;background:#000;left:' + r.left + 'px;top:' + r.top + 'px;width:' + r.width + 'px;height:' + r.height + 'px';
    document.documentElement.appendChild(c); n++;
  }
  return n;
})`

// coveredScreenshot — снимок страницы, где введён секрет: поля закрашены.
func (x *Executor) coveredScreenshot(ctx context.Context, selector string) ([]byte, error) {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	tag := hex.EncodeToString(raw[:])
	var n int
	if err := x.Engine.Eval(ctx, call(coverJS, x.secretSel, tag), &n); err != nil {
		return nil, err
	}
	defer func() { var z int; _ = x.Engine.Eval(context.WithoutCancel(ctx), call(coverJS, nil, tag), &z) }()
	return x.Engine.Screenshot(ctx, selector)
}

// FillSecret вводит одно поле учётки на странице входа (без отправки):
// origin страницы — из bind, поле проверено, документ помечен.
func (x *Executor) FillSecret(ctx context.Context, bind []string, f LoginField) (LoginResult, error) {
	u, err := x.onOrigin(ctx, bind)
	if err != nil {
		return LoginResult{}, err
	}
	if err := x.checkFields(ctx, []LoginField{f}); err != nil {
		return LoginResult{}, err
	}
	// Документ уже помечен (второе поле секрета): повторная метка не встанет
	// (свойство не изменить) и включила бы режим до перехода через Open.
	if f.Secret && !x.SecretOnPage(ctx) {
		if err := x.mark(ctx); err != nil {
			return LoginResult{}, err
		}
	}
	if err := x.fill(ctx, f); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{URL: u, Origin: domain.OriginOf(u)}, nil
}

// CheckSecret — проверки FillSecret без ввода: страница на сайте входа и
// поле подходит (хост проверяет это до вопроса человеку о коде).
func (x *Executor) CheckSecret(ctx context.Context, bind []string, f LoginField) (LoginResult, error) {
	u, err := x.onOrigin(ctx, bind)
	if err != nil {
		return LoginResult{}, err
	}
	if err := x.checkFields(ctx, []LoginField{f}); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{URL: u, Origin: domain.OriginOf(u)}, nil
}

// LoginPoll — шаг ожидания результата отправки формы.
var LoginPoll = 200 * time.Millisecond

// Login — простой вход: проверка origin и всех полей, ввод, отправка,
// ожидание ухода со страницы входа (или признака успеха) до Timeout.
func (x *Executor) Login(ctx context.Context, form LoginForm) (LoginResult, error) {
	if len(form.Fields) == 0 {
		return LoginResult{}, fmt.Errorf("%w: the account has no fields", ErrFormMismatch)
	}
	if form.SubmitSelector == "" && form.SubmitPress == "" {
		return LoginResult{}, fmt.Errorf("%w: the account has no submit", ErrFormMismatch)
	}
	start, err := x.onOrigin(ctx, form.Bind)
	if err != nil {
		return LoginResult{}, err
	}
	if err := x.checkFields(ctx, form.Fields); err != nil {
		return LoginResult{}, err
	}
	for _, f := range form.Fields {
		// Метка прежнего документа (неверный пароль — новая страница входа) не
		// в счёт: SecretOnPage сверяет её с текущим документом.
		if f.Secret && !x.SecretOnPage(ctx) {
			if err := x.mark(ctx); err != nil {
				return LoginResult{}, err
			}
		}
		if err := x.fill(ctx, f); err != nil {
			return LoginResult{}, err
		}
	}
	// Перед отправкой origin проверяется снова: страница могла уйти, пока шёл ввод.
	if _, err := x.onOrigin(ctx, form.Bind); err != nil {
		return LoginResult{}, err
	}
	submit := ActRequest{Selector: form.SubmitSelector, Method: ActClick}
	if form.SubmitSelector == "" {
		submit = ActRequest{Selector: form.Fields[len(form.Fields)-1].Selector, Method: ActPress, Value: form.SubmitPress}
	}
	if err := CheckAct(submit); err != nil {
		return LoginResult{}, fmt.Errorf("%w: submit: %v", ErrFormMismatch, err)
	}
	if _, err := x.Act(ctx, submit); err != nil {
		if errors.Is(err, ErrNotFound) {
			return LoginResult{}, fmt.Errorf("%w: submit %s: not found", ErrFormMismatch, submit.Selector)
		}
		return LoginResult{}, err
	}
	timeout := form.Timeout
	if timeout <= 0 {
		timeout = DefaultLoginTimeout
	}
	deadline := time.Now().Add(timeout)
	u := start
	for {
		if v, err := x.Engine.URL(ctx); err == nil { // во время перехода адрес может быть недоступен
			u = v
		}
		if form.SuccessPrefix != "" && strings.HasPrefix(u, form.SuccessPrefix) {
			break
		}
		if !domain.OriginIn(u, form.Bind) || (form.SuccessPrefix == "" && u != start) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-time.After(LoginPoll):
		case <-ctx.Done():
			return LoginResult{}, ctx.Err()
		}
	}
	res := LoginResult{URL: u, Origin: domain.OriginOf(start), Status: Submitted}
	switch {
	case form.SuccessPrefix != "" && strings.HasPrefix(u, form.SuccessPrefix):
		res.Status = LoggedIn
	case domain.OriginIn(u, form.Bind) && (u == start || form.SuccessPrefix != ""):
		res.Status = StillOnLogin
	}
	if err := x.CheckURL(u); err != nil {
		_ = x.Engine.Navigate(ctx, "about:blank")
		return LoginResult{}, fmt.Errorf("sign-in led off the allowlist: %w", err)
	}
	x.HookNetwork(ctx)
	return res, nil
}
