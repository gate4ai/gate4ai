// SPDX-License-Identifier: Apache-2.0

// Package registry — формат индекса registry коннекторов (index.json) и
// проверка его подписи (index.json.sig). Одни и те же правила у хоста
// gate4ai (магазин, `gate4ai registry check`) и у сайта gate4.ai.
//
// Подпись индекса — Ed25519 над hex(sha256(index.json)) в base64; ключи —
// "ed25519:<base64 публичного>".
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Уровни доверия издателя.
const (
	TrustVerified  = "verified"  // проверен gate4.ai: ревью кода и возможностей
	TrustPublisher = "publisher" // проверенный издатель
	TrustCommunity = "community" // сообщество: с предупреждением, по умолчанию только действия read
)

// TrustLevels — уровни доверия издателя в индексе, от высшего к низшему.
var TrustLevels = []string{TrustVerified, TrustPublisher, TrustCommunity}

// Index — индекс registry (index.json) с подписью index.json.sig.
type Index struct {
	Version int `json:"version"`
	// Updated — дата (YYYY-MM-DD) или время RFC 3339 публикации индекса: хост
	// не принимает индекс старше уже виденного (CheckNewer, защита от отката).
	Updated string `json:"updated,omitempty"`
	// Expires — срок годности индекса (YYYY-MM-DD или RFC 3339; пусто — без
	// срока): просроченный индекс хост не принимает (защита от заморозки).
	Expires    string               `json:"expires,omitempty"`
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
	Repo        string    `json:"repo"`           // исходник (обязателен: воспроизводимая сборка)
	Path        string    `json:"path,omitempty"` // каталог коннектора в repo (пусто — корень)
	License     string    `json:"license"`
	Description string    `json:"description,omitempty"`
	Versions    []Release `json:"versions"`
}

// Release — версия коннектора.
type Release struct {
	Version      string   `json:"version"`
	URL          string   `json:"url"`    // пакет .g4c или список платформ .platforms.json: URL или путь относительно индекса
	SHA256       string   `json:"sha256"` // sha256 файла по URL
	Digest       string   `json:"digest"` // дайджест пакета (подписан издателем)
	Capabilities []string `json:"capabilities"`
}

// Native — запись ссылается на список платформ нативного пакета (runtime: native).
func (r Release) Native() bool { return strings.HasSuffix(r.URL, ".platforms.json") }

// Revocation — отозванная версия ("*" — все версии).
type Revocation struct {
	Connector string `json:"connector"`
	Version   string `json:"version"`
	Reason    string `json:"reason"`
}

// ReservedNames — имена, которые коннектор в registry и магазине носить не
// может: каталоги и служебные имена хоста в каталоге коннекторов (data — KV
// всех коннекторов) и имена действий ядра (gate4ai.*, egress.*, secret.use).
var ReservedNames = []string{"data", "gate4ai", "egress", "secret"}

// Reserved — имя коннектора зарезервировано (ReservedNames).
func Reserved(name string) bool {
	for _, r := range ReservedNames {
		if r == name {
			return true
		}
	}
	return false
}

// ErrRollback — индекс старше уже виденного (откат) или просрочен.
var ErrRollback = errors.New("registry: index is older than the one seen before (rollback) or expired")

// parseWhen — дата YYYY-MM-DD или время RFC 3339.
func parseWhen(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// compareWhen сравнивает отметки времени индекса; пустая — самая старая.
func compareWhen(a, b string) int {
	ta, oka := parseWhen(a)
	tb, okb := parseWhen(b)
	switch {
	case a == b:
		return 0
	case a == "":
		return -1
	case b == "":
		return 1
	case oka && okb:
		return ta.Compare(tb)
	}
	return strings.Compare(a, b)
}

// CheckNewer — индекс ix можно принять вместо уже виденного prev (nil — не было):
// updated не старше, при той же отметке не пропал ни один отзыв, срок
// expires не истёк к now. Иначе ErrRollback.
func (ix *Index) CheckNewer(prev *Index, now time.Time) error {
	if ix.Expires != "" {
		t, ok := parseWhen(ix.Expires)
		if !ok {
			return fmt.Errorf("%w: bad expires %q", ErrRollback, ix.Expires)
		}
		if len(ix.Expires) == len("2006-01-02") {
			t = t.AddDate(0, 0, 1) // дата — до конца дня
		}
		if now.After(t) {
			return fmt.Errorf("%w: expired %s", ErrRollback, ix.Expires)
		}
	}
	if prev == nil {
		return nil
	}
	switch c := compareWhen(ix.Updated, prev.Updated); {
	case c < 0:
		return fmt.Errorf("%w: updated %q, seen %q", ErrRollback, ix.Updated, prev.Updated)
	case c == 0:
		for _, r := range prev.Revoked {
			if !slices.Contains(ix.Revoked, r) {
				return fmt.Errorf("%w: revocation of %s %s is missing in an index with the same updated %q", ErrRollback, r.Connector, r.Version, ix.Updated)
			}
		}
	}
	return nil
}

// Ошибки ParseIndex.
var (
	ErrNoKeys       = errors.New("registry: no registry keys configured")
	ErrBadSignature = errors.New("registry: index signature is not valid")
)

// ParseIndex проверяет подпись индекса ключами registry, затем разбирает его
// (неизвестные поля — ошибка) и проверяет структуру (Check).
func ParseIndex(raw []byte, sig string, keys []string) (*Index, error) {
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}
	if !VerifyIndex(raw, sig, keys) {
		return nil, ErrBadSignature
	}
	var ix Index
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ix); err != nil {
		return nil, fmt.Errorf("registry index: %w", err)
	}
	if err := ix.Check(); err != nil {
		return nil, err
	}
	return &ix, nil
}

// VerifyIndex — подпись sig (base64, пробелы по краям не важны) верна для
// индекса raw хотя бы одним из ключей.
func VerifyIndex(raw []byte, sig string, keys []string) bool {
	_, ok := VerifySig(keys, Sum(raw), strings.TrimSpace(sig))
	return ok
}

// SignIndex — подпись индекса приватным ключом registry ("ed25519-private:<base64 seed>").
func SignIndex(raw []byte, key string) (string, error) {
	priv, err := ParsePrivateKey(key)
	if err != nil {
		return "", err
	}
	return Sign(priv, Sum(raw)), nil
}

// Check проверяет индекс: версию формата, издателей и их ключи, ссылки на
// издателей, обязательные поля версий.
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
		if Reserved(name) {
			errs = append(errs, fmt.Sprintf("connector %s: the name is reserved", name))
		}
		if _, ok := ix.Publishers[e.Publisher]; !ok {
			errs = append(errs, fmt.Sprintf("connector %s: unknown publisher %q", name, e.Publisher))
		}
		if !strings.HasPrefix(e.Repo, "https://") {
			errs = append(errs, fmt.Sprintf("connector %s: repo with the source code is required", name))
		}
		if p := e.Path; p != "" && (strings.HasPrefix(p, "/") || strings.Contains("/"+p+"/", "/../")) {
			errs = append(errs, fmt.Sprintf("connector %s: path %q must be relative inside the repo", name, p))
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

// Trust — уровень доверия издателя коннектора ("" — коннектора или издателя нет).
func (ix *Index) Trust(name string) string {
	return ix.Publishers[ix.Connectors[name].Publisher].Trust
}

// IsRevoked — версия отозвана; второе значение — причина.
func (ix *Index) IsRevoked(name, version string) (bool, string) {
	for _, r := range ix.Revoked {
		if r.Connector == name && (r.Version == "*" || r.Version == version) {
			return true, r.Reason
		}
	}
	return false, ""
}

// CompareVersions сравнивает версии по числовым компонентам (1.10 > 1.9).
func CompareVersions(a, b string) int {
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

// Latest — последняя неотозванная версия коннектора.
func (ix *Index) Latest(name string) (Release, bool) {
	var best *Release
	e := ix.Connectors[name]
	for i := range e.Versions {
		r := &e.Versions[i]
		if rev, _ := ix.IsRevoked(name, r.Version); rev {
			continue
		}
		if best == nil || CompareVersions(r.Version, best.Version) > 0 {
			best = r
		}
	}
	if best == nil {
		return Release{}, false
	}
	return *best, true
}

// Resolve — версия коннектора ("" — последняя неотозванная). Отозванная версия — ошибка.
func (ix *Index) Resolve(name, version string) (Entry, Release, error) {
	e, ok := ix.Connectors[name]
	if !ok {
		return e, Release{}, fmt.Errorf("connector %q is not in the registry", name)
	}
	var rel Release
	found := false
	if version == "" {
		rel, found = ix.Latest(name)
	} else {
		for _, r := range e.Versions {
			if r.Version == version {
				rel, found = r, true
				break
			}
		}
	}
	if !found {
		return e, Release{}, fmt.Errorf("connector %s: version %q not found", name, version)
	}
	if rev, why := ix.IsRevoked(name, rel.Version); rev {
		return e, Release{}, fmt.Errorf("connector %s %s is revoked: %s", name, rel.Version, why)
	}
	return e, rel, nil
}
