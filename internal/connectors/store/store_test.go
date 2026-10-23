// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// Модуль-заглушка: для упаковки и установки достаточно заголовка WASM;
// компиляция идёт в фоне и в этих тестах не нужна.
var fakeWasm = []byte("\x00asm\x01\x00\x00\x00")

func manifestYAML(version, host string, write bool) []byte {
	acts := "  - { name: read, class: read }\n"
	if write {
		acts += "  - { name: pay, class: external }\n"
	}
	return []byte("connector: bank\nversion: \"" + version + "\"\nruntime: wasm\ncapabilities:\n  network: [ { host: \"" + host + "\", scheme: https } ]\nactions:\n" + acts)
}

func keys(t *testing.T) (string, string) {
	pub, priv, err := registry.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func pack(t *testing.T, version, host string, write bool, priv string) []byte {
	t.Helper()
	b, err := packKey(manifestYAML(version, host, write), priv)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func packKey(m []byte, priv string) ([]byte, error) {
	if priv == "" {
		return Pack(m, fakeWasm, nil)
	}
	k, err := registry.ParsePrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return Pack(m, fakeWasm, k)
}

func write(t *testing.T, dir, name string, b []byte) string {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPackageDigestAndSignature(t *testing.T) {
	pub, priv := keys(t)
	b := pack(t, "1", "api.bank.example", false, priv)
	p, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := registry.VerifySig([]string{pub}, p.Sig.Digest, p.Sig.Signature); !ok || k != pub {
		t.Fatal("signature")
	}
	other, _ := keys(t)
	if _, ok := registry.VerifySig([]string{other}, p.Sig.Digest, p.Sig.Signature); ok {
		t.Fatal("wrong key accepted")
	}
	// Подмена модуля внутри пакета ломает дайджест.
	if _, err := Pack(manifestYAML("1", "x", false), []byte("not wasm"), nil); err == nil {
		t.Fatal("non-wasm module packed")
	}
	tampered, _ := Pack(manifestYAML("1", "api.bank.example", false), append(append([]byte{}, fakeWasm...), 1), nil)
	tp, _ := Open(tampered)
	tp.Sig.Digest = p.Sig.Digest
	if Digest(tp.ManifestYAML, tp.Wasm) == p.Sig.Digest {
		t.Fatal("digest does not cover the module")
	}
}

func TestInstallFromFileTrustAndConsent(t *testing.T) {
	dir := t.TempDir()
	pub, priv := keys(t)
	o := Options{Dir: filepath.Join(dir, "connectors")}
	ctx := context.Background()
	unsigned := write(t, dir, "bank-1.g4c", pack(t, "1", "api.bank.example", true, ""))
	if _, err := o.Prepare(ctx, unsigned, false); err == nil || !strings.Contains(err.Error(), "--dev") {
		t.Fatal("unsigned package without --dev:", err)
	}
	p, err := o.Prepare(ctx, unsigned, true)
	if err != nil || p.Trust != TrustLocal || !p.NeedsConsent || len(p.Warnings) == 0 {
		t.Fatal(p, err)
	}
	if _, err := o.Install(p, nil); !errors.Is(err, ErrConsent) {
		t.Fatal("installed without consent")
	}
	if _, err := o.Install(p, []string{"network https://evil.example"}); !errors.Is(err, ErrConsent) {
		t.Fatal("installed with consent to other capabilities")
	}
	info, err := o.Install(p, p.Capabilities)
	if err != nil || info.Trust != TrustLocal {
		t.Fatal(info, err)
	}
	// Подписанный доверенным ключом — уровень publisher.
	o.TrustedKeys = []string{pub}
	signed := write(t, dir, "bank-2.g4c", pack(t, "2", "api.bank.example", true, priv))
	p, err = o.Prepare(ctx, signed, false)
	if err != nil || p.Trust != registry.TrustPublisher || p.Installed != "1" {
		t.Fatal(p, err)
	}
	// Возможности не изменились — обновление без повторного согласия.
	if p.NeedsConsent {
		t.Fatal("same capabilities must not need consent again")
	}
	if _, err := o.Install(p, nil); err != nil {
		t.Fatal(err)
	}
	// Новые возможности — снова согласие.
	wider := write(t, dir, "bank-3.g4c", pack(t, "3", "*.bank.example", true, priv))
	p, _ = o.Prepare(ctx, wider, false)
	if !p.NeedsConsent {
		t.Fatal("changed capabilities installed without consent")
	}
	if _, err := o.Install(p, nil); !errors.Is(err, ErrConsent) {
		t.Fatal(err)
	}
	if info, _ := o.Info("bank"); info.Version != "2" {
		t.Fatal("failed update replaced the installed version", info)
	}
	if err := o.Remove("bank"); err != nil {
		t.Fatal(err)
	}
	if err := o.Remove("../etc"); err == nil {
		t.Fatal("bad name accepted")
	}
}

type registryEnv struct {
	o       Options
	regPriv string
	pubPriv string
	dir     string
	index   registry.Index
}

func newRegistry(t *testing.T, trust string) *registryEnv {
	dir := t.TempDir()
	regPub, regPriv := keys(t)
	pubPub, pubPriv := keys(t)
	r := &registryEnv{dir: dir, regPriv: regPriv, pubPriv: pubPriv,
		o: Options{Dir: filepath.Join(dir, "home", "connectors"), Registry: filepath.Join(dir, "registry", "index.json"), RegistryKeys: []string{regPub}},
		index: registry.Index{Version: 1, Publishers: map[string]registry.Publisher{"acme": {Keys: []string{pubPub}, Trust: trust}},
			Connectors: map[string]registry.Entry{"bank": {Publisher: "acme", Repo: "https://github.com/acme/gate4ai-bank", License: "AGPL-3.0-only"}}}}
	os.MkdirAll(filepath.Join(dir, "registry", "packages"), 0o755)
	return r
}

func (r *registryEnv) publish(t *testing.T, version, host string) {
	b := pack(t, version, host, true, r.pubPriv)
	write(t, filepath.Join(r.dir, "registry", "packages"), "bank-"+version+".g4c", b)
	p, _ := Open(b)
	e := r.index.Connectors["bank"]
	e.Versions = append(e.Versions, registry.Release{Version: version, URL: "packages/bank-" + version + ".g4c", SHA256: SHA256(b),
		Digest: p.Sig.Digest, Capabilities: p.Manifest.Summary()})
	r.index.Connectors["bank"] = e
	r.save(t)
}

func (r *registryEnv) save(t *testing.T) {
	raw, _ := json.MarshalIndent(r.index, "", "  ")
	write(t, filepath.Join(r.dir, "registry"), "index.json", raw)
	sig, err := registry.SignIndex(raw, r.regPriv)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r.dir, "registry"), "index.json.sig", []byte(sig))
}

// List не видит каталоги идущей установки: ".install-<имя>-*" и "<имя>.old".
func TestListSkipsInstallInProgress(t *testing.T) {
	dir := t.TempDir()
	o := Options{Dir: filepath.Join(dir, "connectors")}
	p, err := o.Prepare(context.Background(), write(t, dir, "bank-1.g4c", pack(t, "1", "api.bank.example", true, "")), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Install(p, p.Capabilities); err != nil {
		t.Fatal(err)
	}
	for _, tmp := range []string{".install-bank-123", "bank.old"} {
		if err := os.CopyFS(filepath.Join(o.Dir, tmp), os.DirFS(filepath.Join(o.Dir, "bank"))); err != nil {
			t.Fatal(err)
		}
	}
	list, err := List(o.Dir)
	if err != nil || len(list) != 1 || list[0].Name != "bank" {
		t.Fatal(list, err)
	}
}

func TestRegistryInstallRevokeAndTamper(t *testing.T) {
	r := newRegistry(t, registry.TrustVerified)
	r.publish(t, "1.9", "api.bank.example")
	r.publish(t, "1.10", "api.bank.example")
	ctx := context.Background()
	p, err := r.o.Prepare(ctx, "bank", false)
	if err != nil || p.Version != "1.10" || p.Trust != registry.TrustVerified || p.Repo == "" {
		t.Fatal(p, err)
	}
	if _, err := r.o.Install(p, p.Capabilities); err != nil {
		t.Fatal(err)
	}
	// Отзыв: последняя версия отозвана → берётся предыдущая; явная — отказ.
	r.index.Revoked = []registry.Revocation{{Connector: "bank", Version: "1.10", Reason: "leaks data"}}
	r.save(t)
	if p, err := r.o.Prepare(ctx, "bank", false); err != nil || p.Version != "1.9" {
		t.Fatal(p, err)
	}
	if _, err := r.o.Prepare(ctx, "bank@1.10", false); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatal("revoked version installed:", err)
	}
	// Установленная отозванная версия не загружается (кэш индекса).
	if err := r.o.Check("bank"); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked connector passes the check:", err)
	}
	// Подпись индекса чужим ключом.
	_, other := keys(t)
	raw, _ := os.ReadFile(r.o.Registry)
	sig, _ := registry.SignIndex(raw, other)
	write(t, filepath.Join(r.dir, "registry"), "index.json.sig", []byte(sig))
	if _, err := r.o.Prepare(ctx, "bank", false); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatal("index with a foreign signature accepted:", err)
	}
	r.save(t)
	// Пакет подменён на сервере — sha256 не совпадает.
	write(t, filepath.Join(r.dir, "registry", "packages"), "bank-1.9.g4c", pack(t, "1.9", "evil.example", true, r.pubPriv))
	if _, err := r.o.Prepare(ctx, "bank@1.9", false); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatal("tampered package accepted:", err)
	}
}

func TestRegistryCapabilitiesMustMatchReview(t *testing.T) {
	r := newRegistry(t, registry.TrustVerified)
	r.publish(t, "1", "api.bank.example")
	e := r.index.Connectors["bank"]
	e.Versions[0].Capabilities = []string{"network https://statements.bank.example"} // проверены другие возможности
	r.index.Connectors["bank"] = e
	r.save(t)
	if _, err := r.o.Prepare(context.Background(), "bank", false); err == nil || !strings.Contains(err.Error(), "reviewed") {
		t.Fatal(err)
	}
	bad := r.index
	bad.Connectors = map[string]registry.Entry{"x": {Publisher: "nobody"}}
	if err := bad.Check(); err == nil || !strings.Contains(err.Error(), "unknown publisher") || !strings.Contains(err.Error(), "repo") {
		t.Fatal(err)
	}
}

func TestLoadIntegrityAndCommunityReadOnly(t *testing.T) {
	r := newRegistry(t, registry.TrustCommunity)
	r.publish(t, "1", "api.bank.example")
	p, err := r.o.Prepare(context.Background(), "bank", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) == 0 {
		t.Fatal("community connector without a warning")
	}
	if _, err := r.o.Install(p, p.Capabilities); err != nil {
		t.Fatal(err)
	}
	e, _ := wasm.NewEngine("", wasm.Services{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	c, err := r.o.Load("bank", e)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Manifest()
	if _, ok := m.Find("pay"); ok {
		t.Fatal("community connector exposes a non-read action")
	}
	if _, ok := m.Find("read"); !ok {
		t.Fatal("read action missing")
	}
	// Файлы изменены после установки — коннектор не загружается.
	os.WriteFile(filepath.Join(r.o.Dir, "bank", ModuleFile), append(fakeWasm, 0), 0o600)
	if err := r.o.Check("bank"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatal(err)
	}
	// Положен вручную, без install.json — не загружается.
	os.Remove(filepath.Join(r.o.Dir, "bank", InfoFile))
	if loaded := r.o.LoadAll(context.Background(), e, slog.New(slog.NewTextHandler(io.Discard, nil))); len(loaded) != 0 {
		t.Fatal("connector without install.json loaded")
	}
}

// Папка с исходниками: daemon собирает коннектор сам (своя сборка, только с
// dev), источник — "dir:<путь>"; повторная подготовка пересобирает изменённый
// код, а возможности без изменений не требуют нового согласия.
func TestInstallFromSourceFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("builds WASM")
	}
	// Временный модуль коннектора — вне общего go.work скриптов проекта (test-set.sh).
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	src := filepath.Join(dir, "bank-src")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/bank\n\ngo "+strings.TrimPrefix(runtime.Version(), "go")+"\n"), 0o644)
	os.WriteFile(filepath.Join(src, ManifestFile), manifestYAML("1", "api.bank.example", false), 0o644)
	body := func(s string) {
		os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() { println(\""+s+"\") }\n"), 0o644)
	}
	body("v1")
	o := Options{Dir: filepath.Join(dir, "connectors")}
	ctx := context.Background()
	if _, ok := SourceFolder(dir); ok {
		t.Fatal("a folder without manifest.yaml is not a source folder")
	}
	if _, err := o.Prepare(ctx, src, false); err == nil || !strings.Contains(err.Error(), "--dev") {
		t.Fatal("source folder without --dev:", err)
	}
	p, err := o.Prepare(ctx, src, true)
	if err != nil || p.Trust != TrustLocal || p.Source != SourceDir+src || p.Connector != "bank" {
		t.Fatal(p, err)
	}
	info, err := o.Install(p, p.Capabilities)
	if err != nil || info.Source != SourceDir+src {
		t.Fatal(info, err)
	}
	body("v2")
	p2, err := o.Prepare(ctx, info.Source, true)
	if err != nil || p2.NeedsConsent || p2.pkg.Sig.Digest == p.pkg.Sig.Digest {
		t.Fatal("rebuild of changed sources:", p2, err)
	}
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() { syntax error }\n"), 0o644)
	if _, err := o.Prepare(ctx, info.Source, true); err == nil || !strings.Contains(err.Error(), "go build") {
		t.Fatal("build error must be reported:", err)
	}
}

// Слоты учёток манифеста — в согласии; изменение слотов — согласие заново.
func TestCredentialSlotsInConsent(t *testing.T) {
	dir := t.TempDir()
	o := Options{Dir: filepath.Join(dir, "connectors")}
	ctx := context.Background()
	m := func(v, bind string) []byte {
		return []byte("connector: lk\nversion: \"" + v + "\"\nruntime: wasm\ncapabilities:\n  services: [{ target: browser, contract: 1, actions: [open, login] }]\n" +
			"credentials:\n  - { slot: login, bind: [\"" + bind + "\"] }\nactions:\n  - { name: summary, class: write, calls: [browser.open, browser.login] }\n")
	}
	b, err := Pack(m("1", "https://login.mos.ru"), fakeWasm, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := o.Prepare(ctx, write(t, dir, "lk-1.g4c", b), true)
	if err != nil || !slices.Contains(p.Capabilities, "signs in on https://login.mos.ru with your account (slot login)") {
		t.Fatal(p, err)
	}
	if _, err := o.Install(p, p.Capabilities); err != nil {
		t.Fatal(err)
	}
	b, _ = Pack(m("2", "https://id.other.example"), fakeWasm, nil)
	if p, err = o.Prepare(ctx, write(t, dir, "lk-2.g4c", b), true); err != nil || !p.NeedsConsent {
		t.Fatal("changed credential slots without consent", p, err)
	}
	// Слот без bind манифест не принимает.
	b, _ = Pack([]byte("connector: lk\nversion: \"3\"\nruntime: wasm\ncredentials: [{ slot: login, bind: [] }]\nactions: []\n"), fakeWasm, nil)
	if _, err := o.Prepare(ctx, write(t, dir, "lk-3.g4c", b), true); err == nil {
		t.Fatal("slot without bind")
	}
}

func TestReinstallSource(t *testing.T) {
	for _, c := range []struct {
		in     Info
		source string
		dev    bool
	}{
		{Info{Source: "registry:mail@1.2.0", Trust: "verified"}, "mail@1.2.0", false},
		{Info{Source: SourceDir + "/src/bank", Trust: TrustLocal}, SourceDir + "/src/bank", true},
		{Info{Source: "https://example.com/bank.g4c", Trust: TrustLocal}, "https://example.com/bank.g4c", true},
		{Info{Source: "/tmp/bank.g4c", Trust: "publisher"}, "/tmp/bank.g4c", false},
	} {
		if s, dev := c.in.ReinstallSource(); s != c.source || dev != c.dev {
			t.Errorf("%+v: %q %v", c.in, s, dev)
		}
	}
}
