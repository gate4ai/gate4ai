// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/pipeline"
)

// Сеансы браузера для владельца: список, просмотр вживую, управление и
// закрытие — запросы владельца встроенному браузеру (wasm.Builtin.Owner) и его
// события (кадры). Агентам это недоступно: у них свои действия под политикой.

// BrowserSession — сеанс агента в браузере (как его видит пакет).
type BrowserSession struct {
	ID       string    `json:"id"`
	Agent    string    `json:"agent"`
	Instance string    `json:"instance,omitempty"`
	Who      string    `json:"who"`
	URL      string    `json:"url,omitempty"`
	Since    time.Time `json:"since"`
	Last     time.Time `json:"last"`
	Closes   time.Time `json:"closes,omitempty"`
	Running  string    `json:"running,omitempty"`
	State    string    `json:"state"` // working | idle | waiting (handoff) | user (держит владелец)
	Held     time.Time `json:"held,omitempty"`
	Watched  bool      `json:"watched,omitempty"`
	ViewURL  string    `json:"view_url,omitempty"`
}

// BrowserStatus — сеансы и режим браузера.
type BrowserStatus struct {
	Sessions []BrowserSession `json:"sessions"`
	Display  string           `json:"display,omitempty"` // virtual | headless | host ("" — Chrome не запущен)
	Warning  string           `json:"warning,omitempty"`
	Running  bool             `json:"running"` // браузер отвечает (встроен: всегда, когда есть)
	Missing  bool             `json:"missing,omitempty"`
}

// BrowserEvent — событие просмотра: кадр, смена состояния или закрытие сеанса.
type BrowserEvent struct {
	Kind    string          // frame | state | closed
	JPEG    []byte          // frame
	Width   float64         // frame: CSS px области страницы
	Height  float64         // frame
	URL     string          // frame
	State   string          // frame, state
	Session *BrowserSession // state
	Reason  string          // closed
}

// browserOwner — браузер с запросами владельца.
type browserOwner interface {
	Owner(ctx context.Context, op string, args, out any) error
	SetEventSink(f func(kind string, data json.RawMessage))
}

// ErrNoBrowser — браузера в этой сборке нет (daemon --no-browser в тестах).
var ErrNoBrowser = errors.New("the browser is not available")

func (d *Daemon) browser() (browserOwner, error) {
	c, ok := d.Registry.Get(pipeline.BrowserConnector)
	if !ok {
		return nil, ErrNoBrowser
	}
	o, ok := c.(browserOwner)
	if !ok {
		return nil, ErrNoBrowser
	}
	return o, nil
}

// SetLocalUI — адрес Web UI на этом компьютере (http://127.0.0.1:<port>):
// ссылки на сеансы, когда публичного адреса нет.
func (d *Daemon) SetLocalUI(u string) {
	d.mu.Lock()
	d.localUI = u
	d.mu.Unlock()
}

// BrowserViewURL — ссылка на просмотр сеанса: на публичном адресе устройства
// (/ui/browser/<id>), без него — в Web UI этого компьютера. Сама ссылка
// доступа не даёт: страница требует вход владельца.
func (d *Daemon) BrowserViewURL(session string) string {
	id := url.PathEscape(session)
	if pub := d.PublicURL(); pub != "" {
		return pub + "/ui/browser/" + id
	}
	d.mu.Lock()
	local := d.localUI
	d.mu.Unlock()
	if local == "" {
		local = "http://127.0.0.1:7431"
	}
	return local + "/browser/" + id
}

// BrowserStatus — сеансы агентов в браузере.
func (d *Daemon) BrowserStatus(ctx context.Context) (BrowserStatus, error) {
	b, err := d.browser()
	if err != nil {
		return BrowserStatus{Sessions: []BrowserSession{}, Missing: true}, nil
	}
	var st BrowserStatus
	if err := b.Owner(ctx, "sessions", nil, &st); err != nil {
		return BrowserStatus{Sessions: []BrowserSession{}}, err
	}
	st.Running = true
	for i := range st.Sessions {
		st.Sessions[i].ViewURL = d.BrowserViewURL(st.Sessions[i].ID)
	}
	return st, nil
}

// BrowserSession — один сеанс (nil — нет такого).
func (d *Daemon) BrowserSession(ctx context.Context, id string) (*BrowserSession, error) {
	st, err := d.BrowserStatus(ctx)
	if err != nil {
		return nil, err
	}
	for i := range st.Sessions {
		if st.Sessions[i].ID == id {
			return &st.Sessions[i], nil
		}
	}
	return nil, nil
}

// browserViews — зрители сеансов: кадры браузера раздаются всем зрителям
// сеанса; первый зритель включает просмотр, последний — выключает.
type browserViews struct {
	mu      sync.Mutex
	viewers map[string]map[*browserViewer]bool
	latest  map[string]BrowserEvent // последний кадр сеанса: новому зрителю — сразу
}

type browserViewer struct {
	ch   chan BrowserEvent
	once sync.Once
}

// ViewerQueue — событий в очереди зрителя; кадры сверх неё пропускаются.
const ViewerQueue = 4

func (v *browserViewer) push(e BrowserEvent) {
	select {
	case v.ch <- e:
		return
	default:
	}
	if e.Kind == "frame" {
		return // зритель не успевает: этот кадр пропустить
	}
	// Смена состояния важнее кадра: освободить место.
	select {
	case <-v.ch:
	default:
	}
	select {
	case v.ch <- e:
	default:
	}
}

func (v *browserViewer) close() { v.once.Do(func() { close(v.ch) }) }

// WatchBrowser — просмотр сеанса: канал событий (закрывается, когда сеанс
// закрыт или зритель ушёл — stop).
func (d *Daemon) WatchBrowser(ctx context.Context, session string) (<-chan BrowserEvent, func(), error) {
	b, err := d.browser()
	if err != nil {
		return nil, nil, err
	}
	b.SetEventSink(d.browserEvent)
	v := &browserViewer{ch: make(chan BrowserEvent, ViewerQueue)}
	d.bviews.mu.Lock()
	if d.bviews.viewers == nil {
		d.bviews.viewers = map[string]map[*browserViewer]bool{}
	}
	first := len(d.bviews.viewers[session]) == 0
	if first {
		d.bviews.viewers[session] = map[*browserViewer]bool{}
	}
	d.bviews.viewers[session][v] = true
	d.bviews.mu.Unlock()
	stop := func() { d.unwatch(session, v) }
	if !first {
		// Просмотр уже идёт: последний кадр — сразу (следующий screencast
		// пришлёт, только когда страница изменится).
		d.bviews.mu.Lock()
		f, ok := d.bviews.latest[session]
		d.bviews.mu.Unlock()
		if ok {
			v.push(f)
		}
		return v.ch, stop, nil
	}
	if err := b.Owner(ctx, "view", map[string]any{"session": session}, nil); err != nil {
		stop()
		return nil, nil, err
	}
	return v.ch, stop, nil
}

func (d *Daemon) unwatch(session string, v *browserViewer) {
	d.bviews.mu.Lock()
	vs := d.bviews.viewers[session]
	_, had := vs[v]
	delete(vs, v)
	last := had && len(vs) == 0
	if last {
		delete(d.bviews.viewers, session)
		delete(d.bviews.latest, session)
	}
	d.bviews.mu.Unlock()
	v.close()
	if last {
		if b, err := d.browser(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.Owner(ctx, "unview", map[string]any{"session": session}, nil)
		}
	}
}

// browserEvent — событие браузера зрителям его сеанса.
func (d *Daemon) browserEvent(kind string, data json.RawMessage) {
	var p struct {
		Session string  `json:"session"`
		ID      string  `json:"id"`
		JPEG    []byte  `json:"jpeg"`
		Width   float64 `json:"width"`
		Height  float64 `json:"height"`
		URL     string  `json:"url"`
		State   string  `json:"state"`
		Reason  string  `json:"reason"`
	}
	if json.Unmarshal(data, &p) != nil {
		return
	}
	e := BrowserEvent{Kind: kind, JPEG: p.JPEG, Width: p.Width, Height: p.Height, URL: p.URL, State: p.State, Reason: p.Reason}
	session := p.Session
	if kind == "state" {
		var s BrowserSession
		if json.Unmarshal(data, &s) != nil {
			return
		}
		s.ViewURL = d.BrowserViewURL(s.ID)
		session, e.Session, e.State = s.ID, &s, s.State
	}
	d.bviews.mu.Lock()
	vs := d.bviews.viewers[session]
	list := make([]*browserViewer, 0, len(vs))
	for v := range vs {
		list = append(list, v)
	}
	switch kind {
	case "closed":
		delete(d.bviews.viewers, session)
		delete(d.bviews.latest, session)
	case "frame":
		if len(vs) > 0 {
			if d.bviews.latest == nil {
				d.bviews.latest = map[string]BrowserEvent{}
			}
			d.bviews.latest[session] = e
		}
	}
	d.bviews.mu.Unlock()
	for _, v := range list {
		v.push(e)
		if kind == "closed" {
			v.close()
		}
	}
}

// BrowserInput — ввод владельца в сеанс (мышь, клавиши, текст): только под
// управлением владельца или во время handoff — решает браузер.
func (d *Daemon) BrowserInput(ctx context.Context, session string, in json.RawMessage) error {
	b, err := d.browser()
	if err != nil {
		return err
	}
	return b.Owner(ctx, "input", map[string]any{"session": session, "input": in}, nil)
}

// BrowserControl — владелец берёт сеанс (on) или возвращает агенту.
func (d *Daemon) BrowserControl(ctx context.Context, session string, on bool) error {
	b, err := d.browser()
	if err != nil {
		return err
	}
	if err := b.Owner(ctx, "control", map[string]any{"session": session, "on": on}, nil); err != nil {
		return err
	}
	verb := map[bool]string{true: "took control of", false: "gave back"}[on]
	d.Log.Info(fmt.Sprintf("the owner %s browser session", verb), "session", session)
	return nil
}

// CloseBrowserSession — владелец закрывает сеанс агента.
func (d *Daemon) CloseBrowserSession(ctx context.Context, session string) error {
	b, err := d.browser()
	if err != nil {
		return err
	}
	if err := b.Owner(ctx, "close", map[string]any{"session": session}, nil); err != nil {
		return err
	}
	d.Log.Info("the owner closed a browser session", "session", session)
	return nil
}

// browserRoutes — API владельца: сеансы браузера (gate4ai browser sessions /
// watch / close).
func (d *Daemon) browserRoutes(mux *http.ServeMux) {
	owner := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !PrincipalFrom(r.Context()).IsOwner() {
				WriteError(w, 403, "owner_only", "only the owner sees and manages the browser sessions of agents")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /v1/browser/sessions", owner(func(w http.ResponseWriter, r *http.Request) {
		st, err := d.BrowserStatus(r.Context())
		if err != nil {
			WriteError(w, 502, "browser", err.Error())
			return
		}
		WriteJSON(w, 200, st)
	}))
	mux.HandleFunc("POST /v1/browser/sessions/{id}/{op}", owner(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var err error
		switch r.PathValue("op") {
		case "close":
			err = d.CloseBrowserSession(r.Context(), id)
		case "control":
			err = d.BrowserControl(r.Context(), id, true)
		case "release":
			err = d.BrowserControl(r.Context(), id, false)
		default:
			WriteError(w, 404, "not_found", "unknown operation; close, control or release")
			return
		}
		if err != nil {
			WriteError(w, 409, "browser", err.Error())
			return
		}
		WriteJSON(w, 200, map[string]any{"ok": true, "session": id})
	}))
}

// BrowserHandoff — ответ владельца на browser.handoff из просмотра сеанса:
// done (готово) или declined (не будет делать).
func (d *Daemon) BrowserHandoff(ctx context.Context, session, result string) error {
	b, err := d.browser()
	if err != nil {
		return err
	}
	return b.Owner(ctx, "handoff", map[string]any{"session": session, "result": result}, nil)
}
