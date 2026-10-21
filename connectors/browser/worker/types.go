// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"errors"
	"fmt"
)

// Page — страница: адрес после редиректов, заголовок, текст (observe).
type Page struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ActRequest — действие по CSS-селектору, без естественного языка.
type ActRequest struct {
	Selector string `json:"selector"`
	Method   string `json:"method"`
	// Text — из элементов по селектору берётся первый видимый, чей текст (или
	// подпись, placeholder, aria-label) содержит Text.
	Text string `json:"text,omitempty"`
	// Value — значение для fill, клавиша для press.
	Value string `json:"value,omitempty"`
	// TimeoutMS — сколько ждать появления элемента (0 — 10 с, предел MaxActTimeout).
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// Expect* — результат действия, которого надо дождаться (иначе ErrNoEffect):
	// видимый элемент по селектору и/или с текстом, смена адреса.
	ExpectSelector  string `json:"expect_selector,omitempty"`
	ExpectText      string `json:"expect_text,omitempty"`
	ExpectURLChange bool   `json:"expect_url_change,omitempty"`
	ExpectMS        int    `json:"expect_timeout_ms,omitempty"`
}

// ActResponse — адрес страницы после действия и текст элемента (до 2000 символов).
type ActResponse struct {
	URL  string `json:"url"`
	Text string `json:"text,omitempty"`
}

// Методы act.
const (
	ActClick = "click" // клик по элементу
	ActFill  = "fill"  // ввод Value в поле (input, textarea, contenteditable)
	ActPress = "press" // клавиша Value (Enter, Tab, Escape) в элементе
	ActWait  = "wait"  // дождаться элемента и вернуть его текст
)

// ErrNotFound — элемента нет на странице за время ожидания.
var ErrNotFound = errors.New("element not found")

// Пределы act.
const (
	MaxActValue   = 4096
	MaxActTimeout = 60000
)

// CheckAct проверяет запрос действия до исполнения.
func CheckAct(r ActRequest) error {
	switch r.Method {
	case ActClick, ActWait:
	case ActFill:
		if len(r.Value) > MaxActValue {
			return fmt.Errorf("act: value is longer than %d bytes", MaxActValue)
		}
	case ActPress:
		switch r.Value {
		case "Enter", "Tab", "Escape", "Backspace", "ArrowDown", "ArrowUp":
		default:
			return fmt.Errorf("act: key %q is not supported", r.Value)
		}
	default:
		return fmt.Errorf("act: unknown method %q (click, fill, press, wait)", r.Method)
	}
	if r.Selector == "" {
		return errors.New("act: selector is required")
	}
	if r.TimeoutMS < 0 || r.TimeoutMS > MaxActTimeout {
		return fmt.Errorf("act: timeout_ms must be 0..%d", MaxActTimeout)
	}
	if r.ExpectMS < 0 || r.ExpectMS > MaxActTimeout {
		return fmt.Errorf("act: expect_timeout_ms must be 0..%d", MaxActTimeout)
	}
	return nil
}
