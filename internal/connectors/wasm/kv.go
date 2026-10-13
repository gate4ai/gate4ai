// SPDX-License-Identifier: FSL-1.1-ALv2

package wasm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/gate4ai/sdk-go/abi"
)

// FileKV — хранилище коннектора в JSON-файле (kv_*): у каждого коннектора свой
// файл, квота — capabilities.storage.quota_mb.
type FileKV struct {
	Path string
	mu   sync.Mutex
}

// KVDir — фабрика хранилищ: <dir>/<коннектор>/kv.json.
func KVDir(dir string) func(string) *FileKV {
	var mu sync.Mutex
	m := map[string]*FileKV{}
	return func(name string) *FileKV {
		mu.Lock()
		defer mu.Unlock()
		if m[name] == nil {
			m[name] = &FileKV{Path: filepath.Join(dir, name, "kv.json")}
		}
		return m[name]
	}
}

func (k *FileKV) load() (map[string][]byte, error) {
	m := map[string][]byte{}
	b, err := os.ReadFile(k.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (k *FileKV) save(m map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(k.Path), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	tmp := k.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, k.Path)
}

func (k *FileKV) Get(key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return nil, false, err
	}
	v, ok := m[key]
	return v, ok, nil
}

func (k *FileKV) Set(key string, v []byte, quota int64) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	m[key] = v
	var total int64
	for kk, vv := range m {
		total += int64(len(kk) + len(vv))
	}
	if total > quota {
		return abi.Errorf(abi.CodeLimit, "storage quota of %d bytes exceeded", quota)
	}
	return k.save(m)
}

func (k *FileKV) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	m, err := k.load()
	if err != nil {
		return err
	}
	delete(m, key)
	return k.save(m)
}
