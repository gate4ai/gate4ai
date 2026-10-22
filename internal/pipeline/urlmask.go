// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// mask — известные секреты (Vault) и одноразовые коды и токены в адресах:
// OAuth-редирект `…/auth?code=…` не должен лечь в журнал и ответ агенту.
func (p *Pipeline) mask(v any) any { return maskURLs(p.Secrets.Mask(v)) }

// secretParam — параметры адреса с одноразовыми кодами, токенами и сессиями.
var secretParam = regexp.MustCompile(`(?i)^(code|token|access_token|id_token|refresh_token|state|ticket|sid|session.*|auth.*|.*_token)$`)

var urlInText = regexp.MustCompile(`https?://[^\s"'<>]+`)

// MaskURLText — текст с *** вместо одноразовых кодов и токенов в адресах
// (для уведомлений каналов подтверждения: Telegram, рабочий стол).
func MaskURLText(s string) string { return maskURLs(s).(string) }

// maskURLs заменяет значения секретных параметров во всех адресах внутри v
// (строки, списки, объекты после JSON).
func maskURLs(v any) any {
	switch t := v.(type) {
	case string:
		if !strings.ContainsAny(t, "?#") {
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

// maskURL — адрес с *** вместо значений секретных параметров в query и во
// фрагменте (implicit flow и response_mode=fragment: #access_token=…).
func maskURL(u string) string {
	rest, frag, hasFrag := strings.Cut(u, "#")
	base, query, hasQuery := strings.Cut(rest, "?")
	out := base
	if hasQuery {
		out += "?" + maskParams(query)
	}
	if hasFrag {
		if strings.Contains(frag, "=") {
			frag = maskParams(frag)
		}
		out += "#" + frag
	}
	return out
}

// maskParams — k=v&… с *** вместо значений секретных параметров.
func maskParams(query string) string {
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
	return strings.Join(parts, "&")
}

// evidenceData — данные доказательства для хранения: текстовые (text/*, JSON,
// XML, HTML, пустой MIME) проходят маскирование секретов и адресов, как ответы;
// изображения и прочие двоичные — как есть (снимки уже с закрашенными полями).
func (p *Pipeline) evidenceData(e abi.EvidenceItem) []byte {
	m := strings.ToLower(e.MIME)
	if m == "" || strings.HasPrefix(m, "text/") || strings.Contains(m, "json") || strings.Contains(m, "xml") {
		return []byte(p.maskString(string(e.Data)))
	}
	return e.Data
}
