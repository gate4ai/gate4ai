// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Пределы handoff: человек входит сам (капча, SMS, вход без учётки в gate4ai).
const (
	HandoffDefault = 5 * time.Minute
	HandoffMax     = 9 * time.Minute // меньше limits.timeout_s пакета
)

// HandoffRequest — чего ждать: смены адреса (по умолчанию) и/или видимого элемента.
type HandoffRequest struct {
	UntilURLChange bool
	UntilSelector  string
	Timeout        time.Duration
}

// HandoffResult — чем закончилось ожидание.
type HandoffResult struct {
	URL    string `json:"url"`
	Status string `json:"status"` // done | timeout
}

// CheckHandoff проверяет запрос до уведомления человека.
func CheckHandoff(r HandoffRequest) error {
	if r.Timeout < 0 || r.Timeout > HandoffMax {
		return fmt.Errorf("handoff: timeout_ms must be 0..%d", HandoffMax.Milliseconds())
	}
	return nil
}

// Handoff ждёт, пока человек сам сделает шаг в окне Chrome: адрес страницы
// сменится и/или появится элемент UntilSelector (без условий — смена адреса).
// Итоговый адрес проверяется по списку сайтов, как после act.
func (x *Executor) Handoff(ctx context.Context, r HandoffRequest) (HandoffResult, error) {
	if err := CheckHandoff(r); err != nil {
		return HandoffResult{}, err
	}
	if !r.UntilURLChange && r.UntilSelector == "" {
		r.UntilURLChange = true
	}
	if r.Timeout == 0 {
		r.Timeout = HandoffDefault
	}
	before, err := x.Engine.URL(ctx)
	if err != nil {
		return HandoffResult{}, err
	}
	deadline := time.Now().Add(r.Timeout)
	for {
		ok := true
		u, err := x.Engine.URL(ctx)
		if err != nil {
			return HandoffResult{}, err
		}
		if r.UntilURLChange {
			ok = u != before
		}
		if ok && r.UntilSelector != "" {
			if ok, err = x.present(ctx, r.UntilSelector, ""); err != nil {
				return HandoffResult{}, err
			}
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			// Человек не закончил: агент решает сам — подождать ещё или спросить.
			return HandoffResult{URL: u, Status: "timeout"}, nil
		}
		if err := wait(ctx, HandoffPoll); err != nil {
			return HandoffResult{}, err
		}
	}
	u, err := x.settledURL(ctx)
	if err != nil && !errors.Is(err, ErrDomain) {
		return HandoffResult{}, err
	}
	if err != nil {
		_ = x.Engine.Navigate(ctx, "about:blank")
		return HandoffResult{}, fmt.Errorf("the page left the allowed sites: %w", err)
	}
	x.HookNetwork(ctx)
	return HandoffResult{URL: u, Status: "done"}, nil
}

// HandoffPoll — шаг опроса страницы в Handoff.
var HandoffPoll = 500 * time.Millisecond

// Reload перезагружает текущую страницу (адрес проверяется, как в Open).
func (x *Executor) Reload(ctx context.Context) (string, error) {
	u, err := x.settledURL(ctx)
	if err != nil {
		return "", err
	}
	return x.Open(ctx, u)
}
