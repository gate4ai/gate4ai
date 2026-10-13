// SPDX-License-Identifier: FSL-1.1-ALv2

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Уровни доверия.
const (
	TrustVerified  = "verified"  // проверен gate4.ai: ревью кода и возможностей
	TrustPublisher = "publisher" // проверенный издатель
	TrustCommunity = "community" // сообщество: с предупреждением, по умолчанию только действия read
	TrustLocal     = "local"     // пакет владельца из файла (--dev)
)

// Index — индекс registry (index.json) с подписью index.json.sig.
type Index struct {
	Version    int                  `json:"version"`
	Updated    string               `json:"updated,omitempty"`
	Publishers map[string]Publisher `json:"publishers"`
	Connectors map[string]Entry     `json:"connectors"`
	Revoked    []Revocation         `json:"revoked,omitempty"`
}

// Publisher — издатель и его ключи подписи пакетов.
type Publisher struct {
	Keys  []string `json:"keys"`
	Trust string   `json:"trust"` // verified | publisher | community
}

// Entry — коннектор в индексе.
type Entry struct {
	Publisher   string    `json:"publisher"`
	Repo        string    `json:"repo"` // исходник (обязателен: воспроизводимая сборка)
	License     string    `json:"license"`
	Description string    `json:"description,omitempty"`
	Versions    []Release `json:"versions"`
}

// Release — версия коннектора.
type Release struct {
	Version      string   `json:"version"`
	URL          string   `json:"url"`    // пакет .g4c: URL или путь относительно индекса
	SHA256       string   `json:"sha256"` // sha256 файла пакета
	Digest       string   `json:"digest"` // дайджест пакета (подписан издателем)
	Capabilities []string `json:"capabilities"`
}

// Revocation — отозванная версия ("*" — все версии).
type Revocation struct {
	Connector string `json:"connector"`
	Version   string `json:"version"`
	Reason    string `json:"reason"`
}

// Check проверяет индекс: структуру, ключи, ссылки на издателей.
func (ix *Index) Check() error {
	if ix.Version != 1 {
		return fmt.Errorf("index: unsupported version %d", ix.Version)
	}
	var errs []string
	for name, p := range ix.Publishers {
		switch p.Trust {
		case TrustVerified, TrustPublisher, TrustCommunity:
		default:
			errs = append(errs, fmt.Sprintf("publisher %s: bad trust %q", name, p.Trust))
		}
		if len(p.Keys) == 0 {
			errs = append(errs, fmt.Sprintf("publisher %s: no keys", name))
		}
		for _, k := range p.Keys {
			if _, err := ParsePublicKey(k); err != nil {
				errs = append(errs, fmt.Sprintf("publisher %s: %v", name, err))
			}
		}
	}
	for name, e := range ix.Connectors {
		if _, ok := ix.Publishers[e.Publisher]; !ok {
			errs = append(errs, fmt.Sprintf("connector %s: unknown publisher %q", name, e.Publisher))
		}
		if !strings.HasPrefix(e.Repo, "https://") {
			errs = append(errs, fmt.Sprintf("connector %s: repo with the source code is required", name))
		}
		if e.License == "" {
			errs = append(errs, fmt.Sprintf("connector %s: license is required", name))
		}
		seen := map[string]bool{}
		for _, r := range e.Versions {
			if r.Version == "" || seen[r.Version] {
				errs = append(errs, fmt.Sprintf("connector %s: empty or duplicate version %q", name, r.Version))
			}
			seen[r.Version] = true
			if len(r.SHA256) != 64 || len(r.Digest) != 64 || r.URL == "" {
				errs = append(errs, fmt.Sprintf("connector %s %s: url, sha256 and digest are required", name, r.Version))
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return errors.New("index: " + strings.Join(errs, "; "))
	}
	return nil
}

// IsRevoked — версия отозвана.
func (ix *Index) IsRevoked(name, version string) (bool, string) {
	for _, r := range ix.Revoked {
		if r.Connector == name && (r.Version == "*" || r.Version == version) {
			return true, r.Reason
		}
	}
	return false, ""
}

// cmpVersion сравнивает версии по числовым компонентам (1.10 > 1.9).
func cmpVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}

// Resolve — версия коннектора ("" — последняя неотозванная).
func (ix *Index) Resolve(name, version string) (Entry, Release, error) {
	e, ok := ix.Connectors[name]
	if !ok {
		return e, Release{}, fmt.Errorf("connector %q is not in the registry", name)
	}
	var best *Release
	for i := range e.Versions {
		r := &e.Versions[i]
		if version != "" {
			if r.Version == version {
				best = r
				break
			}
			continue
		}
		if rev, _ := ix.IsRevoked(name, r.Version); rev {
			continue
		}
		if best == nil || cmpVersion(r.Version, best.Version) > 0 {
			best = r
		}
	}
	if best == nil {
		return e, Release{}, fmt.Errorf("connector %s: version %q not found", name, version)
	}
	if rev, why := ix.IsRevoked(name, best.Version); rev {
		return e, Release{}, fmt.Errorf("connector %s %s is revoked: %s", name, best.Version, why)
	}
	return e, *best, nil
}

// fetch читает путь или http(s)-URL.
func fetch(ctx context.Context, src string) ([]byte, error) {
	if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", src, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("GET %s: %s", src, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, maxPackage+1))
	}
	return os.ReadFile(src)
}

// resolveRef — ссылка относительно индекса.
func resolveRef(base, ref string) string {
	if strings.Contains(ref, "://") || filepath.IsAbs(ref) {
		return ref
	}
	if u, err := url.Parse(base); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		r, _ := url.Parse(ref)
		return u.ResolveReference(r).String()
	}
	return filepath.Join(filepath.Dir(base), ref)
}

// IndexData — индекс и подпись: данные для подписи — сам файл index.json.
func IndexData(ctx context.Context, src string) (raw []byte, sig string, err error) {
	if raw, err = fetch(ctx, src); err != nil {
		return nil, "", fmt.Errorf("registry index: %w", err)
	}
	s, err := fetch(ctx, src+".sig")
	if err != nil {
		return nil, "", fmt.Errorf("registry index signature: %w", err)
	}
	return raw, strings.TrimSpace(string(s)), nil
}

// LoadIndex загружает индекс и проверяет подпись ключами registry.
func LoadIndex(ctx context.Context, src string, keys []string) (*Index, []byte, error) {
	raw, sig, err := IndexData(ctx, src)
	if err != nil {
		return nil, nil, err
	}
	ix, err := ParseIndex(raw, sig, keys)
	return ix, raw, err
}

// ParseIndex разбирает индекс и проверяет его подпись.
func ParseIndex(raw []byte, sig string, keys []string) (*Index, error) {
	if len(keys) == 0 {
		return nil, errors.New("registry: no registry keys configured (store.registry_keys)")
	}
	if _, ok := VerifySig(keys, sum(raw), sig); !ok {
		return nil, errors.New("registry: index signature is not valid for store.registry_keys")
	}
	var ix Index
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ix); err != nil {
		return nil, fmt.Errorf("registry index: %w", err)
	}
	return &ix, ix.Check()
}

// SignIndex — подпись индекса ключом registry (base64 над sha256 файла).
func SignIndex(raw []byte, key string) (string, error) {
	priv, err := ParsePrivateKey(key)
	if err != nil {
		return "", err
	}
	return Sign(priv, sum(raw)), nil
}
