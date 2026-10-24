// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/connectors/browser/worker"
)

// Запросы владельца (native.Owner): их шлёт только daemon и только от имени
// владельца — Web UI («Браузер»: список сеансов, просмотр, управление) и
// CLI владельца (gate4ai browser sessions). Агентам они недоступны: у агента
// свои действия под политикой.

// Состояния сеанса для владельца.
const (
	StateWorking = "working" // идёт действие агента
	StateIdle    = "idle"    // агент ничего не делает
	StateWaiting = "waiting" // browser.handoff: агент ждёт человека
	StateUser    = "user"    // сеанс держит владелец (агент ждёт)
)

// SessionInfo — сеанс агента для владельца.
type SessionInfo struct {
	ID       string    `json:"id"`
	Agent    string    `json:"agent"`
	Instance string    `json:"instance,omitempty"`
	Who      string    `json:"who"` // агент и его экземпляр одной строкой
	URL      string    `json:"url,omitempty"`
	Since    time.Time `json:"since"`
	Last     time.Time `json:"last"`
	Closes   time.Time `json:"closes,omitempty"` // закроется при простое (нулевое — нет: его держат или смотрят)
	Running  string    `json:"running,omitempty"`
	State    string    `json:"state"`
	Held     time.Time `json:"held,omitempty"`
	Watched  bool      `json:"watched,omitempty"`
}

// BrowserStatus — ответ sessions: сеансы и режим браузера.
type BrowserStatus struct {
	Sessions []SessionInfo `json:"sessions"`
	Display  string        `json:"display,omitempty"` // режим Chrome, который запустил пакет ("" — не запускал)
	Warning  string        `json:"warning,omitempty"`
}

// SetEmit — события хосту (native.Evented).
func (b *Browser) SetEmit(f func(kind string, data any)) {
	b.mu.Lock()
	b.emit = f
	b.events = nil // новая очередь — с новым получателем
	b.mu.Unlock()
}

// EventQueue — событий ждут отправки хосту; больше — пропускаются (кадры
// и смены состояния, которые хост не успел забрать).
const EventQueue = 64

type event struct {
	kind string
	data any
}

// send ставит событие в очередь: хосту события уходят по порядку из одной
// горутины, а действия сеансов не ждут хост, переставший читать.
func (b *Browser) send(kind string, data any) {
	b.mu.Lock()
	f := b.emit
	if f != nil && b.events == nil {
		b.events = make(chan event, EventQueue)
		go func(ch chan event) {
			for e := range ch {
				f(e.kind, e.data)
			}
		}(b.events)
	}
	ch := b.events
	b.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- event{kind, data}:
	default:
	}
}

// state — состояние сеанса (под b.mu).
func (as *agentSession) state() string {
	switch {
	case !as.held.IsZero():
		return StateUser
	case as.running == "handoff":
		return StateWaiting
	case as.running != "":
		return StateWorking
	}
	return StateIdle
}

// info — сеанс для владельца (под b.mu).
func (as *agentSession) info() SessionInfo {
	in := SessionInfo{ID: as.id, Agent: as.agent, Instance: as.instance, Who: who(as.agent, as.instance), URL: cleanURL(as.shownURL()),
		Since: as.since, Last: as.last, Running: as.running, State: as.state(), Held: as.held, Watched: as.view != nil}
	if as.running == "" && as.held.IsZero() && as.view == nil {
		in.Closes = as.last.Add(AgentIdle)
	}
	return in
}

func (b *Browser) infoOf(as *agentSession) SessionInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return as.info()
}

// stateChanged — зрителям: состояние сеанса поменялось.
func (b *Browser) stateChanged(as *agentSession) { b.send("state", b.infoOf(as)) }

// heldErr — сеанс держит владелец: действия агента ждут.
func (b *Browser) heldErr(as *agentSession) error {
	b.mu.Lock()
	held := as.held
	b.mu.Unlock()
	if held.IsZero() {
		return nil
	}
	return abi.Errorf(CodeHeldByUser, "the user took control of browser session %s in gate4ai at %s and works in it now: "+
		"wait and try again later (browser.observe works again when they give it back); do not open another session to go around them", as.id, clock(held))
}

// ownerArgs — аргументы запросов владельца.
type ownerArgs struct {
	Session  string       `json:"session"`
	On       bool         `json:"on"`
	MaxWidth int          `json:"max_width"`
	Quality  int          `json:"quality"`
	Input    worker.Input `json:"input"`
	Result   string       `json:"result"` // handoff: done | declined
}

// Owner — запрос владельца.
func (b *Browser) Owner(ctx context.Context, op string, raw json.RawMessage) (any, error) {
	var a ownerArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
	}
	if op == "sessions" {
		return b.status(), nil
	}
	b.mu.Lock()
	as := b.agents[a.Session]
	b.mu.Unlock()
	if as == nil {
		return nil, fmt.Errorf("browser session %s is closed or unknown", a.Session)
	}
	switch op {
	case "view":
		if err := b.startView(ctx, as, worker.ViewOptions{MaxWidth: a.MaxWidth, Quality: a.Quality}); err != nil {
			return nil, err
		}
		return b.infoOf(as), nil
	case "unview":
		b.stopView(as)
		return map[string]any{}, nil
	case "control":
		b.mu.Lock()
		if a.On && as.held.IsZero() {
			as.held = time.Now()
		} else if !a.On {
			as.held = time.Time{}
			as.last = time.Now()
			as.timer.Reset(AgentIdle)
		}
		b.mu.Unlock()
		b.stateChanged(as)
		return b.infoOf(as), nil
	case "input":
		return map[string]any{}, b.input(ctx, as, a.Input)
	case "handoff":
		// Ответ человека на handoff из просмотра: готово или не буду.
		if a.Result != worker.HandoffDone && a.Result != worker.HandoffDeclined {
			return nil, fmt.Errorf("handoff result %q: done or declined", a.Result)
		}
		ch := as.stop.Load()
		if ch == nil {
			return nil, errors.New("the agent is not waiting for you in this session")
		}
		select {
		case *ch <- a.Result:
		default:
		}
		return map[string]any{}, nil
	case "close":
		_, err := b.closeSession(as, nil, "closed by the user in gate4ai")
		return map[string]any{"closed": true}, err
	}
	return nil, fmt.Errorf("unknown owner request %q", op)
}

// status — сеансы (старые сверху) и режим браузера.
func (b *Browser) status() BrowserStatus {
	b.mu.Lock()
	out := BrowserStatus{Sessions: []SessionInfo{}}
	for _, as := range b.agents {
		out.Sessions = append(out.Sessions, as.info())
	}
	b.mu.Unlock()
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].Since.Before(out.Sessions[j].Since) })
	out.Display = b.W.DisplayMode()
	if out.Display == worker.DisplayHeadless {
		out.Warning = worker.HeadlessWarning
	}
	return out
}

// sessionView — просмотр сеанса: кадры уходят хосту по одному — последний
// побеждает (медленный канал пропускает кадры, а не копит их).
type sessionView struct {
	v      *worker.View
	latest chan worker.Frame
	done   chan struct{}
}

// startView — просмотр сеанса (уже открытый — тот же).
func (b *Browser) startView(ctx context.Context, as *agentSession, o worker.ViewOptions) error {
	b.mu.Lock()
	if as.view != nil {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	sv := &sessionView{latest: make(chan worker.Frame, 1), done: make(chan struct{})}
	v, err := as.s.View(ctx, o, func(f worker.Frame) {
		select {
		case <-sv.latest: // зритель ещё не забрал прошлый кадр
		default:
		}
		select {
		case sv.latest <- f:
		default:
		}
	})
	if errors.Is(err, worker.ErrNoView) {
		return err
	}
	if err != nil {
		return fmt.Errorf("browser session %s cannot be shown: %w", as.id, err)
	}
	sv.v = v
	b.mu.Lock()
	if as.view != nil || as.closed { // два запроса сразу или сеанс закрылся
		b.mu.Unlock()
		v.Close()
		return nil
	}
	as.view = sv
	b.mu.Unlock()
	go b.frames(as, sv)
	return nil
}

func (b *Browser) frames(as *agentSession, sv *sessionView) {
	for {
		select {
		case <-sv.done:
			return
		case f := <-sv.latest:
			b.mu.Lock()
			state := as.state()
			b.mu.Unlock()
			b.send("frame", map[string]any{"session": as.id, "jpeg": f.JPEG, "width": f.Width, "height": f.Height, "url": cleanURL(f.URL), "state": state})
		}
	}
}

// stopView закрывает просмотр сеанса.
func (b *Browser) stopView(as *agentSession) {
	b.mu.Lock()
	sv := as.view
	as.view = nil
	b.mu.Unlock()
	if sv != nil {
		close(sv.done)
		sv.v.Close()
	}
}

// input — ввод владельца: только когда сеанс у него (control) или агент сам
// передал страницу (handoff).
func (b *Browser) input(ctx context.Context, as *agentSession, in worker.Input) error {
	b.mu.Lock()
	allowed := !as.held.IsZero() || as.running == "handoff"
	sv := as.view
	b.mu.Unlock()
	if !allowed {
		return errors.New("take control of the session first: the agent is working in it")
	}
	if sv == nil {
		return errors.New("the session is not being shown")
	}
	return sv.v.Input(ctx, in)
}
