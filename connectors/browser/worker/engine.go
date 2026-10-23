// SPDX-License-Identifier: AGPL-3.0-only

// Package worker — браузерный воркер нативного коннектора browser:
// выделенный профиль настоящего Chrome по CDP, движок Stagehand Go SDK v4
// (или прямой CDP). Сайты проверяются по списку до и после редиректов. Все
// переходы, клики и ввод идут через Executor — единую точку исполнения.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Engine — низкоуровневый движок браузера.
type Engine interface {
	Navigate(ctx context.Context, url string) error
	URL(ctx context.Context) (string, error)
	Title(ctx context.Context) (string, error)
	Text(ctx context.Context) (string, error)
	// Screenshot — снимок элемента по CSS-селектору (не всей страницы).
	Screenshot(ctx context.Context, selector string) ([]byte, error)
	// Extract — извлечение данных по инструкции (LLM внутри движка; только чтение).
	Extract(ctx context.Context, instruction string) (any, error)
	// Eval — выражение JS на странице, результат по значению в out.
	Eval(ctx context.Context, expr string, out any) error
	// InsertText — ввод текста в элемент в фокусе (как с клавиатуры).
	InsertText(ctx context.Context, text string) error
	// Key — нажатие клавиши или сочетания в фокусе: запись Key.String
	// ("Enter", "Control+Enter", "Shift+Tab").
	Key(ctx context.Context, key string) error
	Close() error
}

// ErrDomain — сайт не разрешён списком сайтов (настройки браузера или params вызывающего).
var ErrDomain = errors.New("site is not allowed by the browser's site list")

// Executor — единая точка исполнения действий в браузере.
type Executor struct {
	Engine Engine
	Sites  domain.Sites
	// Login — origin страниц входа учёток, доступных вызывающему (bind): на
	// них страница может быть и вне Sites (редирект на вход).
	LoginOrigins []string
	// RedactParams — параметры адресов и поля JSON, значения которых network
	// не показывает (настройка redact_params: личные идентификаторы сайта).
	RedactParams []string

	// StillLoading — после последнего Open страница не догрузилась за
	// IdleMax (запросы ещё идут или document.readyState не complete).
	StillLoading bool

	secret    string   // метка документа, на котором введён секрет (markJS)
	sticky    bool     // секрет введён, метку поставить не удалось: до перехода через Open
	secretSel []string // поля, куда введены секреты (закрашиваются на снимках)
}

// wait — пауза опроса d; раньше — ошибка, если ctx отменён.
func wait(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// CheckURL — URL разрешён списком сайтов коннектора (дополнительно к политике).
func (x *Executor) CheckURL(u string) error {
	if !x.Sites.Allows(u) && !domain.OriginIn(u, x.LoginOrigins) {
		if domain.HostOf(u) == "" {
			return fmt.Errorf("%w: the page is blank — it was closed after leaving the allowed sites; open an allowed address", ErrDomain)
		}
		return fmt.Errorf("%w: %s", ErrDomain, domain.HostOf(u))
	}
	return nil
}

// SettleTimeout — сколько ждать, что страница вернётся на разрешённый сайт:
// тихий вход через SSO на секунду уводит её на сайт входа и обратно, и
// проверка в этот момент оборвала бы вход, который удался бы.
var SettleTimeout = 5 * time.Second

// settledURL — текущий адрес; если он вне списка, ждёт до SettleTimeout, не
// вернётся ли страница на разрешённый сайт.
func (x *Executor) settledURL(ctx context.Context) (string, error) {
	deadline := time.Now().Add(SettleTimeout)
	for {
		u, err := x.Engine.URL(ctx)
		if err != nil {
			return "", err
		}
		if err := x.CheckURL(u); err == nil || time.Now().After(deadline) {
			return u, err
		}
		if err := wait(ctx, ActPoll); err != nil {
			return "", err
		}
	}
}

// Open переходит по URL; после редиректов итоговый адрес тоже проверяется,
// иначе страница закрывается переходом на about:blank.
func (x *Executor) Open(ctx context.Context, u string) (string, error) {
	if err := x.CheckURL(u); err != nil {
		return "", err
	}
	if err := x.Engine.Navigate(ctx, u); err != nil {
		return "", err
	}
	x.sticky, x.secretSel = false, nil // новый документ: введённого секрета на нём нет
	final, err := x.settledURL(ctx)
	if err != nil && !errors.Is(err, ErrDomain) {
		return "", err
	}
	if err != nil {
		_ = x.Engine.Navigate(ctx, "about:blank")
		return "", fmt.Errorf("redirected off the allowlist: %w", err)
	}
	x.HookNetwork(ctx)
	x.StillLoading = !x.idle(ctx)
	return final, nil
}

// Observe — адрес, заголовок и текст текущей страницы (max символов, 0 — без предела).
func (x *Executor) Observe(ctx context.Context, max int) (url, title, text string, truncated bool, err error) {
	if url, err = x.settledURL(ctx); err != nil {
		return
	}
	if err = x.readable(ctx); err != nil {
		return
	}
	title, _ = x.Engine.Title(ctx)
	if text, err = x.Engine.Text(ctx); err != nil {
		return
	}
	text = strings.TrimSpace(text)
	if r := []rune(text); max > 0 && len(r) > max {
		text, truncated = string(r[:max]), true
	}
	return
}

// Screenshot — снимок элемента текущей страницы; если на ней введён секрет —
// с закрашенными полями секрета и пароля.
func (x *Executor) Screenshot(ctx context.Context, selector string) ([]byte, error) {
	if x.SecretOnPage(ctx) {
		return x.coveredScreenshot(ctx, selector)
	}
	return x.Engine.Screenshot(ctx, selector)
}

// Extract — извлечение по инструкции с текущей страницы (Act с естественным
// языком для изменений не используется: в нём нет точки перехвата политикой).
func (x *Executor) Extract(ctx context.Context, instruction string) (any, error) {
	if err := x.readable(ctx); err != nil {
		return nil, err
	}
	return x.Engine.Extract(ctx, instruction)
}

// ActPoll — шаг ожидания элемента в Act.
var ActPoll = 200 * time.Millisecond

// actJS ищет видимый элемент по селектору, чей текст (подписи, placeholder,
// aria-label, value) содержит нужный текст без учёта регистра и «ё»; из
// нескольких берёт элемент с самой короткой подписью. Готовит его к методу
// (click — клик, fill — фокус и выделение содержимого, press — фокус) и
// возвращает его текст; append — фокус и курсор в конец значения, paste — как fill.
//
// Значение поля пароля не читается никогда: ни в подписи (по ней подбирают
// текст и expect_text / wait_for_text), ни в ответе. Пока на странице введён
// секрет (secret), не читаются значения никаких полей ввода (у кнопок value —
// их надпись), текст редактируемых элементов (contenteditable) и тех, что их
// содержат, а ответ — пустой: секрет мог попасть в поле type: text.
var actJS = `((sel, text, method, secret) => {
  const vt = ` + visibleTextJS + `;
  const norm = s => (s || '').replace(/\s+/g, ' ').trim().toLowerCase().replace(/ё/g, 'е');
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none'; };
  const pwd = e => e.localName === 'input' && e.type === 'password';
  const btn = e => e.localName === 'input' && ['submit', 'button', 'reset'].includes(e.type);
  const val = e => typeof e.value !== 'string' || pwd(e) || (secret && !btn(e)) ? '' : e.value;
  const txt = e => secret && (e.isContentEditable || e.querySelector('[contenteditable]:not([contenteditable=false])')) ? '' : vt(e);
  const label = e => [txt(e), val(e), e.placeholder, e.getAttribute('aria-label'), e.getAttribute('title'),
    e.labels ? [...e.labels].map(l => txt(l)).join(' ') : ''].filter(Boolean).join(' ');
  const want = norm(text);
  // из подходящих — с самой короткой подписью: кнопка, а не её контейнер
  const e = [...document.querySelectorAll(sel)].filter(e => vis(e) && (!want || norm(label(e)).includes(want)))
    .sort((a, b) => norm(label(a)).length - norm(label(b)).length)[0];
  if (!e) return null;
  e.scrollIntoView({block: 'center'});
  if (method === 'click') e.click();
  if (['fill', 'paste', 'append', 'press'].includes(method)) {
    e.focus();
    const edit = typeof e.setSelectionRange === 'function' && typeof e.value === 'string';
    if (method === 'fill' || method === 'paste') {
      if (typeof e.select === 'function') e.select();
      else { const r = document.createRange(); r.selectNodeContents(e); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
    }
    if (method === 'append') {
      if (edit) { try { e.setSelectionRange(e.value.length, e.value.length); } catch (_) {} }
      else { const r = document.createRange(); r.selectNodeContents(e); r.collapse(false); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
    }
  }
  return {text: secret ? '' : (txt(e) || val(e) || '').slice(0, 2000)};
})`

// actExpr — вызов actJS; secret — на странице введён секрет (SecretOnPage).
func (x *Executor) actExpr(ctx context.Context, sel, text, method string) (expr string, secret bool) {
	secret = x.SecretOnPage(ctx)
	return call(actJS, sel, text, method, secret), secret
}

// Act — click / fill / press / wait по селектору: ждёт элемент (до TimeoutMS),
// выполняет метод; итоговый адрес проверяется по списку доменов.
func (x *Executor) Act(ctx context.Context, r ActRequest) (ActResponse, error) {
	before, err := x.settledURL(ctx)
	if err != nil {
		return ActResponse{}, err
	}
	if writes(r.Method) {
		if err := x.readable(ctx); err != nil {
			return ActResponse{}, err
		}
	}
	timeout := time.Duration(r.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	expr, secret := x.actExpr(ctx, r.Selector, r.Text, r.Method)
	deadline := time.Now().Add(timeout)
	var found *struct {
		Text string `json:"text"`
	}
	for {
		if err := x.Engine.Eval(ctx, expr, &found); err != nil {
			return ActResponse{}, err
		}
		if found != nil {
			break
		}
		if time.Now().After(deadline) {
			what := r.Selector
			if r.Text != "" {
				what += " with text " + fmt.Sprintf("%q", r.Text)
			}
			return ActResponse{}, fmt.Errorf("%w: %s", ErrNotFound, what)
		}
		if err := wait(ctx, ActPoll); err != nil {
			return ActResponse{}, err
		}
	}
	var value *FieldValue
	switch {
	case writes(r.Method):
		if err := x.write(ctx, r.Method, r.Value); err != nil {
			return ActResponse{}, err
		}
		if value, err = x.fieldValue(ctx, secret); err != nil {
			return ActResponse{}, err
		}
	case r.Method == ActPress:
		if err := x.press(ctx, r.Value); err != nil {
			return ActResponse{}, err
		}
	}
	if err := x.expect(ctx, r, before); err != nil {
		return ActResponse{}, err
	}
	u, err := x.settledURL(ctx)
	if err != nil && !errors.Is(err, ErrDomain) {
		return ActResponse{}, err
	}
	if err != nil {
		_ = x.Engine.Navigate(ctx, "about:blank")
		return ActResponse{}, fmt.Errorf("action led off the allowlist: %w", err)
	}
	x.HookNetwork(ctx) // действие могло перейти на новую страницу
	if secret || x.SecretOnPage(ctx) {
		// Текст элемента на странице с секретом не отдаётся и здесь (не
		// только в actJS): это могло быть значение поля.
		found.Text = ""
	}
	return ActResponse{URL: u, Text: found.Text, Value: value}, nil
}

// TypedMax — до стольких символов fill / append вводятся как с клавиатуры
// (Engine.InsertText); длиннее — командой вставки страницы одним шагом
// (insertJS): Stagehand печатает посимвольно, и 24 КБ шли бы минуты.
const TypedMax = 256

// insertJS вставляет текст в элемент в фокусе вместо выделения: командой
// insertText (событие input, отмена Ctrl+Z), иначе — setRangeText у полей.
var insertJS = `((v) => {
  const e = document.activeElement;
  if (!e) return false;
  if (document.execCommand('insertText', false, v)) return true;
  if (typeof e.setRangeText === 'function') {
    e.setRangeText(v, e.selectionStart, e.selectionEnd, 'end');
    e.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'insertText', data: v}));
    return true;
  }
  return false;
})`

// pasteJS — вставка как из буфера обмена: событие paste с text/plain (без
// системного буфера); если страница его не обработала — insertText.
var pasteJS = `((v) => {
  const e = document.activeElement;
  if (!e) return false;
  const dt = new DataTransfer();
  dt.setData('text/plain', v);
  const ev = new ClipboardEvent('paste', {clipboardData: dt, bubbles: true, cancelable: true});
  if (!e.dispatchEvent(ev)) return true;
  return document.execCommand('insertText', false, v) || (typeof e.setRangeText === 'function' &&
    (e.setRangeText(v, e.selectionStart, e.selectionEnd, 'end'), e.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'insertFromPaste'})), true));
})`

// write — ввод Value в элемент в фокусе (actJS выделил содержимое или
// поставил курсор в конец): fill с пустым value стирает выделенное.
func (x *Executor) write(ctx context.Context, method, v string) error {
	switch {
	case method == ActPaste:
		return x.evalTrue(ctx, call(pasteJS, v), "paste")
	case v == "":
		return x.Engine.Key(ctx, "Backspace")
	case utf8.RuneCountInString(v) <= TypedMax:
		return x.Engine.InsertText(ctx, v)
	}
	return x.evalTrue(ctx, call(insertJS, v), "insert text")
}

// evalTrue — выражение страницы, которое должно вернуть true.
func (x *Executor) evalTrue(ctx context.Context, expr, what string) error {
	var ok bool
	if err := x.Engine.Eval(ctx, expr, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: the element did not accept the text (not an editable field?)", what)
	}
	return nil
}

// commandJS — команда редактирования (selectAll, undo, redo) в элементе в фокусе.
var commandJS = `((c) => document.execCommand(c))`

// press — клавиша или сочетание; сочетания редактирования — командой страницы.
func (x *Executor) press(ctx context.Context, name string) error {
	k, err := ParseKey(name)
	if err != nil {
		return err
	}
	if c := k.command(); c != "" {
		var ok bool
		return x.Engine.Eval(ctx, call(commandJS, c), &ok)
	}
	return x.Engine.Key(ctx, k.String())
}

// valueJS — итоговое значение элемента в фокусе: длина, начало и конец.
// Пароль и любое поле страницы с введённым секретом — hidden, без значения и длины.
var valueJS = `((start, end, secret) => {
  const e = document.activeElement;
  if (!e || e === document.body) return null;
  if ((e.localName === 'input' && e.type === 'password') || secret) return {length: 0, start: '', hidden: true};
  const v = typeof e.value === 'string' ? e.value : (e.isContentEditable ? e.innerText : '');
  const cs = [...v];
  const out = {length: cs.length, start: cs.slice(0, start).join('')};
  if (cs.length > start) out.end = cs.slice(Math.max(start, cs.length - end)).join('');
  return out;
})`

// fieldValue — что записалось в поле после fill / append / paste.
func (x *Executor) fieldValue(ctx context.Context, secret bool) (*FieldValue, error) {
	var v *FieldValue
	if err := x.Engine.Eval(ctx, call(valueJS, ValueStart, ValueEnd, secret || x.SecretOnPage(ctx)), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// ErrNoEffect — у действия не было ожидаемого результата (клик не открыл то,
// что должен был): ok был бы ложью.
var ErrNoEffect = errors.New("the action had no expected effect")

// expect — ждёт результат действия, если он задан: элемент (и его текст) или
// смену адреса, до ExpectMS (0 — 5 с).
func (x *Executor) expect(ctx context.Context, r ActRequest, before string) error {
	if r.ExpectSelector == "" && r.ExpectText == "" && !r.ExpectURLChange {
		return nil
	}
	timeout := time.Duration(r.ExpectMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		ok := true
		if r.ExpectURLChange {
			u, err := x.Engine.URL(ctx)
			if err != nil {
				return err
			}
			ok = u != before
		}
		if ok && (r.ExpectSelector != "" || r.ExpectText != "") {
			sel := r.ExpectSelector
			if sel == "" {
				sel = "body *"
			}
			found, err := x.present(ctx, sel, r.ExpectText)
			if err != nil {
				return err
			}
			ok = found
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			what := []string{}
			if r.ExpectURLChange {
				what = append(what, "the address to change")
			}
			if r.ExpectSelector != "" || r.ExpectText != "" {
				what = append(what, fmt.Sprintf("%s %q", r.ExpectSelector, r.ExpectText))
			}
			return fmt.Errorf("%w: waited %s for %s", ErrNoEffect, timeout, strings.Join(what, " and "))
		}
		if err := wait(ctx, ActPoll); err != nil {
			return err
		}
	}
}

// present — на странице есть видимый элемент по селектору с текстом.
func (x *Executor) present(ctx context.Context, sel, text string) (bool, error) {
	expr, _ := x.actExpr(ctx, sel, text, ActWait)
	var found *struct {
		Text string `json:"text"`
	}
	if err := x.Engine.Eval(ctx, expr, &found); err != nil {
		return false, err
	}
	return found != nil, nil
}
