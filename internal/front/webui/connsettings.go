// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
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
	fieldBool   = "bool"   // галочка
	fieldChoice = "choice" // выбор из Choices
)

// SettingField — поле формы настроек.
type SettingField struct {
	Key, Label, Kind, Help, Placeholder string
	Value                               string // текущее значение для формы
	Alias                               string // секрет: алиас из конфигурации
	HasSecret                           bool   // секрет: значение в Vault есть
	// Advanced — поле в «Расширенных настройках»: работает с умолчанием, нужно редко.
	Advanced bool
	Choices  []string // fieldChoice: значения (подписи — перевод значения)
}

type fieldHint struct {
	Label, Kind, Help, Placeholder string
	Advanced                       bool
	Choices                        []string
}

// settingHints — подписи и виды полей официальных коннекторов (английские
// исходники; перевод — каталог ru).
var settingHints = map[string]map[string]fieldHint{
	"files": {
		"roots":        {Label: "Allowed folders", Kind: fieldList, Help: "Agents see only these folders and what is inside. One per line.", Placeholder: "~/Documents/notes"},
		"allow_hidden": {Label: "Open hidden files and folders", Kind: fieldBool, Help: "Hidden files and folders inside the allowed folders (.ssh, .aws, .config…) are closed unless this is on. Keys and passwords stay closed anyway.", Advanced: true},
		"allow_paths":  {Label: "Open these protected paths", Kind: fieldList, Help: "Exact paths inside the allowed folders that are opened despite the protection (keys, hidden folders). One per line.", Placeholder: "~/Documents/notes/.obsidian", Advanced: true},
		"max_bytes":    {Label: "Largest file to read, bytes", Kind: fieldNumber, Help: "1 MiB by default.", Placeholder: "1048576", Advanced: true},
	},
	"browser": {
		"allowed_domains":    {Label: "Allowed sites", Kind: fieldList, Help: "The browser opens only these sites; *.example.com means all subdomains.", Placeholder: "*.example.com"},
		"mode":               {Label: "Site list mode", Kind: fieldChoice, Choices: []string{"allowlist", "blocklist"}, Help: "allowlist — only the allowed sites (default); blocklist — every site except the blocked ones.", Advanced: true},
		"blocked":            {Label: "Blocked sites", Kind: fieldList, Help: "For the blocklist mode. Addresses of this computer and the local network are always closed.", Placeholder: "*.example.org", Advanced: true},
		"model":              {Label: "Model for data extraction", Kind: fieldText, Help: "Provider and model, e.g. openai/gpt-5.4-mini.", Placeholder: "openai/gpt-5.4-mini", Advanced: true},
		"model_key":          {Label: "Model key", Kind: fieldSecret, Help: "Kept in gate4.ai passwords; given only to the browser package.", Advanced: true},
		"profile_dir":        {Label: "Chrome profile", Kind: fieldText, Help: "A dedicated profile; ~/.gate4ai-chrome by default.", Placeholder: "~/.gate4ai-chrome", Advanced: true},
		"chrome_path":        {Label: "Path to Chrome", Kind: fieldText, Help: "Empty — find the installed Chrome.", Advanced: true},
		"cdp_url":            {Label: "CDP address", Kind: fieldText, Help: "An already running Chrome; empty — the package starts it.", Advanced: true},
		"engine":             {Label: "Engine", Kind: fieldChoice, Choices: []string{"stagehand", "cdp"}, Help: "stagehand (default) or cdp.", Advanced: true},
		"download_types":     {Label: "File types agents may download", Kind: fieldList, Help: "browser.download returns only these: application/pdf, image/*, .csv. Empty — any type.", Placeholder: "application/pdf", Advanced: true},
		"max_download_bytes": {Label: "Largest file to download, bytes", Kind: fieldNumber, Help: "20 MiB at most (the default).", Placeholder: "20971520", Advanced: true},
		"download_dir":       {Label: "Downloads folder", Kind: fieldText, Help: "Where Chrome saves files downloaded in its window, without a Save as dialog; empty — your Downloads folder.", Placeholder: "~/Downloads", Advanced: true},
	},
	"email": {
		"address":       {Label: "Email address", Kind: fieldText, Placeholder: "me@example.com"},
		"imap.password": {Label: "App password", Kind: fieldSecret, Help: "Not your main password but an app password from the mail settings. gate4.ai keeps it; the connector never sees it."},
		"imap.host":     {Label: "IMAP server", Kind: fieldText, Placeholder: "imap.gmail.com", Help: "Filled in for the chosen mail service.", Advanced: true},
		"imap.port":     {Label: "IMAP port", Kind: fieldNumber, Help: "Usually 993.", Placeholder: "993", Advanced: true},
		"imap.username": {Label: "Login", Kind: fieldText, Help: "Usually the same as the address.", Placeholder: "me@example.com", Advanced: true},
		"smtp.host":     {Label: "SMTP server", Kind: fieldText, Placeholder: "smtp.gmail.com", Help: "Filled in for the chosen mail service.", Advanced: true},
		"smtp.port":     {Label: "SMTP port", Kind: fieldNumber, Help: "Usually 465.", Placeholder: "465", Advanced: true},
		"smtp.password": {Label: "SMTP password", Kind: fieldSecret, Help: "Empty — the same as for IMAP.", Advanced: true},
	},
}

// mailProviders — серверы популярных почтовых сервисов (спецификация: Gmail,
// Яндекс, Mail.ru, iCloud, Fastmail).
var mailProviders = []struct{ ID, Title, IMAP, SMTP, Help string }{
	{"gmail", "Gmail", "imap.gmail.com", "smtp.gmail.com", "App password: myaccount.google.com → Security → App passwords (2-Step Verification is required)."},
	{"yandex", "Yandex", "imap.yandex.ru", "smtp.yandex.ru", "App password: id.yandex.ru → Security → App passwords → Mail."},
	{"mailru", "Mail.ru", "imap.mail.ru", "smtp.mail.ru", "Password for an external app: Mail ID settings → Security."},
	{"icloud", "iCloud", "imap.mail.me.com", "smtp.mail.me.com", "App password: account.apple.com → Sign-In and Security → App-Specific Passwords."},
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
	// Параметры вызовов других коннекторов: шаблон в списке — список, иначе текст.
	var params func(v any, inList bool)
	params = func(v any, inList bool) {
		switch t := v.(type) {
		case string:
			if inList {
				add(t, fieldList)
			} else {
				add(t, fieldText)
			}
		case map[string]any:
			for _, x := range t {
				params(x, false)
			}
		case []any:
			for _, x := range t {
				params(x, true)
			}
		}
	}
	for _, sc := range c.Services {
		params(sc.Params, false)
	}
	for _, r := range c.Reveal {
		add(r, fieldSecret)
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
			f.Label, f.Help, f.Placeholder, f.Advanced, f.Choices = h.Label, h.Help, h.Placeholder, h.Advanced, h.Choices
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
	"files":   {"roots", "allow_hidden", "allow_paths", "max_bytes"},
	"browser": {"allowed_domains", "mode", "blocked", "model", "model_key", "profile_dir", "chrome_path", "cdp_url", "engine", "download_types", "max_download_bytes", "download_dir"},
	"email":   {"address", "imap.password", "imap.host", "imap.port", "imap.username", "smtp.host", "smtp.port", "smtp.password"},
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
			return nil, M("%s: %q is not a number", Word(f.Label), raw)
		}
		return n, nil
	case fieldBool:
		if raw == "1" || raw == "on" || raw == "true" {
			return true, nil
		}
		return nil, nil
	case fieldChoice:
		if len(f.Choices) > 0 && !slices.Contains(f.Choices, raw) {
			return nil, M("%s: choose one of %s", Word(f.Label), strings.Join(f.Choices, ", "))
		}
	}
	return raw, nil
}

// humanCapability — возможность коннектора человеческим языком на языке l.
func humanCapability(l, s string) string {
	s = settingRe.ReplaceAllStringFunc(s, func(m string) string {
		return "“" + settingRe.FindStringSubmatch(m)[1] + "”"
	})
	var m Msg
	switch {
	case strings.HasPrefix(s, "files "):
		m = M("files: %s — only in the folders from the settings", strings.TrimPrefix(s, "files "))
	case strings.HasPrefix(s, "browser on "):
		m = M("browser: only the sites %s", strings.TrimPrefix(s, "browser on "))
	case strings.HasPrefix(s, "network "):
		m = M("network: connections to %s", strings.TrimPrefix(s, "network "))
	case strings.HasPrefix(s, "secret "):
		alias, host, _ := strings.Cut(strings.TrimPrefix(s, "secret "), " for ")
		m = M("password %s — gate4.ai enters it, only for %s (the connector never sees it)", alias, host)
	case strings.HasPrefix(s, "storage "):
		m = M("own storage: %s", strings.TrimPrefix(s, "storage "))
	default:
		return s
	}
	return m.In(l)
}
