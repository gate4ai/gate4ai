// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"reflect"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
)

func TestSecretUses(t *testing.T) {
	cfg := &config.Config{
		Approvals: config.Approvals{Channels: []string{"desktop", "telegram"}, Telegram: &config.Telegram{Token: "@telegram.bot", OwnerID: 1}},
		Relay:     config.Relay{URL: "https://relay.example", DeviceID: "d", Token: "@relay.token"},
		Egress:    config.Egress{Secrets: []config.EgressSecret{{Domain: "api.example", Header: "Authorization", Placeholder: "X", Secret: "@api.key"}}},
		Connectors: map[string]map[string]any{
			"jira": {"token": "@jira.acme"},
			"old":  {"key": "@old.key"},
		},
	}
	ms := []manifest.Manifest{
		{Connector: "jira", Capabilities: manifest.Capabilities{Reveal: []string{"@jira.fixed"}}},
		{Connector: "browser", Capabilities: manifest.Capabilities{Reveal: []string{"${setting:model_key}"}}},
	}
	got := secretUses(cfg, ms)
	want := map[string][]SecretUse{
		"@telegram.bot": {{Name: "Telegram bot", Post: " (approvals)", Link: "/approvals/telegram"}},
		"@relay.token":  {{Name: "relay", Post: " (device connection)", Link: "/settings/internet"}},
		"@api.key":      {{Pre: "Egress Proxy: ", Name: "api.example"}},
		"@jira.fixed":   {{Pre: "connector ", Name: "jira", Link: "/connectors/jira"}},
		"@jira.acme":    {{Pre: "connector ", Name: "jira", Link: "/connectors/jira"}},
		"@old.key":      {{Pre: "connector ", Name: "old", Post: " (not installed)"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if _, ok := got["@unused"]; ok {
		t.Fatal("unused secret reported as used")
	}

	// Канал выключен — токен всё равно указан, с пометкой.
	cfg.Approvals.Channels = []string{"desktop"}
	if u := secretUses(cfg, nil)["@telegram.bot"]; len(u) != 1 || u[0].Text() != "Telegram bot (approvals, the channel is off)" {
		t.Fatalf("telegram off: %v", u)
	}
}
