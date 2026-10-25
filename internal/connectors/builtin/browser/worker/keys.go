// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Key — клавиша или сочетание для act press: модификаторы и клавиша.
type Key struct {
	Ctrl, Shift, Alt, Meta bool
	Name                   string // Enter, ArrowDown, a, 7, «,», «ж»
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

// charCodes — символы американской раскладки вне букв и цифр: символ →
// (code, windowsVirtualKeyCode, нужен ли Shift). Обработчики полей тегов
// сверяют и key (","), и устаревший keyCode (188).
var charCodes = map[rune]struct {
	code  string
	vk    int
	shift bool
}{
	',': {"Comma", 188, false}, '<': {"Comma", 188, true}, '.': {"Period", 190, false}, '>': {"Period", 190, true},
	';': {"Semicolon", 186, false}, ':': {"Semicolon", 186, true}, '/': {"Slash", 191, false}, '?': {"Slash", 191, true},
	'-': {"Minus", 189, false}, '_': {"Minus", 189, true}, '=': {"Equal", 187, false}, '+': {"Equal", 187, true},
	'[': {"BracketLeft", 219, false}, '{': {"BracketLeft", 219, true}, ']': {"BracketRight", 221, false}, '}': {"BracketRight", 221, true},
	'\\': {"Backslash", 220, false}, '|': {"Backslash", 220, true}, '\'': {"Quote", 222, false}, '"': {"Quote", 222, true},
	'`': {"Backquote", 192, false}, '~': {"Backquote", 192, true},
	'!': {"Digit1", '1', true}, '@': {"Digit2", '2', true}, '#': {"Digit3", '3', true}, '$': {"Digit4", '4', true}, '%': {"Digit5", '5', true},
	'^': {"Digit6", '6', true}, '&': {"Digit7", '7', true}, '*': {"Digit8", '8', true}, '(': {"Digit9", '9', true}, ')': {"Digit0", '0', true},
}

// KeyNames — клавиши press для --help и ошибок.
const KeyNames = "Enter, Tab, Escape, Backspace, Delete, Space, Home, End, PageUp, PageDown, ArrowUp, ArrowDown, ArrowLeft, ArrowRight or any single character (a, 7, «,», «;», «ж»); with modifiers Ctrl, Shift, Alt, Meta joined by +: Ctrl+A, Ctrl+Enter, Shift+Tab, Ctrl+,"

// ErrClipboard — сочетание обращается к системному буферу обмена: он общий с
// человеком (Ctrl+V вставил бы на сайт то, что человек скопировал, — например,
// пароль). Вставка текста — метод paste.
var ErrClipboard = fmt.Errorf("act: the system clipboard belongs to the user and is not used; to paste a text use method paste with value")

// ParseKey разбирает клавишу press: "Enter", "ctrl+a", "Control+Shift+Enter".
func ParseKey(s string) (Key, error) {
	var k Key
	t := strings.TrimSpace(s)
	if t == "" && s != "" {
		t = " " // одиночный пробел — клавиша Space
	}
	parts := strings.Split(t, "+")
	if strings.HasSuffix(t, "+") && (t == "+" || strings.HasSuffix(t, "++")) {
		// сама клавиша «+»: "+", "Ctrl++"
		parts = append(strings.Split(strings.TrimSuffix(t, "++"), "+"), "+")
		if t == "+" {
			parts = []string{"+"}
		}
	}
	for i, p := range parts {
		if p != " " {
			p = strings.TrimSpace(p)
		}
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
	if r, n := utf8.DecodeRuneInString(p); n == len(p) && r != utf8.RuneError && unicode.IsPrint(r) {
		if r < utf8.RuneSelf && unicode.IsLetter(r) {
			r = unicode.ToLower(r) // латиница — клавиша (Shift+a — «A»); прочие символы — как есть
		}
		return string(r)
	}
	return ""
}

// String — сочетание в записи Playwright: "Control+Shift+Enter".
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
	c, _ := utf8.DecodeRuneInString(k.Name)
	key = k.Name
	switch cc, ok := charCodes[c]; {
	case c >= 'a' && c <= 'z':
		code, vk = "Key"+strings.ToUpper(k.Name), int(c-'a'+'A')
		if k.Shift {
			key = strings.ToUpper(k.Name)
		}
	case c >= '0' && c <= '9':
		code, vk = "Digit"+k.Name, int(c)
	case ok:
		code, vk = cc.code, cc.vk
	}
	if plain {
		text = key
	}
	return
}

// charKey — клавиша, которой вводится символ при type: буква в верхнем
// регистре и символы над цифрами — с Shift; перевод строки — Enter; прочие
// символы (кириллица, эмодзи) — key и text без кода клавиши.
func charKey(r rune) Key {
	switch {
	case r == '\n' || r == '\r':
		return Key{Name: "Enter"}
	case r == '\t':
		return Key{Name: "Tab"}
	case r == ' ':
		return Key{Name: "Space"}
	case r >= 'A' && r <= 'Z':
		return Key{Shift: true, Name: string(unicode.ToLower(r))}
	}
	if cc, ok := charCodes[r]; ok {
		return Key{Shift: cc.shift, Name: string(r)}
	}
	return Key{Name: string(r)}
}
