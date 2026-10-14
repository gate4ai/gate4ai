// SPDX-License-Identifier: FSL-1.1-ALv2

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
}

// State — ответ GET /v1/tray/state.
type State struct {
	OK       bool      `json:"ok"`
	Pending  []Pending `json:"pending"`
	Receipts int64     `json:"receipts"`
}

// Tray — клиент трея к сокету владельца.
type Tray struct {
	C *cli.Client

	mu   sync.Mutex
	seen map[string]bool
}

// New — трей поверх клиента владельца (без токена агента).
func New(c *cli.Client) *Tray { return &Tray{C: c, seen: map[string]bool{}} }

// Fetch запрашивает состояние; nil — daemon недоступен.
func (t *Tray) Fetch(ctx context.Context) *State {
	var st State
	if err := t.C.Do(ctx, "GET", "/v1/tray/state", nil, &st); err != nil || !st.OK {
		return nil
	}
	return &st
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

// LoginURL — одноразовая ссылка входа в Web UI.
func (t *Tray) LoginURL(ctx context.Context) (string, error) {
	var res struct {
		URL string `json:"url"`
	}
	err := t.C.Do(ctx, "POST", "/v1/ui/session", nil, &res)
	return res.URL, err
}

// StatusLine — первая строка меню.
func StatusLine(st *State) string {
	if st == nil {
		return "gate4.ai: daemon недоступен"
	}
	return fmt.Sprintf("gate4.ai работает · квитанций: %d", st.Receipts)
}

// Tooltip — подсказка значка.
func Tooltip(st *State) string {
	if st != nil && len(st.Pending) > 0 {
		return fmt.Sprintf("gate4.ai: %d ждут решения", len(st.Pending))
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
	for _, p := range st.Pending {
		b.WriteString("," + p.ID)
	}
	return b.String()
}
