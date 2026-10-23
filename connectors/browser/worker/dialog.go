// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Dialog — JS-диалог страницы: alert, confirm, prompt или beforeunload
// («Покинуть страницу?»). Пока он открыт, страница не отвечает: переход и
// любой скрипт ждут ответа на него.
type Dialog struct {
	Type          string `json:"type"`
	Message       string `json:"message"`
	URL           string `json:"url,omitempty"`
	DefaultPrompt string `json:"default_prompt,omitempty"`
}

// DialogError — действие прервано открытым диалогом (или диалог уже открыт):
// агент отвечает на него действием dialog.
type DialogError struct{ Dialog Dialog }

func (e *DialogError) Error() string {
	return fmt.Sprintf("a %s dialog is open on the page: %q; the page waits for an answer: browser.dialog --session <id> --accept (or --dismiss%s)",
		e.Dialog.Type, e.Dialog.Message, map[bool]string{true: "; a prompt takes --text", false: ""}[e.Dialog.Type == "prompt"])
}

// ErrNoDialog — dialog без открытого диалога.
var ErrNoDialog = errors.New("no dialog is open on the page")

// Dialoger — движок видит JS-диалоги своей вкладки и отвечает на них.
type Dialoger interface {
	// OpenDialog — открытый диалог (nil — нет).
	OpenDialog() *Dialog
	// DialogOpened — канал, который закроется, когда откроется следующий
	// диалог, ждущий ответа агента (принятые сами не в счёт).
	DialogOpened() <-chan struct{}
	// HandleDialog — ответ на открытый диалог (text — для prompt).
	HandleDialog(ctx context.Context, accept bool, text string) error
	// AcceptUnload — пока on, beforeunload принимается сам: агент сам попросил
	// уйти со страницы (open, reload).
	AcceptUnload(on bool)
	// AutoDialogs — любой диалог закрывается сам (временная вкладка, вкладка
	// перед закрытием): alert и beforeunload — принять, confirm и prompt — отклонить.
	AutoDialogs()
}

// dialogs — диалоги вкладки по событиям CDP Page.javascriptDialogOpening / Closed.
type dialogs struct {
	mu     sync.Mutex
	cur    *Dialog
	unload bool
	auto   bool
	opened chan struct{}
	// answer — Page.handleJavaScriptDialog (асинхронно: из цикла чтения
	// событий вызов ждал бы сам себя).
	answer func(ctx context.Context, accept bool, text string) error
}

func (d *dialogs) openedCh() chan struct{} {
	if d.opened == nil {
		d.opened = make(chan struct{})
	}
	return d.opened
}

// event — Page.javascriptDialog* вкладки.
func (d *dialogs) event(m cdpMsg) {
	switch m.Method {
	case "Page.javascriptDialogOpening":
		var p struct {
			Type          string `json:"type"`
			Message       string `json:"message"`
			URL           string `json:"url"`
			DefaultPrompt string `json:"defaultPrompt"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			return
		}
		d.mu.Lock()
		auto, unload := d.auto, d.unload && p.Type == "beforeunload"
		if !auto && !unload {
			d.cur = &Dialog{Type: p.Type, Message: p.Message, URL: RedactURL(p.URL, nil), DefaultPrompt: p.DefaultPrompt}
			close(d.openedCh())
			d.opened = make(chan struct{})
		}
		answer := d.answer
		d.mu.Unlock()
		if (auto || unload) && answer != nil {
			accept := p.Type == "beforeunload" || p.Type == "alert"
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = answer(ctx, accept, "")
			}()
		}
	case "Page.javascriptDialogClosed":
		d.mu.Lock()
		d.cur = nil
		d.mu.Unlock()
	}
}

func (d *dialogs) OpenDialog() *Dialog {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur == nil {
		return nil
	}
	c := *d.cur
	return &c
}

func (d *dialogs) DialogOpened() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.openedCh()
}

func (d *dialogs) HandleDialog(ctx context.Context, accept bool, text string) error {
	d.mu.Lock()
	cur, answer := d.cur, d.answer
	d.mu.Unlock()
	if cur == nil {
		return ErrNoDialog
	}
	if err := answer(ctx, accept, text); err != nil {
		return err
	}
	d.mu.Lock()
	d.cur = nil // событие Closed придёт следом; следующий вызов не должен видеть старый диалог
	d.mu.Unlock()
	return nil
}

func (d *dialogs) AcceptUnload(on bool) {
	d.mu.Lock()
	d.unload = on
	cur, answer := d.cur, d.answer
	d.mu.Unlock()
	if on && cur != nil && cur.Type == "beforeunload" && answer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if answer(ctx, true, "") == nil {
			d.mu.Lock()
			d.cur = nil
			d.mu.Unlock()
		}
	}
}

func (d *dialogs) AutoDialogs() {
	d.mu.Lock()
	d.auto = true
	cur, answer := d.cur, d.answer
	d.mu.Unlock()
	if cur != nil && answer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if answer(ctx, cur.Type == "beforeunload" || cur.Type == "alert", "") == nil {
			d.mu.Lock()
			d.cur = nil
			d.mu.Unlock()
		}
	}
}

// Run — действие на странице с учётом диалогов. Открытый диалог отвечает
// DialogError сразу (кроме beforeunload при переходе, о котором попросил
// агент, — navigate: он принимается); диалог, открывшийся во время действия,
// прерывает его тем же DialogError, а не вешает до таймаута.
func (x *Executor) Run(ctx context.Context, navigate bool, fn func(ctx context.Context) error) error {
	d, ok := x.Engine.(Dialoger)
	if !ok {
		return fn(ctx)
	}
	if navigate {
		d.AcceptUnload(true)
		defer d.AcceptUnload(false)
	}
	opened := d.DialogOpened()
	if dl := d.OpenDialog(); dl != nil {
		return &DialogError{Dialog: *dl}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-opened:
			if dl := d.OpenDialog(); dl != nil {
				cancel(&DialogError{Dialog: *dl})
			}
		case <-stop:
		}
	}()
	err := fn(ctx)
	var de *DialogError
	if errors.As(context.Cause(ctx), &de) {
		return de
	}
	return err
}

// HandleDialog — ответ агента на открытый диалог; адрес страницы после него
// (confirm перед уходом со страницы мог её сменить).
func (x *Executor) HandleDialog(ctx context.Context, accept bool, text string) (Dialog, string, error) {
	d, ok := x.Engine.(Dialoger)
	if !ok {
		return Dialog{}, "", ErrNoDialog
	}
	dl := d.OpenDialog()
	if dl == nil {
		return Dialog{}, "", ErrNoDialog
	}
	if err := d.HandleDialog(ctx, accept, text); err != nil {
		return Dialog{}, "", err
	}
	if wait(ctx, 300*time.Millisecond) != nil {
		return *dl, "", ctx.Err()
	}
	if dl2 := d.OpenDialog(); dl2 != nil {
		return *dl, "", &DialogError{Dialog: *dl2} // за ним — следующий
	}
	u, err := x.settledURL(ctx)
	if err != nil && !errors.Is(err, ErrDomain) {
		return *dl, "", err
	}
	if err != nil {
		_ = x.Engine.Navigate(ctx, "about:blank")
		return *dl, "", fmt.Errorf("the page left the allowed sites: %w", err)
	}
	x.HookNetwork(ctx)
	return *dl, u, nil
}

// ManualDialogs — диалоги снова ждут ответа агента (после AutoDialogs:
// стартовая вкладка переходит к следующей сессии).
func (d *dialogs) ManualDialogs() {
	d.mu.Lock()
	d.auto = false
	d.mu.Unlock()
}
