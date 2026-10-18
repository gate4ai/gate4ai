// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"sort"
	"strings"

	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/secrets/vault"
)

// SecretUse — место, где используется секрет Vault (колонка «Используется» на /vault).
type SecretUse struct {
	Text string // «Telegram-бот (подтверждения)», «коннектор email»
	Link string // страница Web UI с этой настройкой, без Base
}

// secretUses собирает ссылки на алиасы Vault из разобранной конфигурации и
// манифестов установленных коннекторов — те же места, откуда ядро берёт
// значения: подтверждения в Telegram, relay, Egress Proxy, возможности
// коннекторов (secrets, reveal) по их настройкам. Ключ — алиас с «@».
func secretUses(cfg *config.Config, ms []manifest.Manifest) map[string][]SecretUse {
	out := map[string][]SecretUse{}
	add := func(alias string, u SecretUse) {
		n, err := vault.Norm(strings.TrimSpace(alias))
		if err != nil {
			return
		}
		a := "@" + n
		for _, x := range out[a] {
			if x == u {
				return
			}
		}
		out[a] = append(out[a], u)
	}
	if t := cfg.Approvals.Telegram; t != nil && t.Token != "" {
		u := SecretUse{"Telegram-бот (подтверждения)", "/approvals/telegram"}
		if !cfg.ChannelOn("telegram") {
			u.Text += ", канал выключен"
		}
		add(t.Token, u)
	}
	if r := cfg.Relay; r.Token != "" {
		add(r.Token, SecretUse{"relay (подключение устройства)", "/settings#relay"})
	}
	for _, s := range cfg.Egress.Secrets {
		if s.Secret != "" {
			add(s.Secret, SecretUse{"Egress Proxy: " + s.Domain, ""})
		}
	}
	if k := cfg.Browser.ModelKey; k != "" { // раздел browser до v0.6, переносится в connectors.browser
		add(k, SecretUse{"браузер: ключ модели", ""})
	}
	installed := map[string]manifest.Manifest{}
	for _, m := range ms {
		installed[m.Connector] = m
	}
	names := make([]string, 0, len(cfg.Connectors)+len(ms))
	for n := range installed {
		names = append(names, n)
	}
	for n := range cfg.Connectors {
		if _, ok := installed[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		settings := cfg.Connectors[name]
		u := SecretUse{"коннектор " + name, "/connectors/" + name}
		m, ok := installed[name]
		if !ok {
			u = SecretUse{"коннектор " + name + " (не установлен)", ""}
		}
		// Возможности манифеста с подставленными настройками экземпляра.
		var tmpls []string
		for _, s := range m.Capabilities.Secrets {
			tmpls = append(tmpls, s.Alias)
		}
		tmpls = append(tmpls, m.Capabilities.Reveal...)
		for _, t := range tmpls {
			for _, a := range manifest.Expand(t, settings) {
				if strings.HasPrefix(a, "@") {
					add(a, u)
				}
			}
		}
		// Алиасы в настройках (поля-секреты Web UI пишут туда «@алиас»).
		settingAliases(settings, func(a string) { add(a, u) })
	}
	return out
}

// settingAliases вызывает f для каждой строки «@…» в настройках коннектора.
func settingAliases(v any, f func(string)) {
	switch t := v.(type) {
	case string:
		if strings.HasPrefix(t, "@") {
			f(t)
		}
	case map[string]any:
		for _, x := range t {
			settingAliases(x, f)
		}
	case []any:
		for _, x := range t {
			settingAliases(x, f)
		}
	case []string:
		for _, x := range t {
			settingAliases(x, f)
		}
	}
}
