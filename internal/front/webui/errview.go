// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"regexp"
	"strings"
)

// ErrView — ошибка для окна Web UI: понятный текст и, если он заменил
// технический, исходный текст под «Подробности».
type ErrView struct {
	Text, Detail string
}

var (
	reLookup = regexp.MustCompile(`lookup ([^\s:]+)`)
	reURL    = regexp.MustCompile(`https?://([^\s/:"]+)`)
)

// errView переводит частые сетевые ошибки Go на человеческий язык:
// «dial tcp: lookup relay.gate4.ai …: i/o timeout» → «Нет связи с relay.gate4.ai…».
// Остальные ошибки показываются как есть.
func errView(msg string) ErrView {
	host := ""
	if m := reLookup.FindStringSubmatch(msg); m != nil {
		host = m[1]
	} else if m := reURL.FindStringSubmatch(msg); m != nil {
		host = m[1]
	}
	where := "сервером"
	if host != "" {
		where = host
	}
	var text string
	switch {
	case strings.Contains(msg, "lookup ") && (strings.Contains(msg, "no such host") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "server misbehaving")):
		text = "Нет связи с " + where + ": адрес не найден (DNS не ответил). Проверьте подключение к интернету и адрес."
	case strings.Contains(msg, "connection refused"):
		text = "Нет связи с " + where + ": подключение отклонено. Проверьте адрес и что сервер запущен."
	case strings.Contains(msg, "network is unreachable") || strings.Contains(msg, "no route to host"):
		text = "Нет связи с " + where + ": сеть недоступна. Проверьте подключение к интернету."
	case strings.Contains(msg, "x509:") || strings.Contains(msg, "tls: "):
		text = "Не удалось установить защищённое соединение с " + where + ": проблема с сертификатом."
	case strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "Client.Timeout"):
		text = "Нет связи с " + where + ": сервер не ответил вовремя. Проверьте подключение к интернету и попробуйте ещё раз."
	default:
		return ErrView{Text: msg}
	}
	return ErrView{Text: text, Detail: msg}
}
