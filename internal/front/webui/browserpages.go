// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/browserenv"
)

// Раздел «Браузер»: сеансы агентов в браузере gate4ai — список, просмотр
// вживую (кадры по websocket), управление владельцем и закрытие. Доступен и
// удалённо (/ui/browser): просмотр — всегда, управление вне handoff —
// только если вход прошёл подтверждение в Telegram или после подтверждения
// в Telegram самого управления (на телефоне видно, кто его просит).

// browserTimeout — сколько ждать ответа пакета для страницы.
const browserTimeout = 10 * time.Second

func (u *UI) browserList(r *http.Request) (string, map[string]any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), browserTimeout)
	defer cancel()
	st, err := u.D.BrowserStatus(ctx)
	env := u.BrowserEnv
	if env == nil {
		env = browserenv.System
	}
	// Чего не хватает на этом компьютере (Chrome, Xvfb) — с командами установки.
	return "browser", map[string]any{"Status": st, "Handoffs": u.D.Handoffs(), "Check": browserenv.CheckBrowser(env())}, err
}

func (u *UI) browserView(m mount) func(r *http.Request) (string, map[string]any, error) {
	return func(r *http.Request) (string, map[string]any, error) {
		id := r.PathValue("id")
		ctx, cancel := context.WithTimeout(r.Context(), browserTimeout)
		defer cancel()
		s, err := u.D.BrowserSession(ctx, id)
		data := map[string]any{"ID": id, "Session": s}
		for _, h := range u.D.Handoffs() {
			if h.Session == id {
				data["Handoff"] = h
			}
		}
		return "browser_view", data, err
	}
}

// browserControl — взять сеанс (on=1) или вернуть агенту (on=0).
func (u *UI) browserControl(m mount) func(w http.ResponseWriter, r *http.Request) map[string]any {
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		id := r.PathValue("id")
		back := "/browser/" + id
		on := r.PostForm.Get("on") == "1"
		ctx, cancel := context.WithTimeout(r.Context(), browserTimeout)
		defer cancel()
		if err := u.D.BrowserControl(ctx, id, on); err != nil {
			return redirectErr(back, err)
		}
		if on {
			return redirect(back, M("The session is yours: the agent waits until you give it back."))
		}
		return redirect(back, M("The session is given back to the agent."))
	}
}

func (u *UI) browserClose(w http.ResponseWriter, r *http.Request) map[string]any {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), browserTimeout)
	defer cancel()
	if err := u.D.CloseBrowserSession(ctx, id); err != nil {
		return redirectErr("/browser/"+id, err)
	}
	return redirect("/browser", M("The browser session %s is closed", id))
}

// browserHandoff — ответ на просьбу агента: готово или не буду.
func (u *UI) browserHandoff(w http.ResponseWriter, r *http.Request) map[string]any {
	id := r.PathValue("id")
	res := r.PostForm.Get("result")
	ctx, cancel := context.WithTimeout(r.Context(), browserTimeout)
	defer cancel()
	if err := u.D.BrowserHandoff(ctx, id, res); err != nil {
		return redirectErr("/browser/"+id, err)
	}
	if res == "declined" {
		return redirect("/browser/"+id, M("The agent is told you will not do it."))
	}
	return redirect("/browser/"+id, M("The agent continues."))
}

// wsMeta — описание кадра или события для просмотрщика (текстовое сообщение;
// сам кадр — следующим двоичным).
type wsMeta struct {
	Type    string                 `json:"type"` // frame | state | closed | error
	Width   float64                `json:"width,omitempty"`
	Height  float64                `json:"height,omitempty"`
	URL     string                 `json:"url,omitempty"`
	State   string                 `json:"state,omitempty"`
	Session *daemon.BrowserSession `json:"session,omitempty"`
	Message string                 `json:"message,omitempty"`
}

// browserWS — кадры сеанса и ввод владельца. Сессия Web UI — как у страниц;
// websocket.Accept сверяет Origin с Host (чужая страница не подключится).
func (u *UI) browserWS(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := u.session(m, r); !ok {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		if !u.originOK(m, r) {
			http.Error(w, "bad origin", http.StatusForbidden)
			return
		}
		id := r.PathValue("id")
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(256 << 10)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		send := func(v wsMeta) error {
			b, _ := json.Marshal(v)
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			defer wcancel()
			return c.Write(wctx, websocket.MessageText, b)
		}
		ch, stop, err := u.D.WatchBrowser(ctx, id)
		if err != nil {
			_ = send(wsMeta{Type: "error", Message: err.Error()})
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
		defer stop()
		go func() {
			defer cancel()
			for {
				_, b, err := c.Read(ctx)
				if err != nil {
					return
				}
				if !u.sessionAlive(m, r) {
					return
				}
				ictx, icancel := context.WithTimeout(ctx, browserTimeout)
				err = u.D.BrowserInput(ictx, id, b)
				icancel()
				if err != nil {
					_ = send(wsMeta{Type: "error", Message: err.Error()})
				}
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok {
					c.Close(websocket.StatusNormalClosure, "")
					return
				}
				var err error
				switch e.Kind {
				case "frame":
					if err = send(wsMeta{Type: "frame", Width: e.Width, Height: e.Height, URL: e.URL, State: e.State}); err == nil {
						wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
						err = c.Write(wctx, websocket.MessageBinary, e.JPEG)
						wcancel()
					}
				case "state":
					err = send(wsMeta{Type: "state", State: e.State, Session: e.Session})
				case "closed":
					err = send(wsMeta{Type: "closed", Message: e.Reason})
				}
				if err != nil {
					return
				}
			}
		}
	}
}

// sessionAlive — сессия Web UI ещё действует (выход, истечение, смена защиты
// закрывают и открытый просмотр).
func (u *UI) sessionAlive(m mount, r *http.Request) bool {
	_, _, ok := u.session(m, r)
	return ok
}
