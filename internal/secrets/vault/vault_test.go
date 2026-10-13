// SPDX-License-Identifier: FSL-1.1-ALv2

package vault

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

func TestVaultKeychain(t *testing.T) {
	keyring.MockInit()
	ks := keys.NewKeyring()
	v, err := Open(ks)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set("@mail.work", "app-pass-123"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("Bad Alias", "x"); err == nil {
		t.Fatal("bad alias")
	}
	// Повторное открытие видит алиасы через индекс в keychain.
	v2, _ := Open(ks)
	if got, err := v2.Get("@mail.work"); err != nil || got != "app-pass-123" {
		t.Fatal(got, err)
	}
	if a := v2.Aliases(); len(a) != 1 || a[0] != "@mail.work" {
		t.Fatal(a)
	}
	v2.Delete("@mail.work")
	if _, err := v2.Get("mail.work"); err == nil {
		t.Fatal("deleted")
	}
}

func TestMask(t *testing.T) {
	v, _ := Open(keys.NewMemory())
	v.Set("tbank", "s3cr3t-token")
	in := map[string]any{
		"text":     "token s3cr3t-token card 4111 1111 1111 1111 order 1234567890123 passport 45 06 123456",
		"password": "whatever",
		"list":     []any{"4111-1111-1111-1111"},
		"n":        5.0,
	}
	out := v.Mask(in).(map[string]any)
	txt := out["text"].(string)
	if strings.Contains(txt, "s3cr3t") || !strings.Contains(txt, "**** 1111") || !strings.Contains(txt, "1234567890123") || !strings.Contains(txt, "**** ******") {
		t.Fatal(txt)
	}
	if out["password"] != "***" || out["list"].([]any)[0] != "**** 1111" || out["n"] != 5.0 {
		t.Fatal(out)
	}
}
