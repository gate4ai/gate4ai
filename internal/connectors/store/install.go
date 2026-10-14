// SPDX-License-Identifier: FSL-1.1-ALv2

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// InfoFile — запись об установке (согласованные возможности, доверие, дайджест).
const InfoFile = "install.json"

// Info — install.json.
type Info struct {
	Connector    string    `json:"connector"`
	Version      string    `json:"version"`
	Digest       string    `json:"digest"`
	Trust        string    `json:"trust"`
	Publisher    string    `json:"publisher,omitempty"`
	Source       string    `json:"source"`
	Capabilities []string  `json:"capabilities"` // согласованные владельцем
	InstalledAt  time.Time `json:"installed_at"`
}

// Options — установка коннекторов.
type Options struct {
	Dir          string   // каталог установленных коннекторов
	Registry     string   // индекс registry: URL или путь ("" — без registry)
	RegistryKeys []string // ключи подписи индекса
	TrustedKeys  []string // ключи издателей, которым владелец доверяет без registry
}

// Plan — что будет установлено; показывается владельцу для согласия.
type Plan struct {
	Connector    string   `json:"connector"`
	Version      string   `json:"version"`
	Trust        string   `json:"trust"`
	Publisher    string   `json:"publisher,omitempty"`
	Source       string   `json:"source"`
	Repo         string   `json:"repo,omitempty"`
	Capabilities []string `json:"capabilities"`
	Classes      []string `json:"classes"`
	Installed    string   `json:"installed,omitempty"` // установленная версия
	// NeedsConsent — возможности новые или изменились по сравнению с согласованными.
	NeedsConsent bool     `json:"needs_consent"`
	Warnings     []string `json:"warnings,omitempty"`

	pkg *Package
}

// ErrConsent — установка требует согласия на возможности.
var ErrConsent = errors.New("consent required")

func (o Options) indexKeysOK() bool { return o.Registry != "" && len(o.RegistryKeys) > 0 }

// cachedIndex — индекс из последней проверенной загрузки (для отзыва без сети).
func (o Options) cachedIndex() *Index {
	raw, err := os.ReadFile(filepath.Join(o.Dir, "registry-index.json"))
	if err != nil {
		return nil
	}
	sig, _ := os.ReadFile(filepath.Join(o.Dir, "registry-index.json.sig"))
	ix, err := ParseIndex(raw, strings.TrimSpace(string(sig)), o.RegistryKeys)
	if err != nil {
		return nil
	}
	return ix
}

// RefreshIndex загружает индекс registry, проверяет подпись и кэширует его.
func (o Options) RefreshIndex(ctx context.Context) (*Index, error) {
	if !o.indexKeysOK() {
		return nil, errors.New("registry is not configured (store.registry and store.registry_keys)")
	}
	raw, sig, err := IndexData(ctx, o.Registry)
	if err != nil {
		return nil, err
	}
	ix, err := ParseIndex(raw, sig, o.RegistryKeys)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(o.Dir, 0o700)
	_ = os.WriteFile(filepath.Join(o.Dir, "registry-index.json"), raw, 0o600)
	_ = os.WriteFile(filepath.Join(o.Dir, "registry-index.json.sig"), []byte(sig), 0o600)
	return ix, nil
}

func isFile(source string) bool {
	if strings.HasSuffix(source, PackageExt) {
		return true
	}
	fi, err := os.Stat(source)
	return err == nil && !fi.IsDir()
}

// Prepare проверяет пакет из файла (.g4c) или из registry (имя[@версия]).
// dev — разрешить пакет владельца без доверенной подписи (доверие local).
func (o Options) Prepare(ctx context.Context, source string, dev bool) (*Plan, error) {
	var p *Plan
	var err error
	if isFile(source) {
		p, err = o.prepareFile(ctx, source, dev)
	} else {
		p, err = o.prepareRegistry(ctx, source)
	}
	if err != nil {
		return nil, err
	}
	m := p.pkg.Manifest
	p.Connector, p.Version, p.Capabilities = m.Connector, m.Version, m.Capabilities.Summary()
	if p.Capabilities == nil {
		p.Capabilities = []string{}
	}
	classes := map[string]bool{}
	for _, a := range m.Actions {
		classes[a.Class] = true
	}
	for c := range classes {
		p.Classes = append(p.Classes, c)
	}
	slices.Sort(p.Classes)
	p.NeedsConsent = true
	if info, err := o.Info(m.Connector); err == nil {
		p.Installed = info.Version
		p.NeedsConsent = !slices.Equal(info.Capabilities, p.Capabilities)
	}
	if p.Trust == TrustCommunity {
		p.Warnings = append(p.Warnings, "community connector: not reviewed by gate4.ai; only read actions will be available")
	}
	if p.Trust == TrustLocal {
		p.Warnings = append(p.Warnings, "local package not signed by a trusted key: install only connectors you built yourself")
	}
	return p, nil
}

func (o Options) prepareFile(ctx context.Context, source string, dev bool) (*Plan, error) {
	b, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	pkg, err := Open(b)
	if err != nil {
		return nil, err
	}
	p := &Plan{Source: source, pkg: pkg, Trust: TrustLocal}
	signedBy := func(keys []string) bool {
		_, ok := VerifySig(keys, pkg.Sig.Digest, pkg.Sig.Signature)
		return pkg.Sig.Signature != "" && ok
	}
	var ix *Index
	if o.indexKeysOK() {
		ix = o.cachedIndex()
	}
	switch {
	case signedBy(o.TrustedKeys):
		p.Trust = TrustPublisher
	case ix != nil && ix.trustOf(pkg, signedBy, p):
	case dev:
	default:
		return nil, errors.New("package is not signed by a trusted key (store.trusted_keys or registry publishers); use --dev to install a package you built yourself")
	}
	if ix != nil {
		if rev, why := ix.IsRevoked(pkg.Manifest.Connector, pkg.Manifest.Version); rev {
			return nil, fmt.Errorf("connector %s %s is revoked: %s", pkg.Manifest.Connector, pkg.Manifest.Version, why)
		}
	}
	return p, nil
}

// trustOf — доверие к пакету по ключам издателя из индекса.
func (ix *Index) trustOf(pkg *Package, signedBy func([]string) bool, p *Plan) bool {
	e, ok := ix.Connectors[pkg.Manifest.Connector]
	if !ok {
		return false
	}
	pub := ix.Publishers[e.Publisher]
	if !signedBy(pub.Keys) {
		return false
	}
	p.Trust, p.Publisher, p.Repo = pub.Trust, e.Publisher, e.Repo
	return true
}

func (o Options) prepareRegistry(ctx context.Context, source string) (*Plan, error) {
	ix, err := o.RefreshIndex(ctx)
	if err != nil {
		return nil, err
	}
	name, version, _ := strings.Cut(source, "@")
	e, rel, err := ix.Resolve(name, version)
	if err != nil {
		return nil, err
	}
	pub := ix.Publishers[e.Publisher]
	ref := resolveRef(o.Registry, rel.URL)
	b, err := fetch(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ref, err)
	}
	pkg, err := CheckRelease(b, name, e.Publisher, rel, pub)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return &Plan{Source: "registry:" + name + "@" + rel.Version, Trust: pub.Trust, Publisher: e.Publisher, Repo: e.Repo, pkg: pkg}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// Install устанавливает пакет плана. consent — возможности, на которые
// согласился владелец; они должны совпасть с планом, если согласие нужно.
func (o Options) Install(p *Plan, consent []string) (Info, error) {
	if p.NeedsConsent && !slices.Equal(nonNil(consent), nonNil(p.Capabilities)) {
		return Info{}, ErrConsent
	}
	name := p.Connector
	tmp, err := os.MkdirTemp(o.Dir, ".install-"+name+"-")
	if err != nil {
		if err := os.MkdirAll(o.Dir, 0o700); err != nil {
			return Info{}, err
		}
		if tmp, err = os.MkdirTemp(o.Dir, ".install-"+name+"-"); err != nil {
			return Info{}, err
		}
	}
	defer os.RemoveAll(tmp)
	info := Info{Connector: name, Version: p.Version, Digest: p.pkg.Sig.Digest, Trust: p.Trust, Publisher: p.Publisher,
		Source: p.Source, Capabilities: nonNil(p.Capabilities), InstalledAt: time.Now().UTC()}
	ib, _ := json.MarshalIndent(info, "", "  ")
	for _, f := range []struct {
		name string
		data []byte
	}{{ManifestFile, p.pkg.ManifestYAML}, {ModuleFile, p.pkg.Wasm}, {InfoFile, ib}} {
		if err := os.WriteFile(filepath.Join(tmp, f.name), f.data, 0o600); err != nil {
			return Info{}, err
		}
	}
	dst := filepath.Join(o.Dir, name)
	old := dst + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			return Info{}, err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(old, dst)
		return Info{}, err
	}
	_ = os.RemoveAll(old)
	return info, nil
}

// Info — запись об установке коннектора.
func (o Options) Info(name string) (Info, error) {
	var info Info
	b, err := os.ReadFile(filepath.Join(o.Dir, name, InfoFile))
	if err != nil {
		return info, err
	}
	return info, json.Unmarshal(b, &info)
}

// Remove удаляет установленный коннектор (данные KV остаются в data/).
func (o Options) Remove(name string) error {
	if !nameOK(name) {
		return fmt.Errorf("bad connector name %q", name)
	}
	dir := filepath.Join(o.Dir, name)
	if _, err := os.Stat(filepath.Join(dir, InfoFile)); err != nil {
		return fmt.Errorf("connector %s is not installed", name)
	}
	return os.RemoveAll(dir)
}

func nameOK(n string) bool {
	if n == "" || n == "data" {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
