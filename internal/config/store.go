// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// Version — метаданные версии конфигурации.
type Version struct {
	N       int       `json:"n"`
	At      time.Time `json:"at"`
	Author  string    `json:"author"`
	Comment string    `json:"comment,omitempty"`
	Parent  int       `json:"parent"`
}

// Store — Config Store: каждая запись — новая версия, откат создаёт новую версию
// со старым содержимым. Изменения доступны только владельцу.
type Store struct {
	dir   string
	extra []ExtraValidator

	mu   sync.RWMutex
	cur  *Config
	raw  []byte
	n    int
	subs []func(*Config)
	now  func() time.Time

	// editMu — Edit целиком (чтение, правка, запись): параллельные правки не
	// теряют друг друга.
	editMu sync.Mutex
	// notifyMu — подписчики вызываются по одному и по возрастанию версий:
	// applied — последняя версия, отданная подписчикам.
	notifyMu sync.Mutex
	applied  int
}

// ErrConflict — версия изменилась с тех пор, как её прочитали (PutIf).
var ErrConflict = errors.New("the policy was changed meanwhile: reload it and apply your edit again")

// ErrNoVersion — версия не найдена.
var ErrNoVersion = errors.New("config version not found")

// OpenStore открывает хранилище в dir. Если версий нет и initial != nil,
// initial становится версией 1.
func OpenStore(dir string, initial []byte, extra ...ExtraValidator) (*Store, error) {
	s := &Store{dir: dir, extra: extra, now: time.Now}
	if err := os.MkdirAll(filepath.Join(dir, "versions"), 0o700); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "current"))
	switch {
	case err == nil:
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return nil, fmt.Errorf("config store: bad current pointer: %w", err)
		}
		raw, err := os.ReadFile(s.versionPath(n))
		if err != nil {
			return nil, err
		}
		c, err := Parse(raw, extra...)
		if err != nil {
			return nil, fmt.Errorf("config store: version %d: %w", n, err)
		}
		s.cur, s.raw, s.n = c, raw, n
	case errors.Is(err, os.ErrNotExist):
		if initial == nil {
			return nil, fmt.Errorf("config store %s is empty: pass a config file", dir)
		}
		if _, err := s.put("init", "initial", initial, 0); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return s, nil
}

func (s *Store) versionPath(n int) string {
	return filepath.Join(s.dir, "versions", fmt.Sprintf("%06d.yaml", n))
}

// Current возвращает текущую конфигурацию (не изменять!).
func (s *Store) Current() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// CurrentRaw — YAML текущей версии и её номер.
func (s *Store) CurrentRaw() ([]byte, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.raw...), s.n
}

// Subscribe вызывает f при каждой смене версии.
func (s *Store) Subscribe(f func(*Config)) {
	s.mu.Lock()
	s.subs = append(s.subs, f)
	s.mu.Unlock()
}

// Validate проверяет YAML, ничего не сохраняя.
func (s *Store) Validate(raw []byte) (*Config, error) { return Parse(raw, s.extra...) }

// Put сохраняет новую версию. Только владелец.
func (s *Store) Put(p auth.Principal, raw []byte, comment string) (Version, error) {
	return s.PutIf(p, raw, comment, 0)
}

// PutIf — Put, если текущая версия всё ещё base (base 0 — без проверки):
// иначе ErrConflict, и правка, сделанная по старой версии, не затирает новую.
func (s *Store) PutIf(p auth.Principal, raw []byte, comment string, base int) (Version, error) {
	if err := p.RequireOwner(); err != nil {
		return Version{}, err
	}
	return s.put("owner:"+p.Via, comment, raw, base)
}

// Rollback делает версию n текущей, записывая её копию новой версией. Только владелец.
func (s *Store) Rollback(p auth.Principal, n int) (Version, error) {
	if err := p.RequireOwner(); err != nil {
		return Version{}, err
	}
	raw, err := os.ReadFile(s.versionPath(n))
	if errors.Is(err, os.ErrNotExist) {
		return Version{}, ErrNoVersion
	} else if err != nil {
		return Version{}, err
	}
	return s.put("owner:"+p.Via, fmt.Sprintf("rollback to %d", n), raw, 0)
}

// Get возвращает YAML версии n.
func (s *Store) Get(n int) ([]byte, error) {
	raw, err := os.ReadFile(s.versionPath(n))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoVersion
	}
	return raw, err
}

// Versions — список версий, новые первыми.
func (s *Store) Versions() ([]Version, error) {
	ents, err := os.ReadDir(filepath.Join(s.dir, "versions"))
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "versions", e.Name()))
		if err != nil {
			return nil, err
		}
		var v Version
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N > out[j].N })
	return out, nil
}

// put — новая версия поверх текущей; base != 0 — только если текущая — base.
func (s *Store) put(author, comment string, raw []byte, base int) (Version, error) {
	c, err := Parse(raw, s.extra...)
	if err != nil {
		return Version{}, err
	}
	s.mu.Lock()
	if base != 0 && base != s.n {
		s.mu.Unlock()
		return Version{}, ErrConflict
	}
	parent := s.n
	n := s.n + 1
	v := Version{N: n, At: s.now().UTC(), Author: author, Comment: comment, Parent: parent}
	meta, _ := json.MarshalIndent(v, "", "  ")
	if err := writeAtomic(s.versionPath(n), raw); err != nil {
		s.mu.Unlock()
		return Version{}, err
	}
	if err := writeAtomic(strings.TrimSuffix(s.versionPath(n), ".yaml")+".meta.json", meta); err != nil {
		s.mu.Unlock()
		return Version{}, err
	}
	if err := writeAtomic(filepath.Join(s.dir, "current"), []byte(strconv.Itoa(n)+"\n")); err != nil {
		s.mu.Unlock()
		return Version{}, err
	}
	s.cur, s.raw, s.n = c, append([]byte(nil), raw...), n
	s.mu.Unlock()
	s.notify(false)
	return v, nil
}

// notify отдаёт подписчикам текущую версию: по одному и не назад — если два
// put закончились в обратном порядке, старая версия подписчикам не попадёт.
// force — отдать текущую и без смены версии (сменился каталог коннекторов).
func (s *Store) notify(force bool) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.mu.RLock()
	c, n := s.cur, s.n
	subs := append([]func(*Config){}, s.subs...)
	s.mu.RUnlock()
	if !force && n <= s.applied {
		return
	}
	s.applied = n
	for _, f := range subs {
		f(c)
	}
}

// Reapply вызывает подписчиков с текущей версией (после установки или
// удаления коннектора), упорядоченно с обычными сменами версий.
func (s *Store) Reapply() { s.notify(true) }

func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	// Данные — на диск до переименования: после сбоя питания current не
	// укажет на пустую или усечённую версию.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir — запись каталога (переименование) на диск; на Windows каталог так
// не открыть — там ошибка не важна.
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		f.Close()
	}
}
