// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/sdk-go/registry"

	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
)

var nativeBin = []byte("#!/bin/sh\nexit 0\n")

func nativeManifest(version string, plats ...string) []byte {
	if len(plats) == 0 {
		plats = []string{HostPlatform()}
	}
	return []byte("connector: bank\nversion: \"" + version + "\"\nruntime: native\nplatforms: [" + strings.Join(plats, ", ") +
		"]\nactions:\n  - { name: read, class: read }\n")
}

func packNative(t *testing.T, m []byte, plat, priv string) []byte {
	t.Helper()
	var b []byte
	var err error
	if priv == "" {
		b, err = PackNative(m, nativeBin, plat, nil)
	} else {
		k, _ := registry.ParsePrivateKey(priv)
		b, err = PackNative(m, nativeBin, plat, k)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNativePackage(t *testing.T) {
	_, priv := keys(t)
	b := packNative(t, nativeManifest("1", "linux/amd64", "windows/amd64"), "windows/amd64", priv)
	p, err := Open(b)
	if err != nil || !p.Native() || p.Sig.Platform != "windows/amd64" || string(p.Binary) != string(nativeBin) || p.Wasm != nil {
		t.Fatal(p, err)
	}
	if p.Sig.Digest != DigestNative(p.ManifestYAML, "windows/amd64", nativeBin) || p.Sig.Digest == DigestNative(p.ManifestYAML, "linux/amd64", nativeBin) {
		t.Fatal("digest must cover the platform")
	}
	if _, err := PackNative(nativeManifest("1", "linux/amd64"), nativeBin, "darwin/arm64", nil); err == nil {
		t.Fatal("platform outside manifest.platforms")
	}
	if _, err := PackNative(manifestYAML("1", "x.example", false), nativeBin, HostPlatform(), nil); err == nil {
		t.Fatal("wasm manifest packed as native")
	}
	if _, err := Pack(nativeManifest("1"), fakeWasm, nil); err == nil {
		t.Fatal("native manifest packed as wasm")
	}
}

// Нативный пакет ставится только от проверенного gate4.ai издателя или как
// своя сборка владельца; на экране согласия — предупреждение.
func TestNativeTrust(t *testing.T) {
	for _, tc := range []struct {
		trust string
		ok    bool
	}{{registry.TrustVerified, true}, {registry.TrustPublisher, false}, {registry.TrustCommunity, false}} {
		r := newRegistry(t, tc.trust)
		r.publishNative(t, "1")
		p, err := r.o.Prepare(context.Background(), "bank", false)
		if tc.ok != (err == nil) {
			t.Fatalf("%s: %v", tc.trust, err)
		}
		if !tc.ok {
			if !strings.Contains(err.Error(), "contact us") {
				t.Fatalf("%s: no contact hint: %v", tc.trust, err)
			}
			continue
		}
		if !p.Native || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "without a sandbox") {
			t.Fatal(p)
		}
		if _, err := r.o.Install(p, p.Capabilities); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(r.o.Dir, "bank", BinaryName(HostPlatform()))
		if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o100 == 0 {
			t.Fatal("binary not installed as executable", err)
		}
		e, _ := wasm.NewEngine("", wasm.Services{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		c, err := r.o.Load("bank", e)
		if err != nil || c.Manifest().Runtime != manifest.RuntimeNative {
			t.Fatal(c, err)
		}
		c.Close()
		os.WriteFile(bin, append(nativeBin, '#'), 0o700)
		if err := r.o.Check("bank"); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatal("tampered binary:", err)
		}
	}
	// Своя сборка владельца (--dev) — можно.
	dir := t.TempDir()
	f := write(t, dir, "bank.g4c", packNative(t, nativeManifest("1"), HostPlatform(), ""))
	o := Options{Dir: filepath.Join(dir, "connectors")}
	if p, err := o.Prepare(context.Background(), f, true); err != nil || p.Trust != TrustLocal || !p.Native {
		t.Fatal(p, err)
	}
	// Пакет другой платформы не ставится.
	other := "linux/arm64"
	if HostPlatform() == other {
		other = "linux/amd64"
	}
	f = write(t, dir, "other.g4c", packNative(t, nativeManifest("1", HostPlatform(), other), other, ""))
	if _, err := o.Prepare(context.Background(), f, true); err == nil || !strings.Contains(err.Error(), "built for") {
		t.Fatal("foreign platform:", err)
	}
}

// publishNative — версия нативного пакета: пакет этой платформы и список платформ.
func (r *registryEnv) publishNative(t *testing.T, version string) {
	m := nativeManifest(version)
	b := packNative(t, m, HostPlatform(), r.pubPriv)
	pkgs := filepath.Join(r.dir, "registry", "packages")
	file := "bank-" + version + "-" + strings.ReplaceAll(HostPlatform(), "/", "-") + PackageExt
	write(t, pkgs, file, b)
	p, _ := Open(b)
	set := PlatformSet{Connector: "bank", Version: version, Platforms: map[string]Artifact{HostPlatform(): {URL: file, SHA256: SHA256(b), Digest: p.Sig.Digest}}}
	sb, _ := json.Marshal(set)
	write(t, pkgs, "bank-"+version+SetExt, sb)
	e := r.index.Connectors["bank"]
	e.Versions = append(e.Versions, registry.Release{Version: version, URL: "packages/bank-" + version + SetExt, SHA256: SHA256(sb),
		Digest: SetDigest(set), Capabilities: p.Manifest.Capabilities.Summary()})
	r.index.Connectors["bank"] = e
	r.save(t)
}

func TestRegistryPlatformSet(t *testing.T) {
	r := newRegistry(t, registry.TrustVerified)
	r.publishNative(t, "2")
	ix, err := r.o.RefreshIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n, errs := VerifyPackages(context.Background(), ix, r.o.Registry); n != 1 || len(errs) != 0 {
		t.Fatal(n, errs)
	}
	e, rel, _ := ix.Resolve("bank", "")
	if _, err := FetchRelease(context.Background(), r.o.Registry, "bank", e.Publisher, rel, ix.Publishers[e.Publisher], "plan9/386"); err == nil ||
		!strings.Contains(err.Error(), "no package for plan9/386") {
		t.Fatal("missing platform:", err)
	}
	// Список изменён после публикации — не совпадает с индексом.
	setFile := filepath.Join(r.dir, "registry", "packages", "bank-2"+SetExt)
	sb, _ := os.ReadFile(setFile)
	os.WriteFile(setFile, append(sb, ' '), 0o644)
	if _, err := r.o.Prepare(context.Background(), "bank", false); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatal("tampered list:", err)
	}
	if !IsSet(rel.URL) || IsSet("bank-1.g4c") {
		t.Fatal("IsSet")
	}
}
