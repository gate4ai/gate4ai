// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// CheckRelease проверяет скачанный пакет против записи индекса: sha256,
// дайджест, имя и версию, подпись ключом издателя, возможности, прошедшие ревью.
func CheckRelease(b []byte, name, publisher string, rel Release, pub Publisher) (*Package, error) {
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
	if _, ok := VerifySig(pub.Keys, pkg.Sig.Digest, pkg.Sig.Signature); !ok {
		return nil, fmt.Errorf("package %s %s is not signed by publisher %s", name, rel.Version, publisher)
	}
	if !slices.Equal(pkg.Manifest.Capabilities.Summary(), nonNil(rel.Capabilities)) {
		return nil, errors.New("package capabilities differ from the reviewed capabilities in the registry")
	}
	return pkg, nil
}

// VerifyPackages скачивает пакеты всех версий индекса (base — адрес или путь
// индекса, от него считаются относительные url) и проверяет их CheckRelease.
// Для CI registry: pull request не пройдёт с записью, которой не соответствует
// опубликованный пакет.
func (ix *Index) VerifyPackages(ctx context.Context, base string) (checked int, errs []string) {
	names := make([]string, 0, len(ix.Connectors))
	for n := range ix.Connectors {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		e := ix.Connectors[name]
		for _, rel := range e.Versions {
			ref := resolveRef(base, rel.URL)
			b, err := fetch(ctx, ref)
			if err == nil {
				_, err = CheckRelease(b, name, e.Publisher, rel, ix.Publishers[e.Publisher])
			}
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s %s (%s): %v", name, rel.Version, ref, err))
				continue
			}
			checked++
		}
	}
	return checked, errs
}

// PackInfo — вывод gate4ai connectors pack (pack.json релиза).
type PackInfo struct {
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
func (ix *Index) AddRelease(publisher string, p PackInfo, url string, meta Entry) error {
	if publisher == "" || p.Connector == "" || p.Version == "" || url == "" {
		return errors.New("publisher, connector, version and url are required")
	}
	if p.SignedBy == "" {
		return errors.New("the package is not signed (gate4ai connectors pack --key)")
	}
	if ix.Publishers == nil {
		ix.Publishers = map[string]Publisher{}
	}
	if ix.Connectors == nil {
		ix.Connectors = map[string]Entry{}
	}
	pub, ok := ix.Publishers[publisher]
	if !ok {
		pub = Publisher{Keys: []string{p.SignedBy}, Trust: TrustCommunity}
	} else if !slices.Contains(pub.Keys, p.SignedBy) {
		return fmt.Errorf("publisher %s: the package is signed by %s, which is not a key of this publisher", publisher, p.SignedBy)
	}
	e, ok := ix.Connectors[p.Connector]
	if !ok {
		e = Entry{Publisher: publisher, Repo: meta.Repo, License: meta.License, Description: meta.Description}
		if !strings.HasPrefix(e.Repo, "https://") || e.License == "" {
			return fmt.Errorf("new connector %s: repo (https://…) and license are required", p.Connector)
		}
	} else if e.Publisher != publisher {
		return fmt.Errorf("connector %s belongs to publisher %s", p.Connector, e.Publisher)
	}
	for _, r := range e.Versions {
		if r.Version == p.Version {
			return fmt.Errorf("connector %s %s is already in the index (versions are immutable; publish a new version)", p.Connector, p.Version)
		}
	}
	e.Versions = append(e.Versions, Release{Version: p.Version, URL: url, SHA256: p.SHA256, Digest: p.Digest, Capabilities: nonNil(p.Capabilities)})
	ix.Publishers[publisher] = pub
	ix.Connectors[p.Connector] = e
	return ix.Check()
}
