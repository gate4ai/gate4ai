// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// Нативный пакет — по .g4c на платформу и список платформ
// <connector>-<version>.platforms.json. Запись индекса registry ссылается на
// список теми же полями, что на пакет WASM: url — на список, sha256 — его
// файла, digest — SetDigest.
const SetExt = ".platforms.json"

// PlatformSet — список платформ нативного пакета.
type PlatformSet struct {
	Connector string              `json:"connector"`
	Version   string              `json:"version"`
	Platforms map[string]Artifact `json:"platforms"`
}

// Artifact — пакет под одну платформу: url относительно списка, sha256 файла, дайджест пакета.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Digest string `json:"digest"`
}

// IsSet — ссылка на список платформ нативного пакета.
func IsSet(ref string) bool { return strings.HasSuffix(ref, SetExt) }

// HostPlatform — платформа этого компьютера "os/arch".
func HostPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// SetDigest — дайджест набора: дайджесты пакетов по платформам в порядке имён.
func SetDigest(s PlatformSet) string {
	var b strings.Builder
	b.WriteString("gate4ai-connector-set-v1\n" + s.Connector + "\n" + s.Version + "\n")
	for _, p := range setPlatforms(s) {
		b.WriteString(p + " " + s.Platforms[p].Digest + "\n")
	}
	return SHA256([]byte(b.String()))
}

// ParseSet читает список платформ.
func ParseSet(b []byte) (PlatformSet, error) {
	var s PlatformSet
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("platforms list: %w", err)
	}
	if s.Connector == "" || s.Version == "" || len(s.Platforms) == 0 {
		return s, errors.New("platforms list: connector, version and platforms are required")
	}
	for p, a := range s.Platforms {
		if a.URL == "" || len(a.SHA256) != 64 || len(a.Digest) != 64 {
			return s, fmt.Errorf("platforms list: %s: url, sha256 and digest are required", p)
		}
	}
	return s, nil
}

// FetchRelease скачивает и проверяет версию из индекса (base — адрес индекса):
// пакет WASM или, для нативного, пакет под платформу platform ("" — все
// платформы списка, для CI registry; возвращается пакет первой по имени).
func FetchRelease(ctx context.Context, base, name, publisher string, rel registry.Release, pub registry.Publisher, platform string) (*Package, error) {
	ref := ResolveRef(base, rel.URL)
	b, err := Fetch(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ref, err)
	}
	if !IsSet(rel.URL) {
		pkg, err := CheckRelease(b, name, publisher, rel, pub)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ref, err)
		}
		if pkg.Native() {
			return nil, fmt.Errorf("%s: a native package is published with a platforms list (%s)", ref, SetExt)
		}
		return pkg, nil
	}
	if SHA256(b) != rel.SHA256 {
		return nil, fmt.Errorf("%s: platforms list sha256 does not match the registry", ref)
	}
	set, err := ParseSet(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	if set.Connector != name || set.Version != rel.Version || SetDigest(set) != rel.Digest {
		return nil, fmt.Errorf("%s: platforms list does not match the registry entry", ref)
	}
	plats := []string{platform}
	if platform == "" {
		plats = setPlatforms(set)
	}
	var first *Package
	for _, p := range plats {
		a, ok := set.Platforms[p]
		if !ok {
			return nil, fmt.Errorf("connector %s %s has no package for %s (only %s)", name, rel.Version, p, strings.Join(setPlatforms(set), ", "))
		}
		aref := ResolveRef(ref, a.URL)
		ab, err := Fetch(ctx, aref)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", aref, err)
		}
		pkg, err := CheckRelease(ab, name, publisher, registry.Release{Version: rel.Version, SHA256: a.SHA256, Digest: a.Digest, Capabilities: rel.Capabilities}, pub)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", aref, err)
		}
		if !pkg.Native() || pkg.Sig.Platform != p {
			return nil, fmt.Errorf("%s: not a native package for %s", aref, p)
		}
		if first == nil {
			first = pkg
		}
	}
	return first, nil
}

func setPlatforms(s PlatformSet) []string { return slices.Sorted(maps.Keys(s.Platforms)) }
