// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gate4ai/sdk-go/manifest"
)

// Настройки коннектора в Web UI: поля выводятся из шаблонов ${setting:…} в
// возможностях манифеста (то, что коннектор реально использует) и дополняются
// подсказками для официальных коннекторов. Секретные поля — ввод значения,
// которое уходит в Vault, а в конфигурацию пишется алиас.

// Виды полей.
const (
	fieldText   = "text"
	fieldNumber = "number"
	fieldList   = "list"   // строки через запятую / с новой строки
	fieldSecret = "secret" // значение → Vault, в конфигурации — @алиас
)

// SettingField — поле формы настроек.
type SettingField struct {
	Key, Label, Kind, Help, Placeholder string
	Value                               string // текущее значение для формы
	Alias                               string // секрет: алиас из конфигурации
	HasSecret                           bool   // секрет: значение в Vault есть
}

type fieldHint struct{ Label, Kind, Help, Placeholder string }

var settingHints = map[string]map[string]fieldHint{
	"files": {
		"roots": {"Разрешённые папки", fieldList, "Коннектор видит только эти папки (и всё внутри). По одной на строку.", "~/Documents/notes"},
	},
	"browser": {
		"allowed_domains": {"Разрешённые сайты", fieldList, "Браузер открывает только эти сайты; *.example.com — все поддомены.", "*.example.com"},
	},
	"email": {
		"address":       {"Адрес почты", fieldText, "", "me@example.com"},
		"imap.host":     {"Сервер IMAP", fieldText, "", "imap.gmail.com"},
		"imap.port":     {"Порт IMAP", fieldNumber, "Обычно 993.", "993"},
		"imap.username": {"Логин", fieldText, "Обычно совпадает с адресом.", "me@example.com"},
		"imap.password": {"Пароль приложения", fieldSecret, "Не основной пароль, а пароль приложения из настроек почты. Хранится в Vault, коннектор его не видит.", ""},
		"smtp.host":     {"Сервер SMTP", fieldText, "", "smtp.gmail.com"},
		"smtp.port":     {"Порт SMTP", fieldNumber, "Обычно 465.", "465"},
		"smtp.password": {"Пароль SMTP", fieldSecret, "Пусто — тот же, что для IMAP.", ""},
	},
}

// mailProviders — серверы популярных почтовых сервисов (спецификация: Gmail,
// Яндекс, Mail.ru, iCloud, Fastmail).
var mailProviders = []struct{ ID, Title, IMAP, SMTP, Help string }{
	{"gmail", "Gmail", "imap.gmail.com", "smtp.gmail.com", "Пароль приложения: myaccount.google.com → Безопасность → Пароли приложений (нужна двухэтапная проверка)."},
	{"yandex", "Яндекс", "imap.yandex.ru", "smtp.yandex.ru", "Пароль приложения: id.yandex.ru → Безопасность → Пароли приложений → Почта."},
	{"mailru", "Mail.ru", "imap.mail.ru", "smtp.mail.ru", "Пароль для внешнего приложения: настройки Mail ID → Безопасность."},
	{"icloud", "iCloud", "imap.mail.me.com", "smtp.mail.me.com", "Пароль приложения: account.apple.com → Вход и безопасность → Пароли приложений."},
	{"fastmail", "Fastmail", "imap.fastmail.com", "smtp.fastmail.com", "App password: Settings → Privacy & Security → App passwords."},
}

var settingRe = regexp.MustCompile(`\$\{setting:([a-zA-Z0-9_.]+)\}`)

// settingFields — поля настроек коннектора по манифесту и текущим настройкам.
func settingFields(m manifest.Manifest, cur map[string]any) []SettingField {
	kinds := map[string]string{}
	add := func(s, kind string) {
		for _, mm := range settingRe.FindAllStringSubmatch(s, -1) {
			if _, ok := kinds[mm[1]]; !ok || kind == fieldSecret {
				kinds[mm[1]] = kind
			}
		}
	}
	c := m.Capabilities
	for _, n := range c.Network {
		add(n.Host, fieldText)
		add(n.Port, fieldNumber)
	}
	for _, s := range c.Secrets {
		add(s.Alias, fieldSecret)
		for _, b := range s.Bind {
			add(b, fieldText)
		}
	}
	if c.Browser != nil {
		for _, d := range c.Browser.Domains {
			add(d, fieldList)
		}
	}
	for _, f := range c.Files {
		add(f, fieldList)
	}
	hints := settingHints[m.Connector]
	for k, h := range hints {
		if _, ok := kinds[k]; !ok {
			kinds[k] = h.Kind
		}
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	// Порядок подсказок, затем по алфавиту.
	order := map[string]int{}
	for i, k := range hintOrder[m.Connector] {
		order[k] = i + 1
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, oj := order[keys[i]], order[keys[j]]
		if oi != oj {
			if oi == 0 {
				return false
			}
			if oj == 0 {
				return true
			}
			return oi < oj
		}
		return keys[i] < keys[j]
	})
	var out []SettingField
	for _, k := range keys {
		f := SettingField{Key: k, Label: k, Kind: kinds[k]}
		if h, ok := hints[k]; ok {
			f.Label, f.Help, f.Placeholder = h.Label, h.Help, h.Placeholder
			if h.Kind != "" {
				f.Kind = h.Kind
			}
		}
		v := getPath(cur, k)
		switch f.Kind {
		case fieldList:
			f.Value = strings.Join(toStrings(v), "\n")
		case fieldSecret:
			f.Alias, _ = v.(string)
		default:
			if v != nil {
				f.Value = fmt.Sprint(v)
			}
		}
		out = append(out, f)
	}
	return out
}

var hintOrder = map[string][]string{
	"email": {"address", "imap.host", "imap.port", "imap.username", "imap.password", "smtp.host", "smtp.port", "smtp.password"},
}

// getPath — значение по ключу с точками (imap.host → settings["imap"]["host"]).
func getPath(m map[string]any, key string) any {
	var cur any = m
	for _, p := range strings.Split(key, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

// setPath задаёт значение по ключу с точками (nil — удалить).
func setPath(m map[string]any, key string, v any) {
	parts := strings.Split(key, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	last := parts[len(parts)-1]
	if v == nil {
		delete(cur, last)
		return
	}
	cur[last] = v
}

// fieldValue — значение поля из формы для конфигурации.
func fieldValue(f SettingField, raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	switch f.Kind {
	case fieldList:
		return anyStrings(splitItems(raw)), nil
	case fieldNumber:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", f.Label, raw)
		}
		return n, nil
	}
	return raw, nil
}

// humanCapability — возможность коннектора человеческим языком.
func humanCapability(s string) string {
	s = settingRe.ReplaceAllStringFunc(s, func(m string) string {
		return "«" + settingRe.FindStringSubmatch(m)[1] + "»"
	})
	switch {
	case strings.HasPrefix(s, "files "):
		return "файлы: " + strings.TrimPrefix(s, "files ") + " — только в папках из настройки"
	case strings.HasPrefix(s, "browser on "):
		return "браузер: только сайты " + strings.TrimPrefix(s, "browser on ")
	case strings.HasPrefix(s, "network "):
		return "сеть: соединения с " + strings.TrimPrefix(s, "network ")
	case strings.HasPrefix(s, "secret "):
		return "секрет " + strings.Replace(strings.TrimPrefix(s, "secret "), " for ", " — подставляет gate4ai, только для ", 1) + " (коннектор значения не видит)"
	case strings.HasPrefix(s, "storage "):
		return "своё хранилище: " + strings.TrimPrefix(s, "storage ")
	}
	return s
}
