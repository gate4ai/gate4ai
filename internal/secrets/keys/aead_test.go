// SPDX-License-Identifier: AGPL-3.0-only

package keys

import (
	"bytes"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// Формат записи — nonce || шифртекст XChaCha20-Poly1305: записи, сделанные
// напрямую chacha20poly1305, открываются, и наоборот.
func TestAEADFormat(t *testing.T) {
	key := bytes.Repeat([]byte{7}, chacha20poly1305.KeySize)
	x, err := NewAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := chacha20poly1305.NewX(key)
	nonce := bytes.Repeat([]byte{1}, raw.NonceSize())
	old := raw.Seal(nonce, nonce, []byte("secret"), []byte("ad:1"))
	if got, err := x.Open("ad:1", old); err != nil || string(got) != "secret" {
		t.Fatal(got, err)
	}
	if _, err := x.Open("ad:2", old); err == nil {
		t.Fatal("wrong ad must fail")
	}
	box := x.Seal("ad:1", []byte("v"))
	if got, err := raw.Open(nil, box[:raw.NonceSize()], box[raw.NonceSize():], []byte("ad:1")); err != nil || string(got) != "v" {
		t.Fatal(got, err)
	}
	if _, err := x.Open("ad:1", box[:3]); err == nil {
		t.Fatal("short box must fail")
	}
}
