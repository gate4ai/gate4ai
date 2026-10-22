// SPDX-License-Identifier: AGPL-3.0-only

package keys

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// save пишет файл атомарно: новый временный файл 0600 в том же каталоге,
// fsync, rename. Права каталога (0700) и файла (0600) исправляются, даже если
// каталог или файл уже были с более широкими правами.
func (f *File) save(m map[string]string) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := tighten(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp, err := os.CreateTemp(dir, filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); runtime.GOOS == "windows" {
		err = nil
	}
	if err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

// tighten убирает у каталога права группы и остальных (кроме Windows, где
// права задаются иначе).
func tighten(path string, perm os.FileMode) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&^perm != 0 {
		return os.Chmod(path, fi.Mode().Perm()&perm)
	}
	return nil
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
