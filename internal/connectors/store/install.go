// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/gate4ai/sdk-go/registry"

	"github.com/gate4ai/gate4ai/internal/connectors/native"
	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
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
	Builtin      []string // имена коннекторов ядра: из магазина не ставятся
	Log          *slog.Logger
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
	NeedsConsent bool `json:"needs_consent"`
	// Native — нативный пакет: работает без песочницы, с правами пользователя.
	Native   bool     `json:"native,omitempty"`
	Warnings []string `json:"warnings,omitempty"`

	pkg *Package
}

// ErrConsent — установка требует согласия на возможности.
var ErrConsent = errors.New("consent required")

// ErrNativeThirdParty — нативный пакет не от gate4.ai (и не своя сборка владельца).
var ErrNativeThirdParty = errors.New("native packages are accepted only from gate4.ai for now (they run without a sandbox); " +
	"if you want to publish one, contact us at https://gate4.ai — we are open to it")

func (o Options) indexKeysOK() bool { return o.Registry != "" && len(o.RegistryKeys) > 0 }

// cachedIndex — индекс из последней проверенной загрузки (для отзыва без сети).
func (o Options) cachedIndex() *registry.Index {
	raw, err := os.ReadFile(filepath.Join(o.Dir, "registry-index.json"))
	if err != nil {
		return nil
	}
	sig, _ := os.ReadFile(filepath.Join(o.Dir, "registry-index.json.sig"))
	ix, err := registry.ParseIndex(raw, strings.TrimSpace(string(sig)), o.RegistryKeys)
	if err != nil {
		return nil
	}
	return ix
}

// CachedIndex — индекс registry из последней проверенной загрузки (nil — нет).
func (o Options) CachedIndex() *registry.Index { return o.cachedIndex() }

// RefreshIndex загружает индекс registry, проверяет подпись и кэширует его.
func (o Options) RefreshIndex(ctx context.Context) (*registry.Index, error) {
	if !o.indexKeysOK() {
		return nil, errors.New("registry is not configured (store.registry and store.registry_keys)")
	}
	raw, sig, err := IndexData(ctx, o.Registry)
	if err != nil {
		return nil, err
	}
	ix, err := registry.ParseIndex(raw, sig, o.RegistryKeys)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(o.Dir, 0o700)
	_ = os.WriteFile(filepath.Join(o.Dir, "registry-index.json"), raw, 0o600)
	_ = os.WriteFile(filepath.Join(o.Dir, "registry-index.json.sig"), []byte(sig), 0o600)
	return ix, nil
}

// SourceDir — префикс источника установки из папки с исходниками (Info.Source):
// update такого коннектора пересобирает его из той же папки.
const SourceDir = "dir:"

// SourceFolder — папка исходников, если source — папка с манифестом или "dir:<путь>".
func SourceFolder(source string) (string, bool) {
	path := strings.TrimPrefix(source, SourceDir)
	fi, err := os.Stat(filepath.Join(path, ManifestFile))
	if err != nil || fi.IsDir() {
		return "", false
	}
	abs, err := filepath.Abs(path)
	return abs, err == nil
}

func isFile(source string) bool {
	if strings.HasSuffix(source, PackageExt) {
		return true
	}
	fi, err := os.Stat(source)
	return err == nil && !fi.IsDir()
}

// Prepare проверяет пакет из файла или по адресу http(s) (.g4c), из папки с исходниками (сборка,
// только с dev) или из registry (имя[@версия]).
// dev — разрешить пакет владельца без доверенной подписи (доверие local).
func (o Options) Prepare(ctx context.Context, source string, dev bool) (*Plan, error) {
	var p *Plan
	var err error
	if dir, ok := SourceFolder(source); ok {
		p, err = o.prepareDir(ctx, dir, dev)
	} else if isURL(source) || isFile(source) {
		p, err = o.prepareFile(ctx, source, dev)
	} else {
		p, err = o.prepareRegistry(ctx, source)
	}
	if err != nil {
		return nil, err
	}
	m := p.pkg.Manifest
	if slices.Contains(o.Builtin, m.Connector) {
		return nil, fmt.Errorf("connector %s is part of gate4ai and is not installed from the store", m.Connector)
	}
	p.Connector, p.Version, p.Capabilities = m.Connector, m.Version, m.Summary()
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
	if m.Runtime == manifest.RuntimeNative {
		if p.Trust != registry.TrustVerified && p.Trust != TrustLocal {
			return nil, fmt.Errorf("connector %s: %s", m.Connector, ErrNativeThirdParty)
		}
		if p.pkg.Sig.Platform != HostPlatform() {
			return nil, fmt.Errorf("connector %s: the package is built for %s, this computer is %s", m.Connector, p.pkg.Sig.Platform, HostPlatform())
		}
		p.Native = true
		p.Warnings = append(p.Warnings, "native connector: runs as a separate program without a sandbox, with your user's rights")
	}
	if p.Trust == registry.TrustCommunity {
		p.Warnings = append(p.Warnings, "community connector: not reviewed by gate4.ai; only read actions will be available")
	}
	if p.Trust == TrustLocal {
		p.Warnings = append(p.Warnings, "local package not signed by a trusted key: install only connectors you built yourself")
	}
	return p, nil
}

// prepareFile — пакет .g4c из файла или по адресу http(s) (сборка не из магазина).
func (o Options) prepareFile(ctx context.Context, source string, dev bool) (*Plan, error) {
	b, err := fetchN(ctx, source, maxNativePackage)
	if err != nil {
		return nil, err
	}
	pkg, err := Open(b)
	if err != nil {
		return nil, err
	}
	p := &Plan{Source: source, pkg: pkg, Trust: TrustLocal}
	signedBy := func(keys []string) bool {
		_, ok := registry.VerifySig(keys, pkg.Sig.Digest, pkg.Sig.Signature)
		return pkg.Sig.Signature != "" && ok
	}
	var ix *registry.Index
	if o.indexKeysOK() {
		ix = o.cachedIndex()
	}
	switch {
	case signedBy(o.TrustedKeys):
		p.Trust = registry.TrustPublisher
	case ix != nil && trustOf(ix, pkg, signedBy, p):
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

// prepareDir собирает неподписанный пакет из исходников (Go) — своя сборка владельца.
func (o Options) prepareDir(ctx context.Context, dir string, dev bool) (*Plan, error) {
	if !dev {
		return nil, errors.New("installing from a source folder is your own build: use --dev")
	}
	man, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	m, err := manifest.Parse(man)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "g4abuild")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, ModuleFile)
	build := func() error { return wasm.BuildGo(ctx, dir, out) }
	if m.Runtime == manifest.RuntimeNative {
		out = filepath.Join(tmp, BinaryName(HostPlatform()))
		build = func() error { return native.BuildGo(ctx, dir, HostPlatform(), out) }
	}
	if err := build(); err != nil {
		return nil, err
	}
	wb, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	var b []byte
	if m.Runtime == manifest.RuntimeNative {
		b, err = PackNative(man, wb, HostPlatform(), nil)
	} else {
		b, err = Pack(man, wb, nil)
	}
	if err != nil {
		return nil, err
	}
	pkg, err := Open(b)
	if err != nil {
		return nil, err
	}
	return &Plan{Source: SourceDir + dir, pkg: pkg, Trust: TrustLocal}, nil
}

// trustOf — доверие к пакету по ключам издателя из индекса.
func trustOf(ix *registry.Index, pkg *Package, signedBy func([]string) bool, p *Plan) bool {
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
	pkg, err := FetchRelease(ctx, o.Registry, name, e.Publisher, rel, pub, HostPlatform())
	if err != nil {
		return nil, err
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

// ConsentOK — согласия consent достаточно для установки плана.
func (p *Plan) ConsentOK(consent []string) bool {
	return !p.NeedsConsent || slices.Equal(nonNil(consent), nonNil(p.Capabilities))
}

// Install устанавливает пакет плана. consent — возможности, на которые
// согласился владелец; они должны совпасть с планом, если согласие нужно.
func (o Options) Install(p *Plan, consent []string) (Info, error) {
	if !p.ConsentOK(consent) {
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
	type file struct {
		name string
		data []byte
		mode os.FileMode
	}
	files := []file{{ManifestFile, p.pkg.ManifestYAML, 0o600}, {ModuleFile, p.pkg.Wasm, 0o600}, {InfoFile, ib, 0o600}}
	if p.pkg.Native() {
		files[1] = file{BinaryName(p.pkg.Sig.Platform), p.pkg.Binary, 0o700}
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmp, f.name), f.data, f.mode); err != nil {
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
