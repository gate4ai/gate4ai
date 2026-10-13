// SPDX-License-Identifier: FSL-1.1-ALv2

// Package keys — низкоуровневое хранилище ключей и секретов: системный keychain
// (macOS Keychain, Secret Service, Windows Credential Manager) или память в тестах.
package keys

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// Service — имя сервиса в keychain.
const Service = "gate4ai"

// ErrNotFound — ключа нет.
var ErrNotFound = errors.New("key not found")

// Store — хранилище строковых секретов по имени.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

// Keyring — системный keychain.
type Keyring struct{ Service string }

// NewKeyring — keychain с сервисом gate4ai.
func NewKeyring() *Keyring { return &Keyring{Service: Service} }

func (k *Keyring) Get(name string) (string, error) {
	v, err := keyring.Get(k.Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (k *Keyring) Set(name, value string) error { return keyring.Set(k.Service, name, value) }

func (k *Keyring) Delete(name string) error {
	err := keyring.Delete(k.Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// Memory — хранилище в памяти (тесты, режим --dev).
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemory создаёт пустое хранилище в памяти.
func NewMemory() *Memory { return &Memory{m: map[string]string{}} }

func (m *Memory) Get(name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.m[name]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (m *Memory) Set(name, value string) error {
	m.mu.Lock()
	m.m[name] = value
	m.mu.Unlock()
	return nil
}

func (m *Memory) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.m[name]; !ok {
		return ErrNotFound
	}
	delete(m.m, name)
	return nil
}

// Names возвращает имена ключей (для тестов и Web UI).
func (m *Memory) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.m))
	for k := range m.m {
		out = append(out, k)
	}
	return out
}

// RandomBytes возвращает (создавая при первом обращении) случайный ключ длины n.
func RandomBytes(s Store, name string, n int) ([]byte, error) {
	v, err := s.Get(name)
	if err == nil {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(b) != n {
			return nil, errors.New("key " + name + " is corrupted")
		}
		return b, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := s.Set(name, base64.StdEncoding.EncodeToString(b)); err != nil {
		return nil, err
	}
	return b, nil
}
