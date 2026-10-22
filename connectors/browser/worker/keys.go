// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"fmt"
	"strings"
)

// Key — клавиша или сочетание для act press: модификаторы и клавиша.
type Key struct {
	Ctrl, Shift, Alt, Meta bool
	Name                   string // Enter, ArrowDown, a, 7
}

// keyCodes — клавиши press: имя → (code, windowsVirtualKeyCode).
var keyCodes = map[string]struct {
	code string
	vk   int
}{
	"Enter": {"Enter", 13}, "Tab": {"Tab", 9}, "Escape": {"Escape", 27}, "Backspace": {"Backspace", 8},
	"Delete": {"Delete", 46}, "Space": {"Space", 32}, "Home": {"Home", 36}, "End": {"End", 35},
	"PageUp": {"PageUp", 33}, "PageDown": {"PageDown", 34},
	"ArrowUp": {"ArrowUp", 38}, "ArrowDown": {"ArrowDown", 40}, "ArrowLeft": {"ArrowLeft", 37}, "ArrowRight": {"ArrowRight", 39},
}

// KeyNames — клавиши press для --help и ошибок (ещё: буквы a–z и цифры 0–9).
const KeyNames = "Enter, Tab, Escape, Backspace, Delete, Space, Home, End, PageUp, PageDown, ArrowUp, ArrowDown, ArrowLeft, ArrowRight, a–z, 0–9; with modifiers Ctrl, Shift, Alt, Meta joined by +: Ctrl+A, Ctrl+Enter, Shift+Tab"

// ErrClipboard — сочетание обращается к системному буферу обмена: он общий с
// человеком (Ctrl+V вставил бы на сайт то, что человек скопировал, — например,
// пароль). Вставка текста — метод paste.
var ErrClipboard = fmt.Errorf("act: the system clipboard belongs to the user and is not used; to paste a text use method paste with value")

// ParseKey разбирает клавишу press: "Enter", "ctrl+a", "Control+Shift+Enter".
func ParseKey(s string) (Key, error) {
	var k Key
	parts := strings.Split(strings.TrimSpace(s), "+")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if i == len(parts)-1 {
			k.Name = keyName(p)
			break
		}
		switch strings.ToLower(p) {
		case "ctrl", "control":
			k.Ctrl = true
		case "shift":
			k.Shift = true
		case "alt", "option":
			k.Alt = true
		case "meta", "cmd", "command":
			k.Meta = true
		default:
			return Key{}, fmt.Errorf("act: unknown modifier %q in key %q (keys: %s)", p, s, KeyNames)
		}
	}
	if k.Name == "" {
		return Key{}, fmt.Errorf("act: key %q is not supported (keys: %s)", s, KeyNames)
	}
	if (k.Ctrl || k.Meta) && (k.Name == "c" || k.Name == "x" || k.Name == "v") {
		return Key{}, ErrClipboard
	}
	return k, nil
}

// keyName — каноническое имя клавиши ("" — не поддерживается).
func keyName(p string) string {
	for n := range keyCodes {
		if strings.EqualFold(n, p) {
			return n
		}
	}
	switch strings.ToLower(p) {
	case "esc":
		return "Escape"
	case "return":
		return "Enter"
	case "del":
		return "Delete"
	case " ":
		return "Space"
	}
	if len(p) == 1 {
		c := strings.ToLower(p)[0]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			return string(c)
		}
	}
	return ""
}

// String — сочетание в записи Playwright / Stagehand: "Control+Shift+Enter".
func (k Key) String() string {
	var b strings.Builder
	for _, m := range []struct {
		on   bool
		name string
	}{{k.Ctrl, "Control"}, {k.Alt, "Alt"}, {k.Meta, "Meta"}, {k.Shift, "Shift"}} {
		if m.on {
			b.WriteString(m.name + "+")
		}
	}
	b.WriteString(k.Name)
	return b.String()
}

// command — сочетание редактирования, которое исполняется командой страницы
// (document.execCommand), а не событием клавиши: Chrome по CDP не выполняет
// Ctrl+A / Ctrl+Z от синтетических нажатий. "" — обычное нажатие.
func (k Key) command() string {
	if !(k.Ctrl || k.Meta) || k.Alt {
		return ""
	}
	switch {
	case k.Name == "a" && !k.Shift:
		return "selectAll"
	case k.Name == "z" && !k.Shift:
		return "undo"
	case k.Name == "y" && !k.Shift, k.Name == "z" && k.Shift:
		return "redo"
	}
	return ""
}

// cdpKey — параметры Input.dispatchKeyEvent: key, code, код клавиши, текст
// (символ, который клавиша вводит без Ctrl / Alt / Meta) и модификаторы.
func (k Key) cdpKey() (key, code string, vk int, text string, mods int) {
	if k.Alt {
		mods |= 1
	}
	if k.Ctrl {
		mods |= 2
	}
	if k.Meta {
		mods |= 4
	}
	if k.Shift {
		mods |= 8
	}
	plain := !k.Ctrl && !k.Alt && !k.Meta
	if kc, ok := keyCodes[k.Name]; ok {
		key, code, vk = k.Name, kc.code, kc.vk
		switch k.Name {
		case "Enter":
			text = "\r"
		case "Space":
			key, text = " ", " "
		}
		if !plain {
			text = ""
		}
		return
	}
	c := k.Name[0]
	key = k.Name
	if c >= 'a' && c <= 'z' {
		code, vk = "Key"+strings.ToUpper(k.Name), int(c-'a'+'A')
		if k.Shift {
			key = strings.ToUpper(k.Name)
		}
	} else {
		code, vk = "Digit"+k.Name, int(c)
	}
	if plain {
		text = key
	}
	return
}
