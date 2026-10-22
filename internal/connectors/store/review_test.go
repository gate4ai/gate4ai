// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// Коннектор с зарезервированным именем (data — KV всех коннекторов) не ставится
// и не стирает данные (B.M1).
func TestReservedNameNotInstalled(t *testing.T) {
	dir := t.TempDir()
	o := Options{Dir: filepath.Join(dir, "connectors")}
	kv := filepath.Join(o.Dir, DataDir, "bank", "kv.json")
	os.MkdirAll(filepath.Dir(kv), 0o700)
	os.WriteFile(kv, []byte(`{"token":"dA=="}`), 0o600)
	m := []byte("connector: data\nversion: \"1\"\nruntime: wasm\nactions:\n  - { name: read, class: read }\n")
	b, err := Pack(m, fakeWasm, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Prepare(context.Background(), write(t, dir, "data-1.g4c", b), true); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatal("connector named data:", err)
	}
	p := &Plan{Connector: "data", Version: "1", pkg: &Package{}}
	if _, err := o.Install(p, nil); err == nil {
		t.Fatal("Install accepted the reserved name")
	}
	if _, err := os.Stat(kv); err != nil {
		t.Fatal("KV of other connectors lost:", err)
	}
	ix := registry.Index{Version: 1, Publishers: map[string]registry.Publisher{}, Connectors: map[string]registry.Entry{
		"data": {Publisher: "x", Repo: "https://example.org/r", License: "MIT"}}}
	if err := ix.Check(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatal("registry index with a reserved name:", err)
	}
}

// Коннектор другого издателя под тем же именем не наследует KV прежнего (B.L4);
// обновление того же издателя данные сохраняет.
func TestDataOfAnotherPublisherErased(t *testing.T) {
	dir := t.TempDir()
	pub, priv := keys(t)
	o := Options{Dir: filepath.Join(dir, "connectors"), TrustedKeys: []string{pub}}
	ctx := context.Background()
	p, err := o.Prepare(ctx, write(t, dir, "bank-1.g4c", pack(t, "1", "api.bank.example", false, priv)), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Install(p, p.Capabilities); err != nil {
		t.Fatal(err)
	}
	kv := filepath.Join(o.Dir, DataDir, "bank", "kv.json")
	os.WriteFile(kv, []byte(`{"token":"dA=="}`), 0o600)
	p, _ = o.Prepare(ctx, write(t, dir, "bank-2.g4c", pack(t, "2", "api.bank.example", false, priv)), false)
	if len(p.Warnings) != 0 {
		t.Fatal("update of the same publisher warns:", p.Warnings)
	}
	o.Install(p, p.Capabilities)
	if _, err := os.Stat(kv); err != nil {
		t.Fatal("update of the same publisher lost the data")
	}
	if err := o.Remove("bank"); err != nil {
		t.Fatal(err)
	}
	// Под тем же именем — чужая сборка (без доверенной подписи).
	p, err = o.Prepare(ctx, write(t, dir, "bank-3.g4c", pack(t, "3", "api.bank.example", false, "")), true)
	if err != nil || !slices.ContainsFunc(p.Warnings, func(w string) bool { return strings.Contains(w, "erased") }) {
		t.Fatal(p, err)
	}
	if _, err := o.Install(p, p.Capabilities); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kv); !os.IsNotExist(err) {
		t.Fatal("connector of another publisher inherited the data")
	}
}

// Пакет из файла с подписью издателя из registry получает его доверие, только
// если версия есть в индексе и совпадает с отревьюенной записью (B.M6).
func TestFileTrustNeedsRegistryEntry(t *testing.T) {
	r := newRegistry(t, registry.TrustVerified)
	r.publish(t, "1", "api.bank.example")
	ctx := context.Background()
	if _, err := r.o.RefreshIndex(ctx); err != nil {
		t.Fatal(err)
	}
	same := write(t, r.dir, "bank-1.g4c", pack(t, "1", "api.bank.example", true, r.pubPriv))
	if p, err := r.o.Prepare(ctx, same, false); err != nil || p.Trust != registry.TrustVerified {
		t.Fatal(p, err)
	}
	// Подписана издателем, но в индексе её нет (не прошла ревью).
	unreviewed := write(t, r.dir, "bank-2.g4c", pack(t, "2", "evil.example", true, r.pubPriv))
	if _, err := r.o.Prepare(ctx, unreviewed, false); err == nil || !strings.Contains(err.Error(), "not in the registry") {
		t.Fatal("unreviewed version got the publisher's trust:", err)
	}
	if p, err := r.o.Prepare(ctx, unreviewed, true); err != nil || p.Trust != TrustLocal {
		t.Fatal("--dev must install it as local:", p, err)
	}
	// Та же версия с другими возможностями.
	other := write(t, r.dir, "bank-1b.g4c", pack(t, "1", "evil.example", true, r.pubPriv))
	if _, err := r.o.Prepare(ctx, other, false); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal("version with other capabilities got the publisher's trust:", err)
	}
}

// Индекс старше закэшированного (до отзыва) не принимается (B.M8).
func TestIndexRollbackRejected(t *testing.T) {
	r := newRegistry(t, registry.TrustVerified)
	r.publish(t, "1", "api.bank.example")
	ctx := context.Background()
	r.index.Updated = "2026-10-20"
	r.save(t)
	oldRaw, _ := os.ReadFile(r.o.Registry)
	oldSig, _ := os.ReadFile(r.o.Registry + ".sig")
	r.index.Updated = "2026-10-21"
	r.index.Revoked = []registry.Revocation{{Connector: "bank", Version: "1", Reason: "compromised"}}
	r.save(t)
	if _, err := r.o.RefreshIndex(ctx); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.dir, "registry"), "index.json", oldRaw)
	write(t, filepath.Join(r.dir, "registry"), "index.json.sig", oldSig)
	if _, err := r.o.RefreshIndex(ctx); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatal("older index accepted:", err)
	}
	if ix := r.o.CachedIndex(); ix == nil || len(ix.Revoked) != 1 {
		t.Fatal("cache replaced by the older index")
	}
	// Та же отметка, но без отзыва — тоже откат.
	r.index.Revoked = nil
	r.save(t)
	if _, err := r.o.RefreshIndex(ctx); err == nil {
		t.Fatal("index without the revocation accepted")
	}
}

// В согласие входят runtime, классы действий и лимиты выше умолчаний (B.L5,
// B.M2); согласие, записанное прежней версией (без этих строк), остаётся
// действительным для того же пакета и не требует согласия заново.
func TestConsentIncludesClassesAndLegacyConsent(t *testing.T) {
	dir := t.TempDir()
	o := Options{Dir: filepath.Join(dir, "connectors")}
	ctx := context.Background()
	p, err := o.Prepare(ctx, write(t, dir, "bank-1.g4c", pack(t, "1", "api.bank.example", false, "")), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Install(p, p.Capabilities); err != nil {
		t.Fatal(err)
	}
	// install.json прежней версии gate4ai: согласие без новых строк.
	info, _ := o.Info("bank")
	info.Capabilities = nonNil(p.pkg.Manifest.LegacySummary())
	ib, _ := json.Marshal(info)
	os.WriteFile(filepath.Join(o.Dir, "bank", InfoFile), ib, 0o600)
	if err := o.Check("bank"); err != nil {
		t.Fatal("legacy consent rejected at load:", err)
	}
	// То же — без согласия; новое действие класса external — согласие заново.
	if p, _ := o.Prepare(ctx, write(t, dir, "bank-2.g4c", pack(t, "2", "api.bank.example", false, "")), true); p.NeedsConsent {
		t.Fatal("unchanged package needs consent after upgrade of gate4ai")
	}
	p, _ = o.Prepare(ctx, write(t, dir, "bank-3.g4c", pack(t, "3", "api.bank.example", true, "")), true)
	if !p.NeedsConsent || !slices.Contains(p.Capabilities, "class external actions: pay") {
		t.Fatal("new external action without consent:", p.Capabilities)
	}
	// Лимиты выше допустимых для установки — отказ; выше умолчаний — в согласии.
	m := []byte("connector: big\nversion: \"1\"\nruntime: wasm\nlimits: { memory_mb: 2048 }\nactions: []\n")
	b, _ := Pack(m, fakeWasm, nil)
	if _, err := o.Prepare(ctx, write(t, dir, "big-1.g4c", b), true); err == nil || !strings.Contains(err.Error(), "memory_mb") {
		t.Fatal("memory_mb 2048 installed:", err)
	}
	m = []byte("connector: big\nversion: \"1\"\nruntime: wasm\nlimits: { memory_mb: 512, timeout_s: 300 }\nactions: []\n")
	b, _ = Pack(m, fakeWasm, nil)
	p, err = o.Prepare(ctx, write(t, dir, "big-2.g4c", b), true)
	if err != nil || !slices.Contains(p.Capabilities, "memory up to 512 MB per call") || !slices.Contains(p.Capabilities, "a call may run up to 300 s") {
		t.Fatal(p, err)
	}
}

// connectors verify показывает ключ, только если подпись им верна (B.L6).
func TestSignedByVerifiesSignature(t *testing.T) {
	pub, priv := keys(t)
	p, _ := Open(pack(t, "1", "api.bank.example", false, priv))
	if p.SignedBy() != pub {
		t.Fatal("valid signature not reported")
	}
	other, _ := keys(t)
	p.Sig.PublicKey = other
	if p.SignedBy() != "" {
		t.Fatal("key claimed by the package reported without a valid signature")
	}
}
