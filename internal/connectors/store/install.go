// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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

// ReinstallSource — откуда поставить заново ту же установку: из registry — та же
// версия (имя@версия), из папки с исходниками — пересборка (dev), из файла или
// по адресу — тот же пакет; dev — своя сборка без доверенной подписи.
func (i Info) ReinstallSource() (source string, dev bool) {
	if s, ok := strings.CutPrefix(i.Source, "registry:"); ok {
		return s, false
	}
	return i.Source, i.Trust == TrustLocal
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

// CachedIndex — индекс registry из последней проверенной загрузки (для отзыва
// без сети; nil — нет).
func (o Options) CachedIndex() *registry.Index {
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
	// Откат: индекс старше закэшированного (до отзыва версии) или просроченный
	// не принимается и кэш не заменяет.
	if err := ix.CheckNewer(o.CachedIndex(), time.Now()); err != nil {
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

// IsName — source — имя коннектора в магазине (name или name@version), а не путь:
// без разделителей каталогов и ":" (адрес, "dir:"), не .g4c, не "." и "..".
// Одноимённый файл или папка в текущем каталоге его путём не делают.
func IsName(source string) bool {
	return source != "" && source != "." && source != ".." &&
		!strings.ContainsAny(source, `/\:`) && !strings.HasSuffix(source, PackageExt)
}

// Prepare проверяет пакет из файла или по адресу http(s) (.g4c), из папки с исходниками (сборка,
// только с dev) или из registry (имя[@версия]).
// dev — разрешить пакет владельца без доверенной подписи (доверие local).
func (o Options) Prepare(ctx context.Context, source string, dev bool) (*Plan, error) {
	var p *Plan
	var err error
	if IsName(source) {
		p, err = o.prepareRegistry(ctx, source)
	} else if dir, ok := SourceFolder(source); ok {
		p, err = o.prepareDir(ctx, dir, dev)
	} else {
		p, err = o.prepareFile(ctx, source, dev)
	}
	if err != nil {
		return nil, err
	}
	m := p.pkg.Manifest
	if slices.Contains(o.Builtin, m.Connector) {
		return nil, fmt.Errorf("connector %s is part of gate4ai and is not installed from the store", m.Connector)
	}
	if registry.Reserved(m.Connector) {
		return nil, fmt.Errorf("connector name %s is reserved by gate4ai", m.Connector)
	}
	if err := m.Limits.CheckInstall(); err != nil {
		return nil, fmt.Errorf("connector %s: %w", m.Connector, err)
	}
	p.Connector, p.Version, p.Capabilities = m.Connector, m.Version, m.Summary()
	if p.Capabilities == nil {
		p.Capabilities = []string{}
	}
	classes := map[string]bool{}
	for _, a := range m.Actions {
		classes[a.Class] = true
	}
	p.Classes = slices.Sorted(maps.Keys(classes))
	p.NeedsConsent = true
	if info, err := o.Info(m.Connector); err == nil {
		p.Installed = info.Version
		p.NeedsConsent = !slices.Equal(nonNil(info.Capabilities), nonNil(p.Capabilities))
	}
	if owner, ok := o.dataOwner(m.Connector); ok && owner != p.owner() {
		p.Warnings = append(p.Warnings, fmt.Sprintf("connector %s from another publisher was installed under this name: its stored data will be erased", m.Connector))
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
	if !bytes.HasPrefix(b, []byte{0x1f, 0x8b}) { // пакет — tar.gz
		return nil, fmt.Errorf("%s: not a gate4ai package (%s)", source, PackageExt)
	}
	pkg, err := Open(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	p := &Plan{Source: source, pkg: pkg, Trust: TrustLocal}
	signedBy := func(keys []string) bool {
		_, ok := registry.VerifySig(keys, pkg.Sig.Digest, pkg.Sig.Signature)
		return pkg.Sig.Signature != "" && ok
	}
	var ix *registry.Index
	if o.indexKeysOK() {
		ix = o.CachedIndex()
	}
	var why error
	trusted := signedBy(o.TrustedKeys)
	if trusted {
		p.Trust = registry.TrustPublisher
	} else if ix != nil {
		trusted, why = trustOf(ix, pkg, signedBy, p)
	}
	if !trusted && !dev {
		if why != nil {
			return nil, fmt.Errorf("%w; use --dev to install it as your own build (trust local)", why)
		}
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

// trustOf — доверие к пакету из файла по ключам издателя из индекса. Уровень
// издателя пакет получает, только если его версия есть в индексе и совпадает
// с прошедшей ревью записью (дайджест и возможности — как в CheckRelease);
// иначе ошибка с причиной (подпись издателя без записи — не ревью registry).
func trustOf(ix *registry.Index, pkg *Package, signedBy func([]string) bool, p *Plan) (bool, error) {
	m := pkg.Manifest
	e, ok := ix.Connectors[m.Connector]
	if !ok {
		return false, nil
	}
	pub := ix.Publishers[e.Publisher]
	if !signedBy(pub.Keys) {
		return false, nil
	}
	for _, rel := range e.Versions {
		if rel.Version != m.Version {
			continue
		}
		if rel.Native() || pkg.Native() {
			// Дайджест платформы — в списке платформ на сервере registry.
			return false, fmt.Errorf("native connector %s %s from a file cannot be matched with the registry entry: install it from the registry", m.Connector, m.Version)
		}
		if pkg.Sig.Digest != rel.Digest || !capsMatch(m, rel.Capabilities) {
			return false, fmt.Errorf("connector %s %s does not match its reviewed entry in the registry (digest or capabilities)", m.Connector, m.Version)
		}
		p.Trust, p.Publisher, p.Repo = pub.Trust, e.Publisher, e.Repo
		return true, nil
	}
	return false, fmt.Errorf("connector %s %s is signed by publisher %s but is not in the registry index (not reviewed)", m.Connector, m.Version, e.Publisher)
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

// DataDir — каталог KV коннекторов в каталоге установленных (<Dir>/data/<имя>).
const DataDir = "data"

// ownerFile — чьи данные лежат в <Dir>/data/<имя> (издатель коннектора).
const ownerFile = "owner"

// owner — кому принадлежат данные коннектора плана (ownerID).
func (p *Plan) owner() string { return ownerID(p.Publisher, p.Trust, p.pkg.Sig.PublicKey) }

// ownerID — владелец данных коннектора: издатель из registry, своя сборка
// владельца или ключ доверенного издателя (store.trusted_keys).
func ownerID(publisher, trust, key string) string {
	switch {
	case publisher != "":
		return "publisher:" + publisher
	case trust == TrustLocal:
		return "local"
	}
	return "key:" + key
}

// dataOwner — владелец данных KV коннектора из метки в каталоге данных;
// false — данных (или метки) нет.
func (o Options) dataOwner(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(o.Dir, DataDir, name, ownerFile))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// claimData — данные KV коннектора принадлежат издателю плана: данные другого
// издателя под тем же именем стираются (новый коннектор не читает чужие
// токены и состояние), метка владельца обновляется.
func (o Options) claimData(p *Plan) error {
	dir := filepath.Join(o.Dir, DataDir, p.Connector)
	if owner, ok := o.dataOwner(p.Connector); ok && owner != p.owner() {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ownerFile), []byte(p.owner()+"\n"), 0o600)
}

// Install устанавливает пакет плана. consent — возможности, на которые
// согласился владелец; они должны совпасть с планом, если согласие нужно.
func (o Options) Install(p *Plan, consent []string) (Info, error) {
	if !p.ConsentOK(consent) {
		return Info{}, ErrConsent
	}
	name := p.Connector
	if !nameOK(name) {
		return Info{}, fmt.Errorf("connector name %q is reserved or invalid", name)
	}
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
	if err := o.claimData(p); err != nil {
		o.log().Warn("connector data not claimed", "connector", name, "err", err)
	}
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

// Remove удаляет установленный коннектор. Данные KV остаются в data/ с меткой
// издателя: их получит только коннектор того же издателя (claimData).
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
	if n == "" || registry.Reserved(n) {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func (o Options) log() *slog.Logger {
	if o.Log != nil {
		return o.Log
	}
	return slog.Default()
}
