// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
)

func TestInstallDevChange(t *testing.T) {
	if err := CheckChange(Change{Op: OpInstallDev}); err == nil {
		t.Fatal("folder is required")
	}
	if err := CheckChange(Change{Op: OpInstallDev, Folder: "rel/dir"}); err == nil {
		t.Fatal("relative folder")
	}
	if err := CheckChange(Change{Op: OpInstallDev, Folder: "/home/a/lk"}); err != nil {
		t.Fatal(err)
	}
	if d := (Change{Op: OpInstallDev, Folder: "/home/a/lk", Connector: "lk"}).Describe(); d != "Build and install an own build from /home/a/lk (connector lk) and give access to it" {
		t.Fatal(d)
	}
}

// add_credential: только новая учётка и новые секреты в её пространстве имён;
// секретоподобные ключи настроек отклоняются (code review H4, E.6).
func TestAgentCredentialAndSecretKeys(t *testing.T) {
	cred := func(secret string) string {
		return `{"bind":["https://evil.example"],"form":{"fields":[{"selector":"#p","secret":"` + secret + `"}],"submit":{"press":"Enter"}}}`
	}
	if err := CheckChange(Change{Op: OpAddCredential, Account: "@site", CredentialJSON: cred("@bank.password")}); err == nil {
		t.Fatal("a foreign alias is accepted")
	}
	if err := CheckChange(Change{Op: OpAddCredential, Account: "@site", CredentialJSON: cred("@site.password")}); err != nil {
		t.Fatal(err)
	}
	c := &Config{Credentials: map[string]Credential{"@bank": {Bind: []string{"https://bank.example"}}},
		Connectors: map[string]map[string]any{"mail": {"imap": map[string]any{"password": "@site.password"}}}}
	if err := c.ApplyChange("p", Change{Op: OpAddCredential, Account: "@bank", CredentialJSON: cred("@bank.x")}); err == nil {
		t.Fatal("an existing account is overwritten")
	}
	if err := c.ApplyChange("p", Change{Op: OpAddCredential, Account: "@site", CredentialJSON: cred("@site.password")}); err == nil {
		t.Fatal("an alias used by the policy is reused")
	}
	if err := c.ApplyChange("p", Change{Op: OpAddCredential, Account: "@new", CredentialJSON: cred("@new.password")}); err != nil {
		t.Fatal(err)
	}
	for _, js := range []string{`{"api_key":"sk-x"}`, `{"client_secret":"x"}`, `{"oauth":{"credentials":"x"}}`} {
		if err := CheckChange(Change{Op: OpConnectorSetting, Connector: "x", SettingsJSON: js}); err == nil {
			t.Fatal("secret-like key accepted:", js)
		}
	}
	if err := CheckChange(Change{Op: OpAddSettingItems, Connector: "x", Key: "client_secret", Items: []string{"a"}}); err == nil {
		t.Fatal("secret-like list key accepted")
	}
}

// add_rule с id существующего правила показывается как замена (code review E.3).
func TestDescribeRuleReplacement(t *testing.T) {
	c := &Config{Profiles: map[string]Profile{"p": {Rules: []Rule{{ID: "deny-mail", Action: "email.send", Effect: EffectDeny}}}}}
	d := (Change{Op: OpAddRule, RuleID: "deny-mail", Action: "email.send", Effect: EffectAllow}).DescribeIn(c, "p")
	if !strings.Contains(d, "REPLACE rule deny-mail") || !strings.Contains(d, "deny email.send") || !strings.Contains(d, "allow email.send") {
		t.Fatal(d)
	}
	if d := (Change{Op: OpAddRule, RuleID: "new", Action: "a.b", Effect: EffectAllow}).DescribeIn(c, "p"); !strings.Contains(d, "[id new]") {
		t.Fatal(d)
	}
}
