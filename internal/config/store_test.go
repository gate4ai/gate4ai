// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

func cfgWithAgent(name string) []byte {
	return []byte(fmt.Sprintf("agents: {%s: {profile: p}}\nprofiles: {p: {connectors: []}}\n", name))
}

// Подписчики не получают старую версию после новой, даже если put
// закончились в обратном порядке (E.7).
func TestStoreNotifyInOrder(t *testing.T) {
	s, err := OpenStore(t.TempDir(), cfgWithAgent("a1"))
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.OwnerVia("socket")
	release := make(chan struct{})
	var mu sync.Mutex
	var last *Config
	first := true
	s.Subscribe(func(c *Config) {
		mu.Lock()
		block := first
		first = false
		mu.Unlock()
		if block {
			<-release // подписчик версии 2 «медленный»
		}
		mu.Lock()
		last = c
		mu.Unlock()
	})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = s.Put(owner, cfgWithAgent("a2"), "v2") }()
	for _, ok := s.Current().Agents["a2"]; !ok; _, ok = s.Current().Agents["a2"] {
		time.Sleep(time.Millisecond)
	}
	go func() { defer wg.Done(); _, _ = s.Put(owner, cfgWithAgent("a3"), "v3") }()
	for _, ok := s.Current().Agents["a3"]; !ok; _, ok = s.Current().Agents["a3"] {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if _, ok := last.Agents["a3"]; !ok {
		t.Fatalf("subscribers ended on an old version: %v", last.Agents)
	}
	// Reapply отдаёт текущую версию и без её смены.
	last = nil
	s.Reapply()
	if last == nil || last != s.Current() {
		t.Fatal("Reapply did not notify with the current version")
	}
}

// Параллельные Edit не теряют правок; PutIf по старой версии — конфликт (E.8).
func TestStoreEditNoLostUpdates(t *testing.T) {
	s, err := OpenStore(t.TempDir(), []byte("profiles:\n  default: { connectors: [] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.OwnerVia("socket")
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Edit(owner, "add", func(c *Config) error {
				return c.SetList("l", append(c.Lists["l"], fmt.Sprintf("x%d", i)))
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(s.Current().Lists["l"]); n != 10 {
		t.Fatalf("lost updates: %d of 10 items", n)
	}
	_, base := s.CurrentRaw()
	raw, _ := s.CurrentRaw()
	if _, err := s.PutIf(owner, raw, "first", base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutIf(owner, raw, "stale", base); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base: %v", err)
	}
}

// Запись версии атомарна: временных файлов не остаётся (E.16 — fsync до rename).
func TestStoreWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir, cfgWithAgent("a1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(auth.OwnerVia("socket"), cfgWithAgent("a2"), "v2"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(dir, "versions")} {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if len(e.Name()) > 4 && e.Name()[:5] == ".tmp-" {
				t.Fatalf("temp file left: %s", e.Name())
			}
		}
	}
}
