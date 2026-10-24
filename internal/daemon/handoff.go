// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"time"

	"github.com/gate4ai/gate4ai/internal/pipeline"
)

// HandoffNote — страница браузера передана человеку (browser.handoff): агент
// ждёт, пока человек сделает шаг сам в окне Chrome gate4ai.
type HandoffNote struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Message string    `json:"message"`
	Session string    `json:"session,omitempty"`
	URL     string    `json:"url,omitempty"` // просмотр сеанса
	Created time.Time `json:"created"`
}

// Text — уведомление: кто просит, его слова отдельно и что делать.
func (h HandoffNote) Text() string {
	where := "in the gate4ai Chrome window"
	if h.URL != "" {
		where = "in the browser session: " + h.URL
	}
	return h.Agent + " hands the browser over to you and asks: «" + h.Message +
		"». Do it yourself " + where + "; the agent continues when the page changes. Never give passwords to the agent in chat."
}

// Path — страница Web UI для уведомления (push): просмотр сеанса или список.
func (h HandoffNote) Path() string {
	if h.Session != "" {
		return "/browser/" + h.Session
	}
	return "/approvals#handoffs"
}

// HandedOff — канал, который умеет уведомить о передаче страницы
// (необязательно для Notifier): text — уведомление, path — страница Web UI.
type HandedOff interface {
	HandedOff(text, path string)
}

// handoff уведомляет каналы и держит запись, пока агент ждёт (Pipeline.Handoff).
func (d *Daemon) handoff(h pipeline.Handoff) func() {
	var raw [9]byte
	_, _ = rand.Read(raw[:])
	n := HandoffNote{ID: "h_" + hex.EncodeToString(raw[:]), Agent: h.Agent, Message: h.Message, Session: h.Session, URL: h.URL, Created: time.Now()}
	d.handoffMu.Lock()
	if d.handoffs == nil {
		d.handoffs = map[string]HandoffNote{}
	}
	d.handoffs[n.ID] = n
	d.handoffMu.Unlock()
	d.notifiers.each(func(nt pipeline.Notifier) {
		if c, ok := nt.(HandedOff); ok {
			c.HandedOff(n.Text(), n.Path())
		}
	})
	d.Log.Info("browser handed over to the user", "id", n.ID, "agent", h.Agent)
	return func() {
		d.handoffMu.Lock()
		delete(d.handoffs, n.ID)
		d.handoffMu.Unlock()
	}
}

// Handoffs — передачи страницы, которые ждут человека (старые сверху).
func (d *Daemon) Handoffs() []HandoffNote {
	d.handoffMu.Lock()
	defer d.handoffMu.Unlock()
	out := make([]HandoffNote, 0, len(d.handoffs))
	for _, h := range d.handoffs {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
