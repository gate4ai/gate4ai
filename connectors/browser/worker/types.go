// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Page — страница: адрес после редиректов, заголовок, текст (observe).
type Page struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// StillLoading — open: страница не догрузилась за IdleMax.
	StillLoading bool `json:"still_loading,omitempty"`
}

// ActRequest — действие по CSS-селектору, без естественного языка.
type ActRequest struct {
	Selector string `json:"selector"`
	Method   string `json:"method"`
	// Text — из элементов по селектору берётся первый видимый, чей текст (или
	// подпись, placeholder, aria-label) содержит Text.
	Text string `json:"text,omitempty"`
	// Value — значение для fill / append / paste, клавиша или сочетание для press.
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

// ActResponse — адрес страницы после действия, текст элемента (до 2000
// символов) и итоговое значение поля после fill / append / paste.
type ActResponse struct {
	URL   string      `json:"url"`
	Text  string      `json:"text,omitempty"`
	Value *FieldValue `json:"value,omitempty"`
}

// FieldValue — что записалось в поле: длина и начало (и конец, если длинное).
// Значение пароля и полей на странице с введённым секретом не отдаётся.
type FieldValue struct {
	Length int    `json:"length"`           // символов
	Start  string `json:"start"`            // первые ValueStart символов
	End    string `json:"end,omitempty"`    // последние ValueEnd символов, если значение длиннее
	Hidden bool   `json:"hidden,omitempty"` // пароль или секрет на странице: значение не показывается
}

// Сколько итогового значения поля показывать в ответе act.
const (
	ValueStart = 1000
	ValueEnd   = 200
)

// Методы act.
const (
	ActClick  = "click"  // клик по элементу
	ActFill   = "fill"   // заменить значение поля (input, textarea, contenteditable) на Value
	ActAppend = "append" // дописать Value в конец значения поля
	ActPaste  = "paste"  // заменить значение, вставив Value как из буфера обмена (событие paste: редакторы разбирают разметку)
	ActPress  = "press"  // клавиша или сочетание Value (Enter, Ctrl+Enter) в элементе
	ActWait   = "wait"   // дождаться элемента и вернуть его текст
)

// ActMethods — методы act по порядку (для --help и ошибок).
var ActMethods = []string{ActClick, ActFill, ActAppend, ActPaste, ActPress, ActWait}

// writes — метод пишет Value в поле.
func writes(method string) bool {
	return method == ActFill || method == ActAppend || method == ActPaste
}

// ErrNotFound — элемента нет на странице за время ожидания.
var ErrNotFound = errors.New("element not found")

// Пределы act.
const (
	MaxActValue   = 256 << 10 // символов в value для fill / append / paste
	MaxActTimeout = 60000
)

// ValueTooLong — единый текст ошибки о длине value (схема манифеста и CheckAct).
func ValueTooLong(n int) error {
	return fmt.Errorf("argument \"value\" is too long: %d characters, the limit is %d", n, MaxActValue)
}

// CheckAct проверяет запрос действия до исполнения.
func CheckAct(r ActRequest) error {
	switch r.Method {
	case ActClick, ActWait:
	case ActFill, ActAppend, ActPaste:
		if n := utf8.RuneCountInString(r.Value); n > MaxActValue {
			return ValueTooLong(n)
		}
		if r.Method != ActFill && r.Value == "" {
			return fmt.Errorf("act: %s needs value", r.Method)
		}
	case ActPress:
		if _, err := ParseKey(r.Value); err != nil {
			return err
		}
	default:
		return fmt.Errorf("act: unknown method %q (%s)", r.Method, strings.Join(ActMethods, ", "))
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
