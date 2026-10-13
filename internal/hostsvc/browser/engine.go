// SPDX-License-Identifier: FSL-1.1-ALv2

// Package browser — браузерный воркер, служба хоста для функций browser_*:
// выделенный профиль настоящего Chrome по CDP, движок Stagehand Go SDK v4
// (или прямой CDP). Домены проверяются по возможностям коннектора до и после
// редиректов. Все переходы, клики и ввод идут через Executor — единую точку
// исполнения с точкой для темпа (pacing); сейчас задержки нулевые.
package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gate4ai/sdk-go/domain"
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
	Close() error
}

// Pacing — задержка перед шагом Executor (точка для будущего «человеческого» темпа).
type Pacing func(ctx context.Context, step string) error

// NoPacing — нулевые задержки (MVP).
func NoPacing(context.Context, string) error { return nil }

// ErrDomain — домен не в списке разрешённых.
var ErrDomain = errors.New("domain is not in the connector's capabilities.browser.domains")

// Executor — единая точка исполнения действий в браузере.
type Executor struct {
	Engine  Engine
	Allowed []string
	Pacing  Pacing
}

func (x *Executor) pace(ctx context.Context, step string) error {
	if x.Pacing == nil {
		return nil
	}
	return x.Pacing(ctx, step)
}

// CheckURL — URL разрешён списком доменов коннектора (дополнительно к политике).
func (x *Executor) CheckURL(u string) error {
	if !domain.Matches(u, x.Allowed) {
		return fmt.Errorf("%w: %s", ErrDomain, domain.HostOf(u))
	}
	return nil
}

// Open переходит по URL; после редиректов итоговый адрес тоже проверяется,
// иначе страница закрывается переходом на about:blank.
func (x *Executor) Open(ctx context.Context, u string) (string, error) {
	if err := x.CheckURL(u); err != nil {
		return "", err
	}
	if err := x.pace(ctx, "navigate"); err != nil {
		return "", err
	}
	if err := x.Engine.Navigate(ctx, u); err != nil {
		return "", err
	}
	final, err := x.Engine.URL(ctx)
	if err != nil {
		return "", err
	}
	if err := x.CheckURL(final); err != nil {
		_ = x.Engine.Navigate(ctx, "about:blank")
		return "", fmt.Errorf("redirected off the allowlist: %w", err)
	}
	return final, nil
}

// Observe — адрес, заголовок и текст текущей страницы (max символов, 0 — без предела).
func (x *Executor) Observe(ctx context.Context, max int) (url, title, text string, truncated bool, err error) {
	if err = x.pace(ctx, "observe"); err != nil {
		return
	}
	if url, err = x.Engine.URL(ctx); err != nil {
		return
	}
	if err = x.CheckURL(url); err != nil {
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

// Screenshot — снимок элемента текущей страницы.
func (x *Executor) Screenshot(ctx context.Context, selector string) ([]byte, error) {
	if err := x.pace(ctx, "screenshot"); err != nil {
		return nil, err
	}
	return x.Engine.Screenshot(ctx, selector)
}

// Extract — извлечение по инструкции с текущей страницы (Act с естественным
// языком для изменений не используется: в нём нет точки перехвата политикой).
func (x *Executor) Extract(ctx context.Context, instruction string) (any, error) {
	if err := x.pace(ctx, "extract"); err != nil {
		return nil, err
	}
	return x.Engine.Extract(ctx, instruction)
}
