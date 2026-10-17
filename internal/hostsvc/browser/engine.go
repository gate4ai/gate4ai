// SPDX-License-Identifier: AGPL-3.0-only

// Package browser — браузерный воркер, служба хоста для функций browser_*:
// выделенный профиль настоящего Chrome по CDP, движок Stagehand Go SDK v4
// (или прямой CDP). Домены проверяются по возможностям коннектора до и после
// редиректов. Все переходы, клики и ввод идут через Executor — единую точку
// исполнения с точкой для темпа (pacing); сейчас задержки нулевые.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/hostfuncs"

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
	// Eval — выражение JS на странице, результат по значению в out.
	Eval(ctx context.Context, expr string, out any) error
	// InsertText — ввод текста в элемент в фокусе (как с клавиатуры).
	InsertText(ctx context.Context, text string) error
	// Key — нажатие клавиши (Enter, Tab, Escape, Backspace, ArrowDown, ArrowUp) в фокусе.
	Key(ctx context.Context, key string) error
	Close() error
}

// Pacing — задержка перед шагом Executor (точка для будущего «человеческого» темпа).
type Pacing func(ctx context.Context, step string) error

// NoPacing — нулевые задержки (MVP).
func NoPacing(context.Context, string) error { return nil }

// ErrDomain — сайт не разрешён списком коннектора (capabilities.browser).
var ErrDomain = errors.New("site is not allowed by the connector's capabilities.browser")

// Executor — единая точка исполнения действий в браузере.
type Executor struct {
	Engine Engine
	Sites  domain.Sites
	Pacing Pacing
}

func (x *Executor) pace(ctx context.Context, step string) error {
	if x.Pacing == nil {
		return nil
	}
	return x.Pacing(ctx, step)
}

// CheckURL — URL разрешён списком сайтов коннектора (дополнительно к политике).
func (x *Executor) CheckURL(u string) error {
	if !x.Sites.Allows(u) {
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

// ActPoll — шаг ожидания элемента в Act.
var ActPoll = 200 * time.Millisecond

// actJS ищет видимый элемент по селектору, чей текст (value, placeholder,
// aria-label, подписи) содержит нужный текст без учёта регистра и «ё»; из
// нескольких берёт элемент с самой короткой подписью. Готовит его к методу
// (click — клик, fill — фокус и выделение содержимого, press — фокус) и
// возвращает его текст.
const actJS = `((sel, text, method) => {
  const norm = s => (s || '').replace(/\s+/g, ' ').trim().toLowerCase().replace(/ё/g, 'е');
  const vis = e => { const r = e.getBoundingClientRect(), st = getComputedStyle(e);
    return r.width > 0 && r.height > 0 && st.visibility !== 'hidden' && st.display !== 'none'; };
  const label = e => [e.innerText, e.value, e.placeholder, e.getAttribute('aria-label'), e.getAttribute('title'),
    e.labels ? [...e.labels].map(l => l.innerText).join(' ') : ''].filter(Boolean).join(' ');
  const want = norm(text);
  // из подходящих — с самой короткой подписью: кнопка, а не её контейнер
  const e = [...document.querySelectorAll(sel)].filter(e => vis(e) && (!want || norm(label(e)).includes(want)))
    .sort((a, b) => norm(label(a)).length - norm(label(b)).length)[0];
  if (!e) return null;
  e.scrollIntoView({block: 'center'});
  if (method === 'click') e.click();
  if (method === 'fill' || method === 'press') {
    e.focus();
    if (method === 'fill') {
      if (typeof e.select === 'function') e.select();
      else { const r = document.createRange(); r.selectNodeContents(e); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
    }
  }
  return {text: (e.innerText || e.value || '').slice(0, 2000)};
})`

// Act — click / fill / press / wait по селектору: ждёт элемент (до TimeoutMS),
// выполняет метод; итоговый адрес проверяется по списку доменов.
func (x *Executor) Act(ctx context.Context, r abi.BrowserRequest) (abi.BrowserActResponse, error) {
	if err := x.pace(ctx, "act:"+r.Method); err != nil {
		return abi.BrowserActResponse{}, err
	}
	if u, err := x.Engine.URL(ctx); err != nil {
		return abi.BrowserActResponse{}, err
	} else if err := x.CheckURL(u); err != nil {
		return abi.BrowserActResponse{}, err
	}
	timeout := time.Duration(r.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	args, _ := json.Marshal([]string{r.Selector, r.Text, r.Method})
	expr := actJS + "(..." + string(args) + ")"
	deadline := time.Now().Add(timeout)
	var found *struct {
		Text string `json:"text"`
	}
	for {
		if err := x.Engine.Eval(ctx, expr, &found); err != nil {
			return abi.BrowserActResponse{}, err
		}
		if found != nil {
			break
		}
		if time.Now().After(deadline) {
			what := r.Selector
			if r.Text != "" {
				what += " with text " + fmt.Sprintf("%q", r.Text)
			}
			return abi.BrowserActResponse{}, fmt.Errorf("%w: %s", hostfuncs.ErrNotFound, what)
		}
		select {
		case <-time.After(ActPoll):
		case <-ctx.Done():
			return abi.BrowserActResponse{}, ctx.Err()
		}
	}
	switch r.Method {
	case abi.ActFill:
		var err error
		if r.Value == "" {
			err = x.Engine.Key(ctx, "Backspace")
		} else {
			err = x.Engine.InsertText(ctx, r.Value)
		}
		if err != nil {
			return abi.BrowserActResponse{}, err
		}
	case abi.ActPress:
		if err := x.Engine.Key(ctx, r.Value); err != nil {
			return abi.BrowserActResponse{}, err
		}
	}
	u, err := x.Engine.URL(ctx)
	if err != nil {
		return abi.BrowserActResponse{}, err
	}
	if err := x.CheckURL(u); err != nil {
		_ = x.Engine.Navigate(ctx, "about:blank")
		return abi.BrowserActResponse{}, fmt.Errorf("action led off the allowlist: %w", err)
	}
	return abi.BrowserActResponse{URL: u, Text: found.Text}, nil
}
