// SPDX-License-Identifier: AGPL-3.0-only

package keys

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// File — запасное хранилище в файле с правами 0600 (headless-системы без
// Secret Service, режим разработки). Слабее keychain: доступно любому процессу
// пользователя; daemon предупреждает об этом при запуске.
type File struct {
	path string
	mu   sync.Mutex
}

// NewFile — хранилище в файле path.
func NewFile(path string) *File { return &File{path: path} }

func (f *File) load() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(b, &m)
}

func (f *File) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *File) Get(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *File) Set(name, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[name] = value
	return f.save(m)
}

func (f *File) Delete(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return ErrNotFound
	}
	delete(m, name)
	return f.save(m)
}

// Probe проверяет, что keychain доступен (запись и чтение пробного ключа).
func Probe(s Store) error {
	if err := s.Set("probe", "1"); err != nil {
		return err
	}
	_, err := s.Get("probe")
	return err
}
