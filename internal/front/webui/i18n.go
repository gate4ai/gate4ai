// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/access"
)

// Языки Web UI: основной — английский (исходные строки в шаблонах и коде),
// русский — перевод (каталог ru в i18n_ru.go). Язык выбирается переключателем
// (cookie g4a_lang), без него — по Accept-Language браузера, иначе — UI.DefaultLang,
// иначе английский. Строки, которых нет в каталоге, показываются по-английски.

// Языки.
const (
	langEN = "en"
	langRU = "ru"
)

// langCookie — выбранный владельцем язык.
const langCookie = "g4a_lang"

// Langs — языки переключателя.
var Langs = []struct{ Code, Title string }{{langEN, "English"}, {langRU, "Русский"}}

func supported(l string) bool { return l == langEN || l == langRU }

// lang — язык запроса.
func (u *UI) lang(r *http.Request) string {
	if r != nil {
		if c, err := r.Cookie(langCookie); err == nil && supported(c.Value) {
			return c.Value
		}
		if l := acceptLang(r.Header.Get("Accept-Language")); l != "" {
			return l
		}
	}
	if supported(u.DefaultLang) {
		return u.DefaultLang
	}
	return langEN
}

// acceptLang — первый поддерживаемый язык из Accept-Language (в порядке заголовка;
// браузеры перечисляют языки по убыванию веса).
func acceptLang(h string) string {
	for _, part := range strings.Split(h, ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		base, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if supported(base) {
			return base
		}
	}
	return ""
}

// tr — перевод строки на язык l.
func tr(l, s string) string {
	if l == langRU {
		if v, ok := ru[s]; ok {
			return v
		}
	}
	return s
}

// Msg — сообщение для человека: английский формат и аргументы. Переводится при
// показе (Web UI на языке запроса), Error() — английский текст (журнал, CLI).
type Msg struct {
	F string
	A []any
}

// M — сообщение с форматом f.
func M(f string, a ...any) Msg { return Msg{F: f, A: a} }

func (m Msg) Error() string { return m.In(langEN) }

// In — текст на языке l; аргументы-сообщения тоже переводятся.
func (m Msg) In(l string) string {
	if len(m.A) == 0 {
		return tr(l, m.F)
	}
	args := make([]any, len(m.A))
	for i, a := range m.A {
		args[i] = text(l, a)
	}
	return fmt.Sprintf(tr(l, m.F), args...)
}

// Word — слово или фраза, которая переводится, когда стоит аргументом сообщения
// («allow» → «разрешать»). Обычные строки-аргументы (имена, пути) не переводятся.
type Word string

// text — значение для показа на языке l: Msg и Word переводятся, ошибки —
// через errView (понятный текст сетевых ошибок), остальное — как есть.
func text(l string, v any) any {
	switch t := v.(type) {
	case Msg:
		return t.In(l)
	case Word:
		return tr(l, string(t))
	case msgs:
		return t.in(l)
	case error:
		var m Msg
		if errors.As(t, &m) {
			return m.In(l)
		}
		return tr(l, t.Error())
	}
	return v
}

// say — строка для показа (flash, ошибка): Msg, Word, error или английская строка.
func say(l string, v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return tr(l, t)
	}
	return fmt.Sprint(text(l, v))
}

// langFuncs — функции шаблонов, зависящие от языка.
func langFuncs(l string) template.FuncMap {
	return template.FuncMap{
		"Lang": func() string { return l },
		// T — перевод строки; с аргументами — формат fmt.
		"T": func(s string, a ...any) string {
			if len(a) == 0 {
				return tr(l, s)
			}
			return M(s, a...).In(l)
		},
		// TH — перевод строки с разметкой, написанной в шаблоне (ссылки, <code>);
		// аргументы экранируются.
		"TH": func(s string, a ...any) template.HTML {
			args := make([]any, len(a))
			for i, x := range a {
				args[i] = template.HTMLEscapeString(fmt.Sprint(text(l, x)))
			}
			if len(a) == 0 {
				return template.HTML(tr(l, s))
			}
			return template.HTML(fmt.Sprintf(tr(l, s), args...))
		},
		"say":             func(v any) string { return say(l, v) },
		"ago":             func(t any) string { return agoIn(l, t) },
		"left":            func(t any) string { return leftIn(l, t) },
		"effect":          func(e string) string { return tr(l, or(effectWords[e], e)) },
		"class":           func(c string) string { return tr(l, or(classWords[c], c)) },
		"classGo":         func(c string) string { return tr(l, or(classVerbs[c], c)) },
		"via":             func(v string) string { return tr(l, or(viaWords[v], v)) },
		"channel":         func(v string) string { return tr(l, or(channelWords[v], v)) },
		"describe":        func(w string) string { s, _ := describeWhen(l, w); return s },
		"capRu":           func(c string) string { return humanCapability(l, c) },
		"mode":            func(m string) string { return tr(l, or(modeWords[m], m)) },
		"siteModes":       func() []string { return access.SiteModes },
		"folderModes":     func() []string { return access.FolderModes },
		"connSiteModes":   func() []string { return access.ConnectorSiteModes },
		"connFolderModes": func() []string { return access.ConnectorFolderModes },
		"errView":         func(msg string) ErrView { return errView(l, msg) },
		"title":           func(nav string) string { return tr(l, or(titles[nav], nav)) },
		"reason":          func(s string) string { return reasonText(l, s) },
		"dur":             func(s string) string { return tr(l, or(durWords[s], s)) },
		"version":         func(s string) string { return versionComment(l, s) },
	}
}

// durWords — сроки из выпадающих списков словами (таймаут подтверждения, хранение журнала).
var durWords = map[string]string{"15m": "15 minutes", "30m": "30 minutes", "1h": "1 hour", "2h": "2 hours", "8h": "8 hours", "1d": "1 day",
	"30d": "30 days", "90d": "90 days", "1y": "1 year", "3y": "3 years", "0s": "forever",
	"receipts": "Journal entries", "denied": "Denials", "evidence_other": "Evidence", "evidence_money": "Evidence of money actions"}
