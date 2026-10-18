// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"reflect"
	"testing"

	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
)

func TestSecretUses(t *testing.T) {
	cfg := &config.Config{
		Approvals: config.Approvals{Channels: []string{"desktop", "telegram"}, Telegram: &config.Telegram{Token: "@telegram.bot", OwnerID: 1}},
		Relay:     config.Relay{URL: "https://relay.example", DeviceID: "d", Token: "@relay.token"},
		Egress:    config.Egress{Secrets: []config.EgressSecret{{Domain: "api.example", Header: "Authorization", Placeholder: "X", Secret: "@api.key"}}},
		Connectors: map[string]map[string]any{
			"email": {"imap": map[string]any{"host": "imap.example", "password": "@email.imap.password"}},
			"jira":  {"token": "@jira.acme"},
			"old":   {"key": "@old.key"},
		},
	}
	ms := []manifest.Manifest{
		{Connector: "email", Capabilities: manifest.Capabilities{Secrets: []manifest.SecretCap{{Alias: "${setting:imap.password}", Bind: []string{"imaps://${setting:imap.host}:993"}}}}},
		{Connector: "jira", Capabilities: manifest.Capabilities{Secrets: []manifest.SecretCap{{Alias: "@jira.fixed", Bind: []string{"https://jira.example"}}}}},
		{Connector: "browser", Capabilities: manifest.Capabilities{Reveal: []string{"${setting:model_key}"}}},
	}
	got := secretUses(cfg, ms)
	want := map[string][]SecretUse{
		"@telegram.bot":        {{"Telegram-бот (подтверждения)", "/approvals/telegram"}},
		"@relay.token":         {{"relay (подключение устройства)", "/settings#relay"}},
		"@api.key":             {{"Egress Proxy: api.example", ""}},
		"@email.imap.password": {{"коннектор email", "/connectors/email"}},
		"@jira.fixed":          {{"коннектор jira", "/connectors/jira"}},
		"@jira.acme":           {{"коннектор jira", "/connectors/jira"}},
		"@old.key":             {{"коннектор old (не установлен)", ""}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if _, ok := got["@unused"]; ok {
		t.Fatal("unused secret reported as used")
	}

	// Канал выключен — токен всё равно указан, с пометкой.
	cfg.Approvals.Channels = []string{"desktop"}
	if u := secretUses(cfg, nil)["@telegram.bot"]; len(u) != 1 || u[0].Text != "Telegram-бот (подтверждения), канал выключен" {
		t.Fatalf("telegram off: %v", u)
	}
}
