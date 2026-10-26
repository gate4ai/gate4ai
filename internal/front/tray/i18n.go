// SPDX-License-Identifier: AGPL-3.0-only

package tray

import (
	"os"
	"strings"
)

// Строки трея: русские при русской локали, иначе английские (CLI — на английском).
var messages = map[string][2]string{ // ключ → {en, ru}
	"not_running":   {"gate4.ai: daemon is not running", "gate4.ai: daemon не запущен"},
	"running":       {"gate4.ai is running · receipts: %d", "gate4.ai работает · квитанций: %d"},
	"waiting":       {"gate4.ai: %d waiting for a decision", "gate4.ai: %d ждут решения"},
	"no_pending":    {"No approval requests", "Нет запросов подтверждения"},
	"why":           {"Why: ", "Почему: "},
	"allow":         {"Allow", "Разрешить"},
	"deny":          {"Deny", "Отклонить"},
	"details":       {"Details in the Web UI", "Подробнее в Web UI"},
	"decide_in_ui":  {"Review the changes and decide in the Web UI", "Просмотреть правки и решить в Web UI"},
	"open_ui":       {"Open Web UI", "Открыть Web UI"},
	"password":      {"Web UI password (until the first sign-in): %s", "Пароль Web UI (до первого входа): %s"},
	"browser":       {"Browser: %d agent sessions", "Браузер: сеансов агентов — %d"},
	"browser_wait":  {"Browser: an agent waits for you", "Браузер: агент ждёт вас"},
	"start":         {"Start daemon", "Запустить daemon"},
	"quit":          {"Quit tray", "Выход из трея"},
	"need_approval": {"gate4.ai: approval needed", "gate4.ai: нужно подтверждение"},
	"not_decided":   {"gate4.ai: decision not saved", "gate4.ai: решение не принято"},
	"ui_failed":     {"gate4.ai: Web UI not opened", "gate4.ai: Web UI не открыт"},
	"start_failed":  {"gate4.ai: daemon did not start", "gate4.ai: daemon не запустился"},
	"started":       {"gate4.ai: daemon is running", "gate4.ai: daemon запущен"},
	"started_body":  {"It runs until logout. Start it at every login: gate4ai service install", "Работает до выхода из системы. Автозапуск при входе: gate4ai service install"},
}

// Russian — язык интерфейса трея: GATE4AI_LANG, затем LC_ALL, LC_MESSAGES, LANGUAGE, LANG.
func Russian(getenv func(string) string) bool {
	for _, k := range []string{"GATE4AI_LANG", "LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if v := getenv(k); v != "" {
			return strings.HasPrefix(strings.ToLower(v), "ru")
		}
	}
	return false
}

var ru = Russian(os.Getenv)

// T — строка трея на языке интерфейса.
func T(key string) string {
	m, ok := messages[key]
	if !ok {
		return key
	}
	if ru {
		return m[1]
	}
	return m[0]
}
