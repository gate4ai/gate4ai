// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// CheckRelease проверяет скачанный пакет против записи индекса: sha256,
// дайджест, имя и версию, подпись ключом издателя, возможности, прошедшие ревью.
func CheckRelease(b []byte, name, publisher string, rel registry.Release, pub registry.Publisher) (*Package, error) {
	if SHA256(b) != rel.SHA256 {
		return nil, errors.New("package sha256 does not match the registry")
	}
	pkg, err := Open(b)
	if err != nil {
		return nil, err
	}
	if pkg.Sig.Digest != rel.Digest || pkg.Manifest.Connector != name || pkg.Manifest.Version != rel.Version {
		return nil, errors.New("package does not match the registry entry")
	}
	if _, ok := registry.VerifySig(pub.Keys, pkg.Sig.Digest, pkg.Sig.Signature); !ok {
		return nil, fmt.Errorf("package %s %s is not signed by publisher %s", name, rel.Version, publisher)
	}
	if !capsMatch(pkg.Manifest, rel.Capabilities) {
		return nil, errors.New("package capabilities differ from the reviewed capabilities in the registry")
	}
	return pkg, nil
}

// capsMatch — caps (согласие владельца или возможности версии в registry)
// совпадают с Summary манифеста.
func capsMatch(m manifest.Manifest, caps []string) bool {
	return slices.Equal(nonNil(m.Summary()), nonNil(caps))
}

// VerifyPackages скачивает пакеты всех версий индекса (base — адрес или путь
// индекса, от него считаются относительные url) и проверяет их CheckRelease.
// Для CI registry: pull request не пройдёт с записью, которой не соответствует
// опубликованный пакет.
func VerifyPackages(ctx context.Context, ix *registry.Index, base string) (checked int, errs []string) {
	for _, name := range slices.Sorted(maps.Keys(ix.Connectors)) {
		e := ix.Connectors[name]
		for _, rel := range e.Versions {
			if _, err := FetchRelease(ctx, base, name, e.Publisher, rel, ix.Publishers[e.Publisher], ""); err != nil {
				errs = append(errs, fmt.Sprintf("%s %s: %v", name, rel.Version, err))
				continue
			}
			checked++
		}
	}
	return checked, errs
}

// PackInfo — вывод gate4ai connectors pack (pack.json релиза).
type PackInfo struct {
	Package      string   `json:"package,omitempty"` // файл пакета (у нативного — список платформ)
	Connector    string   `json:"connector"`
	Version      string   `json:"version"`
	Digest       string   `json:"digest"`
	SHA256       string   `json:"sha256"`
	SignedBy     string   `json:"signed_by"`
	Capabilities []string `json:"capabilities"`
}

// AddRelease добавляет в индекс версию коннектора из pack.json. Новый издатель
// заводится с уровнем community и ключом, которым подписан пакет: уровень выше
// назначает ревьюер registry, а не автор. Новый коннектор требует repo и
// license. Версию нельзя заменить, а пакет должен быть подписан ключом,
// который у издателя уже есть: смена ключа — отдельное решение ревьюера.
func AddRelease(ix *registry.Index, publisher string, p PackInfo, url string, meta registry.Entry) error {
	if publisher == "" || p.Connector == "" || p.Version == "" || url == "" {
		return errors.New("publisher, connector, version and url are required")
	}
	if p.SignedBy == "" {
		return errors.New("the package is not signed (gate4ai connectors pack --key)")
	}
	if registry.Reserved(p.Connector) {
		return fmt.Errorf("connector name %s is reserved", p.Connector)
	}
	if ix.Publishers == nil {
		ix.Publishers = map[string]registry.Publisher{}
	}
	if ix.Connectors == nil {
		ix.Connectors = map[string]registry.Entry{}
	}
	pub, ok := ix.Publishers[publisher]
	if !ok {
		pub = registry.Publisher{Keys: []string{p.SignedBy}, Trust: registry.TrustCommunity}
	} else if !slices.Contains(pub.Keys, p.SignedBy) {
		return fmt.Errorf("publisher %s: the package is signed by %s, which is not a key of this publisher", publisher, p.SignedBy)
	}
	e, ok := ix.Connectors[p.Connector]
	if !ok {
		e = registry.Entry{Publisher: publisher, Repo: meta.Repo, Path: meta.Path, License: meta.License, Description: meta.Description}
		if !strings.HasPrefix(e.Repo, "https://") || e.License == "" {
			return fmt.Errorf("new connector %s: repo (https://…) and license are required", p.Connector)
		}
	} else if e.Publisher != publisher {
		return fmt.Errorf("connector %s belongs to publisher %s", p.Connector, e.Publisher)
	}
	if slices.ContainsFunc(e.Versions, func(r registry.Release) bool { return r.Version == p.Version }) {
		return fmt.Errorf("connector %s %s is already in the index (versions are immutable; publish a new version)", p.Connector, p.Version)
	}
	e.Versions = append(e.Versions, registry.Release{Version: p.Version, URL: url, SHA256: p.SHA256, Digest: p.Digest, Capabilities: nonNil(p.Capabilities)})
	ix.Publishers[publisher] = pub
	ix.Connectors[p.Connector] = e
	return ix.Check()
}
