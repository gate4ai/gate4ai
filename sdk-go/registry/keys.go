// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Ключи Ed25519 в тексте: "ed25519:<base64 публичного>" и
// "ed25519-private:<base64 seed>". Те же ключи у registry и у издателей пакетов.
const (
	pubPrefix  = "ed25519:"
	privPrefix = "ed25519-private:"
)

// Sum — hex(sha256(b)): данные, над которыми подписывается индекс.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// GenerateKey — новый ключ: (публичный, приватный) в тексте.
func GenerateKey() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return FormatPublicKey(pub), privPrefix + base64.StdEncoding.EncodeToString(priv.Seed()), nil
}

// FormatPublicKey — публичный ключ в тексте.
func FormatPublicKey(pub ed25519.PublicKey) string {
	return pubPrefix + base64.StdEncoding.EncodeToString(pub)
}

// ParsePublicKey — "ed25519:<base64>".
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), pubPrefix))
	if err != nil || !strings.HasPrefix(strings.TrimSpace(s), pubPrefix) || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("bad public key %q (want ed25519:<base64>)", s)
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKey — "ed25519-private:<base64 seed>".
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	s = strings.TrimSpace(s)
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, privPrefix))
	if err != nil || !strings.HasPrefix(s, privPrefix) || len(b) != ed25519.SeedSize {
		return nil, errors.New("bad private key (want ed25519-private:<base64 seed>)")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// Sign подписывает данные; ответ — base64.
func Sign(key ed25519.PrivateKey, data string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(data)))
}

// VerifySig проверяет подпись base64 одним из ключей; возвращает подошедший ключ.
func VerifySig(keys []string, data, sig string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	for _, k := range keys {
		pub, err := ParsePublicKey(k)
		if err == nil && ed25519.Verify(pub, []byte(data), raw) {
			return k, true
		}
	}
	return "", false
}
