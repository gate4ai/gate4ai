// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/sdk-go/registry"
)

// Пакет коннектора (.g4c) — tar.gz из трёх файлов:
//
//	manifest.yaml   — манифест
//	connector.wasm  — модуль (runtime: wasm) или
//	connector       — бинарник под одну платформу (runtime: native; на Windows — connector.exe)
//	package.json    — Signature: дайджест и подпись издателя (у нативного — и платформа)
//
// Дайджест — sha256 от "gate4ai-connector-v1\n" + sha256(manifest.yaml) + "\n" +
// sha256(connector.wasm) (hex); у нативного — от "gate4ai-connector-native-v1\n" +
// sha256(manifest.yaml) + "\n" + платформа + "\n" + sha256(бинарника). Подпись —
// Ed25519 над дайджестом.
const (
	SignatureFile      = "package.json"
	PackageExt         = ".g4c"
	digestPrefix       = "gate4ai-connector-v1\n"
	nativeDigestPrefix = "gate4ai-connector-native-v1\n"
	maxPackage         = 64 << 20
	// maxNativePackage — нативный бинарник (с Chrome-драйвером, Stagehand) больше модуля.
	maxNativePackage = 256 << 20
)

// BinaryName — имя бинарника нативного пакета на платформе "os/arch".
func BinaryName(platform string) string {
	if strings.HasPrefix(platform, "windows/") {
		return NativeFile + ".exe"
	}
	return NativeFile
}

// NativeFile — бинарник нативного пакета (без расширения).
const NativeFile = "connector"

// Signature — package.json.
type Signature struct {
	Connector string `json:"connector"`
	Version   string `json:"version"`
	Platform  string `json:"platform,omitempty"` // "os/arch" — только у нативного пакета
	Digest    string `json:"digest"`
	PublicKey string `json:"public_key,omitempty"` // ed25519:<base64>; доверие — не по этому полю, а по ключам registry/владельца
	Signature string `json:"signature,omitempty"`  // base64
}

// Package — разобранный пакет.
type Package struct {
	ManifestYAML []byte
	Wasm         []byte // runtime: wasm
	Binary       []byte // runtime: native — бинарник под Sig.Platform
	Manifest     manifest.Manifest
	Sig          Signature
}

// Native — нативный пакет.
func (p *Package) Native() bool { return p.Manifest.Runtime == manifest.RuntimeNative }

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Digest — дайджест пакета.
func Digest(manifestYAML, wasmBytes []byte) string {
	return sum([]byte(digestPrefix + sum(manifestYAML) + "\n" + sum(wasmBytes)))
}

// DigestNative — дайджест нативного пакета.
func DigestNative(manifestYAML []byte, platform string, bin []byte) string {
	return sum([]byte(nativeDigestPrefix + sum(manifestYAML) + "\n" + platform + "\n" + sum(bin)))
}

// SHA256 — sha256 файла пакета (для индекса registry).
func SHA256(b []byte) string { return sum(b) }

// Pack собирает пакет; key nil — без подписи (только для локальной установки владельцем).
func Pack(manifestYAML, wasmBytes []byte, key ed25519.PrivateKey) ([]byte, error) {
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		return nil, err
	}
	if m.Runtime != manifest.RuntimeWasm {
		return nil, fmt.Errorf("manifest: runtime must be wasm, got %q", m.Runtime)
	}
	if !bytes.HasPrefix(wasmBytes, []byte("\x00asm")) {
		return nil, errors.New("connector.wasm is not a WebAssembly module")
	}
	sig := Signature{Connector: m.Connector, Version: m.Version, Digest: Digest(manifestYAML, wasmBytes)}
	return writePackage(manifestYAML, ModuleFile, wasmBytes, sig, key)
}

// PackNative собирает нативный пакет под платформу; key nil — без подписи.
func PackNative(manifestYAML, bin []byte, platform string, key ed25519.PrivateKey) ([]byte, error) {
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		return nil, err
	}
	if m.Runtime != manifest.RuntimeNative {
		return nil, fmt.Errorf("manifest: runtime must be native, got %q", m.Runtime)
	}
	if !slices.Contains(m.Platforms, platform) {
		return nil, fmt.Errorf("platform %s is not in manifest.platforms", platform)
	}
	if len(bin) == 0 {
		return nil, errors.New("empty connector binary")
	}
	sig := Signature{Connector: m.Connector, Version: m.Version, Platform: platform, Digest: DigestNative(manifestYAML, platform, bin)}
	return writePackage(manifestYAML, BinaryName(platform), bin, sig, key)
}

// binMode — модуль WASM — данные, нативный бинарник — исполняемый.
func binMode(name string) int64 {
	if name == ModuleFile {
		return 0o644
	}
	return 0o755
}

func writePackage(manifestYAML []byte, binName string, bin []byte, sig Signature, key ed25519.PrivateKey) ([]byte, error) {
	if key != nil {
		sig.PublicKey = registry.FormatPublicKey(key.Public().(ed25519.PublicKey))
		sig.Signature = registry.Sign(key, sig.Digest)
	}
	sb, _ := json.MarshalIndent(sig, "", "  ")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
		mode int64
	}{{ManifestFile, manifestYAML, 0o644}, {binName, bin, binMode(binName)}, {SignatureFile, sb, 0o644}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Open разбирает пакет и проверяет дайджест (не подпись: доверие решает Installer).
func Open(b []byte) (*Package, error) {
	if len(b) > maxNativePackage {
		return nil, errors.New("package is too large")
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("package: %w", err)
	}
	tr := tar.NewReader(gz)
	p := &Package{}
	var sigJSON []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("package: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxNativePackage))
		if err != nil {
			return nil, err
		}
		switch h.Name {
		case ManifestFile:
			p.ManifestYAML = data
		case ModuleFile:
			p.Wasm = data
		case NativeFile, NativeFile + ".exe":
			p.Binary = data
		case SignatureFile:
			sigJSON = data
		default:
			return nil, fmt.Errorf("package: unexpected file %q", h.Name)
		}
	}
	if p.ManifestYAML == nil || sigJSON == nil || (p.Wasm == nil) == (p.Binary == nil) {
		return nil, errors.New("package: manifest.yaml, package.json and one of connector.wasm or connector are required")
	}
	if err := json.Unmarshal(sigJSON, &p.Sig); err != nil {
		return nil, fmt.Errorf("package.json: %w", err)
	}
	if p.Manifest, err = manifest.Parse(p.ManifestYAML); err != nil {
		return nil, err
	}
	if p.Native() {
		if p.Binary == nil || !slices.Contains(p.Manifest.Platforms, p.Sig.Platform) {
			return nil, errors.New("package: a native package needs the connector binary for a platform from manifest.platforms")
		}
		if p.Sig.Digest != DigestNative(p.ManifestYAML, p.Sig.Platform, p.Binary) {
			return nil, errors.New("package: digest does not match its contents")
		}
	} else if p.Wasm == nil || p.Sig.Platform != "" || p.Sig.Digest != Digest(p.ManifestYAML, p.Wasm) {
		return nil, errors.New("package: digest does not match its contents")
	}
	if p.Sig.Connector != p.Manifest.Connector || p.Sig.Version != p.Manifest.Version {
		return nil, errors.New("package: package.json does not match the manifest")
	}
	return p, nil
}
