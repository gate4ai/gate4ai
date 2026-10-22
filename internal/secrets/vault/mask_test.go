// SPDX-License-Identifier: AGPL-3.0-only

package vault

import (
	"errors"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Пересекающиеся значения: хвост длинного секрета не выходит наружу
// при любом порядке обхода.
func TestMaskOverlapping(t *testing.T) {
	for i := 0; i < 50; i++ {
		v, _ := Open(keys.NewMemory())
		v.Set("short", "abcd")
		v.Set("long", "abcd-LONGSECRET-xyz")
		if got := v.MaskString("key abcd-LONGSECRET-xyz"); strings.Contains(got, "LONGSECRET") {
			t.Fatal(got)
		}
	}
}

// Производные формы значения тоже маскируются.
func TestMaskDerived(t *testing.T) {
	v, _ := Open(keys.NewMemory())
	v.Set("api", "fake/secret&value")
	for _, in := range []string{"fake%2Fsecret%26value", `"fake\/secret&value"`, "ZmFrZS9zZWNyZXQmdmFsdWU=", "FAKE/SECRET&VALUE"} {
		if got := v.MaskString("x " + in); strings.Contains(got, in) || !strings.Contains(got, "***") {
			t.Fatal(in, got)
		}
	}
}

// flaky — keychain, который временно не отдаёт значения.
type flaky struct {
	*keys.Memory
	down bool
}

func (f *flaky) Get(name string) (string, error) {
	if f.down && strings.HasPrefix(name, prefix) {
		return "", errors.New("keychain locked")
	}
	return f.Memory.Get(name)
}

// Временная ошибка keychain при открытии не выкидывает алиасы из индекса.
func TestOpenTransientError(t *testing.T) {
	ks := &flaky{Memory: keys.NewMemory()}
	v, _ := Open(ks)
	v.Set("a", "fake-a-value")
	v.Set("b", "fake-b-value")
	ks.down = true
	v2, err := Open(ks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.Get("a"); err == nil || errors.Is(err, ErrNoSecret) {
		t.Fatal("want keychain error", err)
	}
	// Запись другого алиаса сохраняет индекс целиком.
	ks.down = false
	if err := v2.Set("c", "fake-c-value"); err != nil {
		t.Fatal(err)
	}
	v3, _ := Open(ks)
	if a := v3.Aliases(); strings.Join(a, ",") != "@a,@b,@c" {
		t.Fatal(a)
	}
	if got, err := v2.Get("b"); err != nil || got != "fake-b-value" {
		t.Fatal(got, err)
	}
}
