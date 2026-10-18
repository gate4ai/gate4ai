// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"net/url"
	"regexp"
	"strings"
)

// mask — известные секреты (Vault) и одноразовые коды и токены в адресах:
// OAuth-редирект `…/auth?code=…` не должен лечь в журнал и ответ агенту.
func (p *Pipeline) mask(v any) any { return maskURLs(p.Secrets.Mask(v)) }

// secretParam — параметры адреса с одноразовыми кодами, токенами и сессиями.
var secretParam = regexp.MustCompile(`(?i)^(code|token|access_token|id_token|refresh_token|state|ticket|sid|session.*|auth.*|.*_token)$`)

var urlInText = regexp.MustCompile(`https?://[^\s"'<>]+`)

// maskURLs заменяет значения секретных параметров во всех адресах внутри v
// (строки, списки, объекты после JSON).
func maskURLs(v any) any {
	switch t := v.(type) {
	case string:
		if !strings.Contains(t, "?") {
			return t
		}
		return urlInText.ReplaceAllStringFunc(t, maskURL)
	case map[string]any:
		for k, x := range t {
			t[k] = maskURLs(x)
		}
	case []any:
		for i := range t {
			t[i] = maskURLs(t[i])
		}
	}
	return v
}

// maskURL — адрес с *** вместо значений секретных параметров.
func maskURL(u string) string {
	base, query, ok := strings.Cut(u, "?")
	if !ok {
		return u
	}
	query, frag, hasFrag := strings.Cut(query, "#")
	parts := strings.Split(query, "&")
	for i, part := range parts {
		k, _, hasVal := strings.Cut(part, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if hasVal && secretParam.MatchString(name) {
			parts[i] = k + "=***"
		}
	}
	out := base + "?" + strings.Join(parts, "&")
	if hasFrag {
		out += "#" + frag
	}
	return out
}
