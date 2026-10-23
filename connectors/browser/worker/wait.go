// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Wait — чего ждать на странице (open, observe, reload): видимого элемента
// по селектору и/или с текстом, адреса по шаблону.
type Wait struct {
	Selector string
	Text     string
	// URL — шаблон адреса: с * — весь адрес по шаблону (* — любые символы),
	// без * — адрес содержит его.
	URL       string
	TimeoutMS int // 0 — WaitDefault
	// From — адрес, с которого ждём (open — запрошенный): уход с него на
	// страницу входа прерывает ожидание сразу (ErrRedirected).
	From string
}

// Пределы ожидания.
const (
	WaitDefault = 10 * time.Second
	MaxWaitMS   = 60000
)

// ErrRedirected — пока ждали, страница ушла на страницу или сайт входа.
var ErrRedirected = errors.New("redirected")

// PageState — состояние страницы, когда ожидание не удалось: по нему видно,
// грузится ли она ещё, куда ушла и что на ней.
type PageState struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	ReadyState string `json:"ready_state"`
	// Pending — незавершённые запросы страницы (-1 — неизвестно).
	Pending int    `json:"pending_requests"`
	Text    string `json:"text,omitempty"` // начало видимого текста (StateText символов)
}

// StateText — сколько текста страницы показывать в PageState.
const StateText = 300

// stateJS — адрес, заголовок, document.readyState, незавершённые запросы
// перехвата (netHookJS) и начало видимого текста (text — false: без него).
var stateJS = `((n, text) => ({url: location.href, title: document.title, ready_state: document.readyState,
  pending_requests: typeof window.__gate4aiPending === 'number' ? window.__gate4aiPending : -1,
  text: text ? ` + visibleTextJS + `(document.body).replace(/\s+/g, ' ').trim().slice(0, n) : ''}))`

// pendingCounter — движок, который считает незавершённые запросы вкладки сам (CDP Network).
type pendingCounter interface{ Pending() int }

// State — состояние текущей страницы; текста нет, если на ней введён секрет.
func (x *Executor) State(ctx context.Context) PageState {
	var st PageState
	if err := x.Engine.Eval(ctx, call(stateJS, StateText, !x.SecretOnPage(ctx)), &st); err != nil {
		st.URL, _ = x.Engine.URL(ctx)
		st.Pending = -1
	}
	if pc, ok := x.Engine.(pendingCounter); ok {
		st.Pending = pc.Pending()
	}
	st.URL = x.redactURL(st.URL)
	return st
}

// String — состояние одной строкой для текста ошибки.
func (st PageState) String() string {
	b, _ := json.Marshal(st)
	return string(b)
}

// globRe — шаблон адреса wait_for_url в регулярное выражение.
func globRe(p string) *regexp.Regexp {
	if !strings.Contains(p, "*") {
		return regexp.MustCompile(regexp.QuoteMeta(p))
	}
	return regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*") + "$")
}

// loginPathRe — путь (или маршрут SPA во фрагменте) страницы входа.
var loginPathRe = regexp.MustCompile(`(?i)(^|[/#])(login|log-in|logon|signin|sign-in|sign_in|sso|oauth2?|authorize|auth)([/?#.]|$)`)

func loginPath(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	return loginPathRe.MatchString(p.Path) || loginPathRe.MatchString("#"+p.Fragment)
}

// redirectedTo — страница ушла с from на страницу входа: путь входа,
// сайт входа учётки или сайт вне списка.
func (x *Executor) redirectedTo(from, cur string) bool {
	if from == "" || cur == from {
		return false
	}
	if loginPath(cur) && !loginPath(from) {
		return true
	}
	if domain.OriginOf(cur) == domain.OriginOf(from) {
		return false
	}
	return (domain.OriginIn(cur, x.LoginOrigins) && !domain.OriginIn(from, x.LoginOrigins)) || !x.Sites.Allows(cur)
}

// WaitFor ждёт, пока на странице появится видимый элемент по селектору (и/или
// с текстом) и адрес совпадёт с шаблоном, до TimeoutMS (0 — 10 с): SPA
// дорисовывает страницу после загрузки и после кликов. Не дождались —
// ErrNotFound с состоянием страницы; страница ушла на вход — ErrRedirected сразу.
func (x *Executor) WaitFor(ctx context.Context, w Wait) error {
	if w.Selector == "" && w.Text == "" && w.URL == "" {
		return nil
	}
	sel := w.Selector
	if sel == "" && w.Text != "" {
		sel = "body *"
	}
	var urlRe *regexp.Regexp
	if w.URL != "" {
		urlRe = globRe(w.URL)
	}
	timeout := time.Duration(w.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = WaitDefault
	}
	deadline := time.Now().Add(timeout)
	for {
		cur, err := x.Engine.URL(ctx)
		if err != nil {
			return err
		}
		urlOK := urlRe == nil || urlRe.MatchString(cur)
		if (urlRe == nil || !urlOK) && x.redirectedTo(w.From, cur) {
			return fmt.Errorf("%w: while waiting for %s the page went to the sign-in page %s; the page: %s — sign in (browser.login with an account, or browser.handoff to the user) and open the address again",
				ErrRedirected, w.what(), x.redactURL(cur), x.State(ctx))
		}
		found := sel == ""
		if urlOK && !found {
			if found, err = x.present(ctx, sel, w.Text); err != nil {
				return err
			}
		}
		if urlOK && found {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s did not appear in %s; the page: %s — wait longer (timeout_ms up to %d), or read it with observe",
				ErrNotFound, w.what(), timeout, x.State(ctx), MaxWaitMS)
		}
		if err := wait(ctx, ActPoll); err != nil {
			return err
		}
	}
}

// what — чего ждали, для текста ошибки.
func (w Wait) what() string {
	var parts []string
	if w.Selector != "" {
		parts = append(parts, "wait_for_selector "+fmt.Sprintf("%q", w.Selector))
	}
	if w.Text != "" {
		parts = append(parts, "wait_for_text "+fmt.Sprintf("%q", w.Text))
	}
	if w.URL != "" {
		parts = append(parts, "wait_for_url "+fmt.Sprintf("%q", w.URL))
	}
	return strings.Join(parts, " and ")
}

// idleWaiter — движок, который знает о запросах страницы (CDP Network).
type idleWaiter interface {
	// WaitIdle ждёт, пока у страницы не будет запросов quiet, не дольше max;
	// false — не дождался.
	WaitIdle(ctx context.Context, quiet, max time.Duration) bool
}

// Пределы ожидания «сеть успокоилась» после перехода.
var (
	IdleQuiet = 500 * time.Millisecond
	IdleMax   = 5 * time.Second
)

// idleJS — страница загрузилась, и запросов перехвата (netHookJS) нет.
const idleJS = `(document.readyState === 'complete' && !(window.__gate4aiPending > 0))`

// idle — после перехода ждёт до IdleMax, пока SPA догрузит данные: по CDP —
// тишину сети, иначе — document.readyState и запросы перехвата. false —
// страница ещё грузится.
func (x *Executor) idle(ctx context.Context) bool {
	if w, ok := x.Engine.(idleWaiter); ok {
		return w.WaitIdle(ctx, IdleQuiet, IdleMax)
	}
	deadline := time.Now().Add(IdleMax)
	for {
		var done bool
		if err := x.Engine.Eval(ctx, idleJS, &done); err != nil || done {
			return true // движок не ответил — не повод считать страницу недогруженной
		}
		if time.Now().After(deadline) || wait(ctx, ActPoll) != nil {
			return false
		}
	}
}
