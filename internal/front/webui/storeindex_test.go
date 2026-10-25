// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// signedIndex — подписанный индекс registry с записями conns на дату updated.
func signedIndex(t *testing.T, regPriv, updated string, conns ...string) (raw []byte, sig string) {
	t.Helper()
	ix := registry.Index{Version: 1, Updated: updated, Publishers: map[string]registry.Publisher{"acme": {Keys: []string{"ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}, Trust: registry.TrustPublisher}},
		Connectors: map[string]registry.Entry{}}
	for _, c := range conns {
		ix.Connectors[c] = registry.Entry{Publisher: "acme", Repo: "https://example.com/" + c, License: "MIT", Description: c + " description",
			Versions: []registry.Release{{Version: "1", URL: c + ".g4c", SHA256: strings.Repeat("0", 64), Digest: strings.Repeat("1", 64)}}}
	}
	raw, _ = json.Marshal(ix)
	sig, err := registry.SignIndex(raw, regPriv)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

// Магазин: кэш индекса старше часа обновляется при открытии страницы, а
// коннекторы, которых больше нет (email), не показываются даже из кэша.
func TestStorePageRefreshesStaleIndex(t *testing.T) {
	reg := t.TempDir()
	regPub, regPriv, _ := registry.GenerateKey()
	raw, sig := signedIndex(t, regPriv, "2026-10-24", "kvx")
	_ = os.WriteFile(filepath.Join(reg, "index.json"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(reg, "index.json.sig"), []byte(sig), 0o644)
	conf := cfg + "store:\n  registry: " + filepath.Join(reg, "index.json") + "\n  registry_keys: [\"" + regPub + "\"]\n"
	u, d, _, base := setupCfg(t, conf)
	// Старый кэш: ещё с email и old-conn, файл недельной давности.
	oraw, osig := signedIndex(t, regPriv, "2026-10-21", "email", "old-conn")
	_ = os.MkdirAll(d.ConnectorsDir, 0o700)
	cache := filepath.Join(d.ConnectorsDir, "registry-index.json")
	_ = os.WriteFile(cache, oraw, 0o600)
	_ = os.WriteFile(cache+".sig", []byte(osig), 0o600)
	week := time.Now().Add(-7 * 24 * time.Hour)
	_ = os.Chtimes(cache, week, week)
	_, _, _, get := loginAs(t, u, base)
	p := get("/connectors")
	if !strings.Contains(p, "kvx") || strings.Contains(p, "old-conn") || strings.Contains(p, "email description") {
		t.Fatalf("stale cache not refreshed:\n%s", p)
	}
	if got, _ := os.ReadFile(cache); string(got) != string(raw) {
		t.Fatalf("cache not rewritten: %s", got)
	}

	// Registry недоступен, кэш свежий, но с email: kvx виден, email — нет.
	_ = os.WriteFile(cache, oraw, 0o600)
	_ = os.WriteFile(cache+".sig", []byte(osig), 0o600)
	_ = os.Remove(filepath.Join(reg, "index.json"))
	p = get("/connectors")
	if !strings.Contains(p, "old-conn") || strings.Contains(p, "email description") {
		t.Fatalf("removed connector shown from cache:\n%s", p)
	}
}
