// SPDX-License-Identifier: AGPL-3.0-only

// Package agentkeys — каталог ключей агентов этого компьютера (gate4ai pair,
// автоподключение из Web UI): по файлу JSON на агента, права 0600. Ключи
// пишет и читает CLI агента и Web UI владельца (один пользователь ОС);
// daemon хранит только их хэши.
package agentkeys

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Key — ключ агента в каталоге ключей.
type Key struct {
	Agent string `json:"agent"`          // имя агента в gate4ai
	Kind  string `json:"kind,omitempty"` // тип агента: claude-code, codex…
	Key   string `json:"key"`
	Dir   string `json:"dir,omitempty"` // ключ только для агента, запущенного в этом каталоге
	URL   string `json:"url,omitempty"` // daemon на другом компьютере (GATE4AI_URL при спаривании)
	// Pairing и Code — запрос на подключение, ещё без решения владельца.
	Pairing string    `json:"pairing,omitempty"`
	Code    string    `json:"code,omitempty"`
	Created time.Time `json:"created"`
}

var nameRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// Name — имя агента из произвольной строки (каталога, типа).
func Name(s string) string {
	s = strings.Trim(nameRe.ReplaceAllString(strings.ToLower(s), "-"), "-._")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-._")
	}
	return s
}

// Path — файл ключа агента.
func Path(dir, agent string) string { return filepath.Join(dir, Name(agent)+".json") }

// Load — ключи из каталога dir.
func Load(dir string) []Key {
	if dir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []Key
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var k Key
		if json.Unmarshal(b, &k) == nil && k.Agent != "" && k.Key != "" {
			out = append(out, k)
		}
	}
	return out
}

// Save — записать ключ (атомарно, 0600).
func Save(dir string, k Key) error {
	if dir == "" {
		return errors.New("no directory for agent keys")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if k.Created.IsZero() {
		k.Created = time.Now().UTC()
	}
	b, _ := json.MarshalIndent(k, "", "  ")
	p := Path(dir, k.Agent)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Remove — удалить ключ агента.
func Remove(dir, agent string) { _ = os.Remove(Path(dir, agent)) }

// Pick — ключ для вызывающего: по имени агента; иначе по типу — сначала
// ключ, привязанный к каталогу агента (самый глубокий), затем общий ключ типа.
func Pick(keys []Key, name, kind, dir string) (Key, bool) {
	if name != "" {
		for _, k := range keys {
			if k.Agent == name {
				return k, true
			}
		}
		return Key{}, false
	}
	if kind == "" {
		return Key{}, false
	}
	var bound, shared []Key
	for _, k := range keys {
		switch {
		case k.Kind != kind:
		case k.Dir != "":
			if WithinDir(dir, k.Dir) {
				bound = append(bound, k)
			}
		default:
			shared = append(shared, k)
		}
	}
	if len(bound) > 0 {
		sort.Slice(bound, func(i, j int) bool { return len(bound[i].Dir) > len(bound[j].Dir) })
		return bound[0], true
	}
	if len(shared) == 0 {
		return Key{}, false
	}
	sort.Slice(shared, func(i, j int) bool {
		if (shared[i].Agent == kind) != (shared[j].Agent == kind) {
			return shared[i].Agent == kind
		}
		return shared[i].Created.After(shared[j].Created)
	})
	return shared[0], true
}

// WithinDir — каталог dir совпадает с root или лежит внутри него.
func WithinDir(dir, root string) bool {
	if dir == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
