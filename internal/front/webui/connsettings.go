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
		"allow_hidden": {Label: "Open hidden files and folders", Kind: fieldBool, Help: "Hidden files and folders inside the allowed folders (.ssh, .aws, .config…) are closed unless this is on. Keys and passwords stay closed anyway.", Advanced: true},
		"allow_paths":  {Label: "Open these protected paths", Kind: fieldList, Help: "Exact paths inside the allowed folders that are opened despite the protection (keys, hidden folders). One per line.", Placeholder: "~/Documents/notes/.obsidian", Advanced: true},
		"max_bytes":    {Label: "Largest file to read, bytes", Kind: fieldNumber, Help: "1 MiB by default.", Placeholder: "1048576", Advanced: true},
	},
	"browser": {
		"max_text_chars":     {Label: "Page text an agent gets, characters", Kind: fieldNumber, Help: "The default limit of browser.read, observe and the responses in network; 30 000 by default. An agent may ask for more in a call (max_chars, up to 1 000 000).", Placeholder: "30000", Advanced: true},
		"redact_params":      {Label: "Hidden parameters and fields", Kind: fieldList, Help: "URL parameters and JSON fields hidden from agents in network and in responses (identifiers, personal data). One per line.", Placeholder: "patient_id", Advanced: true},
		"profile_dir":        {Label: "Chrome profile", Kind: fieldText, Help: "A dedicated profile; ~/.gate4ai-chrome by default.", Placeholder: "~/.gate4ai-chrome", Advanced: true},
		"chrome_path":        {Label: "Path to Chrome", Kind: fieldText, Help: "Empty — find the installed Chrome.", Advanced: true},
		"cdp_url":            {Label: "CDP address", Kind: fieldText, Help: "An already running Chrome; empty — the package starts it.", Advanced: true},
		"display":            {Label: "Screen", Kind: fieldChoice, Choices: []string{"auto", "virtual", "headless", "host"}, Help: "auto (default): your screen; on a server without one — a virtual screen (Xvfb), without Xvfb — no screen (headless, sites see it more often). You watch the sessions in Browser either way.", Advanced: true},
		"sandbox":            {Label: "Chrome sandbox", Kind: fieldChoice, Choices: []string{"auto", "on", "off"}, Help: "auto (default): on, except when the kernel forbids it to this Chrome (Ubuntu 24.04+ with AppArmor and a Chrome outside the distribution packages, such as Chrome for Testing from gate4ai browser setup --user) — then Chrome starts with --no-sandbox and Browser shows a warning. on: never without the sandbox (Chrome does not start when it is forbidden). off: always without it.", Advanced: true},
		"download_types":     {Label: "File types agents may download", Kind: fieldList, Help: "browser.download returns only these: application/pdf, image/*, .csv. Empty — any type.", Placeholder: "application/pdf", Advanced: true},
		"max_download_bytes": {Label: "Largest file to download, bytes", Kind: fieldNumber, Help: "20 MiB at most (the default).", Placeholder: "20971520", Advanced: true},
		"download_dir":       {Label: "Downloads folder", Kind: fieldText, Help: "Where Chrome saves files downloaded in its window, without a Save as dialog; empty — your Downloads folder.", Placeholder: "~/Downloads", Advanced: true},
	},
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
	for _, st := range m.Sites {
		add(st.Domain, fieldList)
	}
	for _, r := range c.Reveal {
		add(r, fieldSecret)
	}
	for _, f := range m.Folders {
		add(f.Path, fieldList)
	}
	// Списки доступа встроенных компонентов и строки коннекторов правятся своими
	// таблицами, не полями настроек.
	for _, k := range []string{"sites", "folders", "roots", "allowed_domains", "blocked", "mode"} {
		delete(kinds, k)
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
	"files":   {"allow_hidden", "allow_paths", "max_bytes"},
	"browser": {"max_text_chars", "redact_params", "profile_dir", "chrome_path", "cdp_url", "display", "sandbox", "download_types", "max_download_bytes", "download_dir"},
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
