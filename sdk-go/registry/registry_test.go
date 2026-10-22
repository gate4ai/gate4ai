// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Ключ registry gate4ai; testdata — настоящий индекс с его подписью.
const realKey = "ed25519:yDcbQuNCGqSqHSt08HdvW5/fNyWRW8FIXtKiL91hqtw="

func realIndex(t *testing.T) ([]byte, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/index.json")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile("testdata/index.json.sig")
	if err != nil {
		t.Fatal(err)
	}
	return raw, string(sig)
}

func TestRealIndexVerifies(t *testing.T) {
	raw, sig := realIndex(t)
	ix, err := ParseIndex(raw, sig, []string{realKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Connectors["email"]; !ok {
		t.Fatal("no email in the index")
	}
	if ix.Trust("email") != TrustVerified || ix.Trust("nope") != "" {
		t.Fatalf("trust: %q", ix.Trust("email"))
	}
}

func TestTamperedIndexRejected(t *testing.T) {
	raw, sig := realIndex(t)
	raw = bytes.Replace(raw, []byte("E"), []byte("X"), 1)
	if _, err := ParseIndex(raw, sig, []string{realKey}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered index: %v", err)
	}
}

func TestWrongKeyOrSignatureRejected(t *testing.T) {
	raw, sig := realIndex(t)
	other := "ed25519:jDMGJWFzQif0EgifZGdqGlW1P0IvOy6RaHwpg6frxcA="
	if _, err := ParseIndex(raw, sig, []string{other}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("other key: %v", err)
	}
	if _, err := ParseIndex(raw, "not-base64!", []string{realKey}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("bad signature: %v", err)
	}
	if _, err := ParseIndex(raw, sig, nil); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("no keys: %v", err)
	}
	// Неразбираемый ключ пропускается, подходящий из того же списка — принимается.
	if _, err := ParseIndex(raw, sig, []string{"garbage", realKey}); err != nil {
		t.Fatal(err)
	}
}

func TestSignRoundTrip(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ix := Index{Version: 1, Publishers: map[string]Publisher{"p": {Keys: []string{pub}, Trust: TrustCommunity}},
		Connectors: map[string]Entry{"x": {Publisher: "p", Repo: "https://example.com/x", License: "MIT",
			Versions: []Release{{Version: "1", URL: "x-1.g4c", SHA256: strings.Repeat("a", 64), Digest: strings.Repeat("b", 64)}}}}}
	raw, _ := json.Marshal(ix)
	sig, err := SignIndex(raw, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseIndex(raw, sig+"\n", []string{pub}); err != nil {
		t.Fatal(err)
	}
	if _, err := SignIndex(raw, pub); err == nil {
		t.Fatal("public key accepted as private")
	}
	// Неизвестное поле — ошибка, даже при верной подписи.
	raw = []byte(strings.Replace(string(raw), `"version":1`, `"version":1,"extra":true`, 1))
	sig, _ = SignIndex(raw, priv)
	if _, err := ParseIndex(raw, sig, []string{pub}); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestCheck(t *testing.T) {
	bad := Index{Version: 1, Publishers: map[string]Publisher{"p": {Trust: "x"}},
		Connectors: map[string]Entry{"c": {Publisher: "q", Path: "../x", Versions: []Release{{Version: "1"}, {Version: "1"}}}}}
	err := bad.Check()
	for _, want := range []string{"bad trust", "no keys", "unknown publisher", "repo", "path", "license", "duplicate", "sha256"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check: want %q in %v", want, err)
		}
	}
	if err := (&Index{Version: 2}).Check(); err == nil {
		t.Error("version 2 accepted")
	}
}

func TestLatestResolveRevoked(t *testing.T) {
	ix := Index{Connectors: map[string]Entry{"c": {Versions: []Release{{Version: "9"}, {Version: "10"}, {Version: "2"}}}},
		Revoked: []Revocation{{Connector: "c", Version: "10", Reason: "bug"}}}
	if r, ok := ix.Latest("c"); !ok || r.Version != "9" {
		t.Fatalf("latest: %v %v", r, ok)
	}
	if _, _, err := ix.Resolve("c", "10"); err == nil || !strings.Contains(err.Error(), "revoked: bug") {
		t.Fatalf("revoked: %v", err)
	}
	if _, r, err := ix.Resolve("c", "2"); err != nil || r.Version != "2" {
		t.Fatalf("resolve 2: %v %v", r, err)
	}
	if _, _, err := ix.Resolve("nope", ""); err == nil {
		t.Fatal("unknown connector")
	}
	ix.Revoked = append(ix.Revoked, Revocation{Connector: "c", Version: "*"})
	if _, ok := ix.Latest("c"); ok {
		t.Fatal("all versions revoked")
	}
	if CompareVersions("1.10", "1.9") <= 0 || CompareVersions("2", "2") != 0 {
		t.Fatal("CompareVersions")
	}
	if !(Release{URL: "https://x/browser-6.platforms.json"}).Native() || (Release{URL: "x.g4c"}).Native() {
		t.Fatal("Native")
	}
}

// Откат индекса (старее виденного, пропал отзыв при той же отметке) и
// просроченный индекс не принимаются (B.M8).
func TestCheckNewer(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	prev := &Index{Updated: "2026-10-21", Revoked: []Revocation{{Connector: "a", Version: "1", Reason: "x"}}}
	cases := []struct {
		ix Index
		ok bool
	}{
		{Index{Updated: "2026-10-22"}, true},
		{Index{Updated: "2026-10-21T10:00:00Z"}, true},
		{Index{Updated: "2026-10-20"}, false},
		{Index{}, false},
		{Index{Updated: "2026-10-21"}, false},
		{Index{Updated: "2026-10-21", Revoked: prev.Revoked}, true},
		{Index{Updated: "2026-10-23", Expires: "2026-10-22"}, true},
		{Index{Updated: "2026-10-23", Expires: "2026-10-21"}, false},
		{Index{Updated: "2026-10-23", Expires: "soon"}, false},
	}
	for i, c := range cases {
		if err := c.ix.CheckNewer(prev, now); (err == nil) != c.ok {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if err := (&Index{}).CheckNewer(nil, now); err != nil {
		t.Fatal("first index:", err)
	}
}
