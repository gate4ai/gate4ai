// SPDX-License-Identifier: AGPL-3.0-only

package keys

import (
	"bytes"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestMemoryAndRandom(t *testing.T) {
	m := NewMemory()
	if _, err := m.Get("x"); err != ErrNotFound {
		t.Fatal(err)
	}
	a, err := RandomBytes(m, "k", 32)
	if err != nil || len(a) != 32 {
		t.Fatal(err)
	}
	b, _ := RandomBytes(m, "k", 32)
	if !bytes.Equal(a, b) {
		t.Fatal("key must be stable")
	}
	if _, err := RandomBytes(m, "k", 16); err == nil {
		t.Fatal("length mismatch must fail")
	}
	if m.Delete("k") != nil || m.Delete("k") != ErrNotFound {
		t.Fatal("delete")
	}
}

func TestKeyringMock(t *testing.T) {
	keyring.MockInit()
	k := NewKeyring()
	if _, err := k.Get("a"); err != ErrNotFound {
		t.Fatal(err)
	}
	if err := k.Set("a", "v"); err != nil {
		t.Fatal(err)
	}
	if v, _ := k.Get("a"); v != "v" {
		t.Fatal(v)
	}
	if k.Delete("a") != nil || k.Delete("a") != ErrNotFound {
		t.Fatal("delete")
	}
}

func TestFile(t *testing.T) {
	p := t.TempDir() + "/k/keys.json"
	f := NewFile(p)
	if _, err := f.Get("a"); err != ErrNotFound {
		t.Fatal(err)
	}
	f.Set("a", "1")
	if v, _ := NewFile(p).Get("a"); v != "1" {
		t.Fatal(v)
	}
	if f.Delete("a") != nil || f.Delete("a") != ErrNotFound {
		t.Fatal("delete")
	}
	if Probe(f) != nil {
		t.Fatal("probe")
	}
}
