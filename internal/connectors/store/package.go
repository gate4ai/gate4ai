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

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// Пакет коннектора (.g4c) — tar.gz из трёх файлов:
//
//	manifest.yaml   — манифест
//	connector.wasm  — модуль (runtime: wasm)
//	package.json    — Signature: дайджест и подпись издателя
//
// Дайджест — sha256 от "gate4ai-connector-v1\n" + sha256(manifest.yaml) + "\n" +
// sha256(connector.wasm) (hex). Подпись — Ed25519 над дайджестом.
const (
	SignatureFile = "package.json"
	PackageExt    = ".g4c"
	digestPrefix  = "gate4ai-connector-v1\n"
	maxPackage    = 64 << 20
)

// Signature — package.json.
type Signature struct {
	Connector string `json:"connector"`
	Version   string `json:"version"`
	Digest    string `json:"digest"`
	PublicKey string `json:"public_key,omitempty"` // ed25519:<base64>; доверие — не по этому полю, а по ключам registry/владельца
	Signature string `json:"signature,omitempty"`  // base64
}

// Package — разобранный пакет.
type Package struct {
	ManifestYAML []byte
	Wasm         []byte
	Manifest     manifest.Manifest
	Sig          Signature
}

// SignedBy — ключ из package.json, если подпись дайджеста этим ключом верна;
// "" — подписи нет или она неверна (ключ пакет заявляет о себе сам).
func (p *Package) SignedBy() string {
	if p.Sig.PublicKey == "" || p.Sig.Signature == "" {
		return ""
	}
	if _, ok := registry.VerifySig([]string{p.Sig.PublicKey}, p.Sig.Digest, p.Sig.Signature); !ok {
		return ""
	}
	return p.Sig.PublicKey
}

// SHA256 — sha256 (hex): файла пакета (для индекса registry) и частей дайджеста.
func SHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Digest — дайджест пакета.
func Digest(manifestYAML, wasmBytes []byte) string {
	return SHA256([]byte(digestPrefix + SHA256(manifestYAML) + "\n" + SHA256(wasmBytes)))
}

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
	}{{ManifestFile, manifestYAML, 0o644}, {ModuleFile, wasmBytes, 0o644}, {SignatureFile, sb, 0o644}} {
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
	if len(b) > maxPackage {
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
		data, err := io.ReadAll(io.LimitReader(tr, maxPackage))
		if err != nil {
			return nil, err
		}
		switch h.Name {
		case ManifestFile:
			p.ManifestYAML = data
		case ModuleFile:
			p.Wasm = data
		case SignatureFile:
			sigJSON = data
		default:
			return nil, fmt.Errorf("package: unexpected file %q", h.Name)
		}
	}
	if p.ManifestYAML == nil || sigJSON == nil || p.Wasm == nil {
		return nil, errors.New("package: manifest.yaml, connector.wasm and package.json are required")
	}
	if err := json.Unmarshal(sigJSON, &p.Sig); err != nil {
		return nil, fmt.Errorf("package.json: %w", err)
	}
	if p.Manifest, err = manifest.Parse(p.ManifestYAML); err != nil {
		return nil, err
	}
	if p.Manifest.Runtime != manifest.RuntimeWasm {
		return nil, fmt.Errorf("package: runtime must be wasm, got %q", p.Manifest.Runtime)
	}
	if p.Sig.Digest != Digest(p.ManifestYAML, p.Wasm) {
		return nil, errors.New("package: digest does not match its contents")
	}
	if p.Sig.Connector != p.Manifest.Connector || p.Sig.Version != p.Manifest.Version {
		return nil, errors.New("package: package.json does not match the manifest")
	}
	return p, nil
}
