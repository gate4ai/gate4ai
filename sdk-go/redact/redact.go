// SPDX-License-Identifier: Apache-2.0

// Package redact — маскирование одноразовых кодов и токенов в адресах: одно
// правило для хоста (журнал, ответ агенту, уведомления) и коннекторов.
package redact

import (
	"net/url"
	"regexp"
	"strings"
)

// secretParam — параметры адреса с одноразовыми кодами, токенами и сессиями.
var secretParam = regexp.MustCompile(`(?i)^(code|token|access_token|id_token|refresh_token|state|ticket|sid|session.*|auth.*|.*_token)$`)

// SecretParam — параметр адреса, значение которого не показывается.
func SecretParam(k string) bool { return secretParam.MatchString(k) }

// URL заменяет на *** значения параметров запроса, для которых SecretParam
// или hide истинно; порядок и остальные параметры — как были. Так же
// разбирается фрагмент: токены implicit-входа OAuth и OIDC приходят в нём
// (#access_token=…&id_token=…), у маршрутов SPA — после «?» (#/cb?code=…).
func URL(u string, hide func(string) bool) string {
	rest, frag, hasFrag := strings.Cut(u, "#")
	base, query, hasQuery := strings.Cut(rest, "?")
	out := base
	if hasQuery {
		out += "?" + Pairs(query, hide)
	}
	if hasFrag {
		if route, q, ok := strings.Cut(frag, "?"); ok {
			frag = route + "?" + Pairs(q, hide)
		} else if strings.Contains(frag, "=") {
			frag = Pairs(frag, hide)
		}
		out += "#" + frag
	}
	return out
}

// Pairs — то же для строки k=v&k=v.
func Pairs(query string, hide func(string) bool) string {
	parts := strings.Split(query, "&")
	for i, p := range parts {
		k, _, hasVal := strings.Cut(p, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if hasVal && (SecretParam(name) || (hide != nil && hide(name))) {
			parts[i] = k + "=***"
		}
	}
	return strings.Join(parts, "&")
}
