// SPDX-License-Identifier: AGPL-3.0-only

// Package tray — логика трея gate4.ai без GUI: состояние daemon, новые запросы
// подтверждения, решения и вход в Web UI. Трей ходит в тот же сокет владельца
// (на Windows — named pipe) с проверкой собеседника, что и CLI, поэтому логики
// политик и доступа к секретам в нём нет. Меню рисует cmd/gate4ai-tray.
package tray

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gate4ai/gate4ai/internal/front/cli"
)

// Pending — запрос подтверждения.
type Pending struct {
	ID      string `json:"id"`
	Agent   string `json:"agent"`
	Action  string `json:"action"`
	Hash    string `json:"hash"`
	Summary string `json:"summary"`
	Reason  string `json:"reason"`
	// WebOnly — daemon просит решать запрос только в Web UI (там виден полный
	// текст правок); трей проверяет и сам, см. Pending.NeedsWebUI.
	WebOnly bool `json:"web_only,omitempty"`
}

// SelfConfigPrefix — действия самонастройки gate4ai (change_policy и др.);
// то же правило, что approval.WebOnly (трей не тянет пакет approval).
const SelfConfigPrefix = "gate4ai."

// NeedsWebUI — запрос нельзя решать одним нажатием в трее: изменение политики
// или установка своей сборки показываются полностью только в Web UI, а в меню
// трея видны лишь заголовок и причина.
func (p Pending) NeedsWebUI() bool {
	return p.WebOnly || strings.HasPrefix(p.Action, SelfConfigPrefix)
}

// State — ответ GET /v1/tray/state.
type State struct {
	OK       bool      `json:"ok"`
	Pending  []Pending `json:"pending"`
	Receipts int64     `json:"receipts"`
	// Browser — сеансов агентов в браузере, BrowserWaiting — из них ждут человека (handoff).
	Browser        int `json:"browser"`
	BrowserWaiting int `json:"browser_waiting"`
	// UIPassword — сгенерированный пароль Web UI, которым ещё не входили:
	// показывается в меню, чтобы войти после установки.
	UIPassword string `json:"ui_password"`
}

// Tray — клиент трея к сокету владельца.
type Tray struct {
	C *cli.Client

	mu   sync.Mutex
	seen map[string]bool
	err  error // ошибка последнего Fetch
}

// New — трей поверх клиента владельца (без токена агента).
func New(c *cli.Client) *Tray { return &Tray{C: c, seen: map[string]bool{}} }

// Fetch запрашивает состояние; nil — daemon недоступен.
func (t *Tray) Fetch(ctx context.Context) *State {
	var st State
	err := t.C.Do(ctx, "GET", "/v1/tray/state", nil, &st)
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
	if err != nil || !st.OK {
		return nil
	}
	return &st
}

// CanStart — daemon не запущен, и его можно запустить из трея.
func (t *Tray) CanStart() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cli.NotRunning(t.err)
}

// Problem — почему daemon недоступен и что делать (для меню и уведомлений).
func Problem(err error) (title, body string) {
	p := cli.Explain(err, false)
	body = p.Message
	if p.Hint != "" {
		body += ".\n" + strings.ToUpper(p.Hint[:1]) + p.Hint[1:]
	}
	return p.Message, body
}

// Fresh возвращает запросы, о которых трей ещё не уведомлял.
func (t *Tray) Fresh(st *State) []Pending {
	if st == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Pending
	for _, p := range st.Pending {
		if !t.seen[p.ID] {
			t.seen[p.ID] = true
			out = append(out, p)
		}
	}
	return out
}

// Decide разрешает или отклоняет запрос.
func (t *Tray) Decide(ctx context.Context, id string, approve bool) error {
	return t.C.Do(ctx, "POST", "/v1/approvals/"+id+"/decide", map[string]any{"approve": approve, "channel": "tray"}, nil)
}

// URL — адрес Web UI.
func (t *Tray) URL(ctx context.Context) (string, error) {
	return t.URLTo(ctx, "")
}

// URLTo — адрес страницы next (/browser) Web UI: без сессии — вход, затем она.
func (t *Tray) URLTo(ctx context.Context, next string) (string, error) {
	var res struct {
		URL string `json:"url"`
	}
	err := t.C.Do(ctx, "POST", "/v1/ui/url", map[string]string{"next": next}, &res)
	return res.URL, err
}

// PasswordLine — пункт меню со сгенерированным паролем Web UI ("" — паролем
// уже входили или его сменили).
func PasswordLine(st *State) string {
	if st == nil || st.UIPassword == "" {
		return ""
	}
	return fmt.Sprintf(T("password"), st.UIPassword)
}

// BrowserLine — пункт меню о сеансах браузера ("" — сеансов нет).
func BrowserLine(st *State) string {
	switch {
	case st == nil || st.Browser == 0:
		return ""
	case st.BrowserWaiting > 0:
		return T("browser_wait")
	}
	return fmt.Sprintf(T("browser"), st.Browser)
}

// StatusLine — первая строка меню.
func StatusLine(st *State) string {
	if st == nil {
		return T("not_running")
	}
	return fmt.Sprintf(T("running"), st.Receipts)
}

// Tooltip — подсказка значка.
func Tooltip(st *State) string {
	if st != nil && len(st.Pending) > 0 {
		return fmt.Sprintf(T("waiting"), len(st.Pending))
	}
	return "gate4.ai"
}

// Title — заголовок пункта меню запроса.
func (p Pending) Title() string { return fmt.Sprintf("%s → %s %s", p.Agent, p.Action, p.Hash) }

// MenuKey меняется, когда меню нужно пересобрать: daemon стал (не)доступен
// или изменился список запросов.
func MenuKey(st *State) string {
	if st == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("ok")
	fmt.Fprintf(&b, ",b%d/%d,p%d", st.Browser, st.BrowserWaiting, len(st.UIPassword))
	for _, p := range st.Pending {
		b.WriteString("," + p.ID)
	}
	return b.String()
}
