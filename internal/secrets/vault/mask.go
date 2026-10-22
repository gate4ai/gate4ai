// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"regexp"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs/mask"
)

var (
	cardRe     = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	passportRe = regexp.MustCompile(`\b\d{2}\s?\d{2}\s\d{6}\b`)
)

// sensitiveKeys — имена полей, значения которых маскируются целиком.
var sensitiveKeys = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true, "api_key": true, "apikey": true,
	"cvv": true, "cvc": true, "pin": true, "card_number": true, "passport": true, "authorization": true,
}

// Mask маскирует известные секреты (за один проход, вместе с производными
// формами — base64, URL-, JSON- и HTML-экранированием), номера карт (с проверкой Луна, видны
// последние 4 цифры), серии и номера паспортов, поля с чувствительными именами.
func (v *Vault) Mask(x any) any {
	v.mu.RLock()
	secrets := make([]string, 0, len(v.values))
	for _, s := range v.values {
		if len(s) >= 4 {
			secrets = append(secrets, s)
		}
	}
	now := time.Now()
	for s, until := range v.transient {
		if len(s) >= 4 && now.Before(until) {
			secrets = append(secrets, s)
		}
	}
	v.mu.RUnlock()
	return maskAny(x, mask.New(secrets...))
}

// MaskString — Mask для строки.
func (v *Vault) MaskString(s string) string { return v.Mask(s).(string) }

func maskAny(x any, secrets *mask.Set) any {
	switch t := x.(type) {
	case string:
		return maskString(t, secrets)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if sensitiveKeys[strings.ToLower(k)] {
				if s, ok := val.(string); ok && s != "" {
					out[k] = "***"
					continue
				}
			}
			out[k] = maskAny(val, secrets)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = maskAny(val, secrets)
		}
		return out
	}
	return x
}

func maskString(s string, secrets *mask.Set) string {
	s = secrets.String(s, "***")
	s = cardRe.ReplaceAllStringFunc(s, func(m string) string {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, m)
		if len(digits) < 13 || len(digits) > 19 || !luhn(digits) {
			return m
		}
		return "**** " + digits[len(digits)-4:]
	})
	s = passportRe.ReplaceAllString(s, "**** ******")
	return s
}

func luhn(d string) bool {
	sum := 0
	alt := false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}
