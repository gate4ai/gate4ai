// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
)

// personalKeys — поля ответов API и форм, чьи значения — личные данные
// (псевдоним, имя, телефон, документ): в режиме auto значение заменяется
// целиком, где бы оно ни встретилось дальше.
var personalKeys = map[string]bool{
	"alias": true, "login": true, "username": true, "user_name": true, "nickname": true, "nick": true, "handle": true,
	"email": true, "e_mail": true, "mail": true, "phone": true, "mobile": true, "tel": true,
	"fullname": true, "full_name": true, "first_name": true, "firstname": true, "last_name": true, "lastname": true,
	"middle_name": true, "surname": true, "given_name": true, "family_name": true, "display_name": true,
	"birthday": true, "birth_date": true, "birthdate": true, "passport": true, "inn": true, "snils": true, "address": true,
}

// Scrubber заменяет личные данные в записи сеанса (gate4ai fixture scrub):
// точные пары real → fake и, в режиме Auto, почту, телефоны, длинные номера и
// значения полей personalKeys. Одно значение — одна и та же замена везде.
type Scrubber struct {
	Pairs map[string]string
	Auto  bool
	fakes map[string]string // найденное автоматически → замена
	// Count — сколько замен сделано.
	Count int
}

// NewScrubber — scrubber с парами (nil — только auto).
func NewScrubber(pairs map[string]string, auto bool) *Scrubber {
	if pairs == nil {
		pairs = map[string]string{}
	}
	return &Scrubber{Pairs: pairs, Auto: auto, fakes: map[string]string{}}
}

// Seen — исходные значения, которые заменялись (проверка, что их не осталось).
func (s *Scrubber) Seen() map[string]bool {
	out := map[string]bool{}
	for k := range s.Pairs {
		out[k] = true
	}
	for k := range s.fakes {
		out[k] = true
	}
	return out
}

// Walk заменяет личные данные в документе JSON (map, slice, string); в режиме
// Auto сначала собирает значения полей personalKeys, чтобы заменить их и там,
// где они встретились раньше поля.
func (s *Scrubber) Walk(v any) any {
	if s.Auto {
		s.collect(v)
	}
	return s.walk(v)
}

// collect — значения личных полей (alias, email, …) → замены.
func (s *Scrubber) collect(v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(t)) {
			if str, ok := t[k].(string); ok && personalKeys[strings.ToLower(k)] && len([]rune(strings.TrimSpace(str))) >= 2 {
				if _, done := s.fakes[str]; !done && !s.isFake(str) {
					s.fakes[str] = fmt.Sprintf("%s_%d", strings.ToLower(k), len(s.fakes)+1)
				}
				continue
			}
			s.collect(t[k])
		}
	case []any:
		for _, x := range t {
			s.collect(x)
		}
	}
}

// isFake — значение уже похоже на замену (user1@example.com, alias_2).
func (s *Scrubber) isFake(v string) bool {
	for _, f := range s.fakes {
		if f == v {
			return true
		}
	}
	return false
}

func (s *Scrubber) walk(v any) any {
	switch t := v.(type) {
	case string:
		return s.text(t)
	case map[string]any:
		// По порядку ключей: одинаковые замены при каждом запуске.
		out := map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(t)) {
			out[s.text(k)] = s.walk(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = s.walk(t[i])
		}
	case float64:
		// Длинный номер числом (id пользователя, номер заказа) — тоже личное.
		if s.Auto && t == float64(int64(t)) && t >= 1e7 {
			str := s.text(strconv.FormatInt(int64(t), 10))
			if n, err := strconv.ParseInt(str, 10, 64); err == nil {
				return float64(n)
			}
		}
	}
	return v
}

func (s *Scrubber) text(t string) string {
	// Сначала длинные значения: «Иванов Иван» раньше «Иван».
	all := map[string]string{}
	maps.Copy(all, s.fakes)
	maps.Copy(all, s.Pairs)
	keys := slices.Collect(maps.Keys(all))
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j]) || len(keys[i]) == len(keys[j]) && keys[i] < keys[j]
	})
	for _, k := range keys {
		if n := strings.Count(t, k); n > 0 {
			t, s.Count = strings.ReplaceAll(t, k, all[k]), s.Count+n
		}
	}
	if !s.Auto {
		return t
	}
	t = emailRe.ReplaceAllStringFunc(t, func(m string) string { return s.fake(m, "user%d@example.com") })
	t = phoneRe.ReplaceAllStringFunc(t, func(m string) string { return s.fake(m, "+7 900 000-00-%02d") })
	return longRe.ReplaceAllStringFunc(t, func(m string) string {
		if strings.Trim(m, "0") == "" || len(strings.TrimLeft(m, "0")) <= 2 {
			return m // уже замена
		}
		return s.fake(m, "%0"+fmt.Sprint(len(m))+"d")
	})
}

// fake — одна и та же замена для одного значения.
func (s *Scrubber) fake(real, format string) string {
	if f, ok := s.fakes[real]; ok {
		return f
	}
	if s.isFake(real) {
		return real
	}
	f := fmt.Sprintf(format, len(s.fakes)+1)
	s.fakes[real] = f
	s.Count++
	return f
}

// ScrubFixture заменяет личные данные в записи сеанса (как fixture scrub --auto).
func ScrubFixture(fx hosttest.Fixture) hosttest.Fixture {
	raw, _ := json.Marshal(fx)
	var doc any
	_ = json.Unmarshal(raw, &doc)
	doc = NewScrubber(nil, true).Walk(doc)
	raw, _ = json.Marshal(doc)
	var out hosttest.Fixture
	_ = json.Unmarshal(raw, &out)
	return out
}
