// SPDX-License-Identifier: AGPL-3.0-only

package keys

import (
	"crypto/cipher"
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// AEAD — шифрование полей в базе (журнал, State Store): XChaCha20-Poly1305 с
// ключом из хранилища; запись — nonce (24 байта) || шифртекст, ad привязывает
// её к месту (id квитанции, подтверждения).
type AEAD struct{ a cipher.AEAD }

// OpenAEAD — AEAD на ключе name (создаётся при первом обращении, RandomBytes).
func OpenAEAD(s Store, name string) (*AEAD, error) {
	key, err := RandomBytes(s, name, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return NewAEAD(key)
}

// NewAEAD — AEAD на готовом ключе (chacha20poly1305.KeySize байт).
func NewAEAD(key []byte) (*AEAD, error) {
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return &AEAD{a}, nil
}

// Seal шифрует plain со случайным nonce (crypto/rand.Read не возвращает ошибку).
func (x *AEAD) Seal(ad string, plain []byte) []byte {
	nonce := make([]byte, x.a.NonceSize())
	_, _ = rand.Read(nonce)
	return x.a.Seal(nonce, nonce, plain, []byte(ad))
}

// Open расшифровывает запись Seal с тем же ad.
func (x *AEAD) Open(ad string, box []byte) ([]byte, error) {
	n := x.a.NonceSize()
	if len(box) < n {
		return nil, errors.New("ciphertext too short")
	}
	return x.a.Open(nil, box[:n], box[n:], []byte(ad))
}
