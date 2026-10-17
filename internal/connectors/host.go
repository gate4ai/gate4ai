// SPDX-License-Identifier: AGPL-3.0-only

// Package connectors — коннекторы на стороне хоста: реестр манифестов и вызовы
// execute/verify. Коннекторы из магазина — WASM-модули (пакет wasm), их
// установка — пакет store; внутренние коннекторы ядра (например, egress) —
// Go-код в том же процессе (Builtin). Манифест и типы запросов — контракт из
// github.com/gate4ai/sdk-go.
package connectors

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/manifest"
)

// Client — то, чем daemon пользуется для вызова коннектора.
type Client interface {
	Manifest() manifest.Manifest
	Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error)
	Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error)
	Close() error
}

// Connector — внутренний коннектор ядра на Go (и тестовые коннекторы).
type Connector interface {
	Manifest() manifest.Manifest
	Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error)
	Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error)
}

// InProcess — внутренний коннектор в том же процессе. Запросы и ответы
// проходят через JSON, как у WASM-модуля: результат — те же map/[]any/float64.
type InProcess struct {
	c   Connector
	man manifest.Manifest
}

// NewInProcess проверяет манифест и оборачивает коннектор.
func NewInProcess(c Connector) (*InProcess, error) {
	m := c.Manifest()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &InProcess{c: c, man: m}, nil
}

func roundtrip[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func (p *InProcess) Manifest() manifest.Manifest { return p.man }

func (p *InProcess) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	if _, ok := p.man.Find(req.Action); !ok {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown action %q", req.Action)
	}
	req, err := roundtrip(req)
	if err != nil {
		return abi.ExecResult{}, err
	}
	res, err := p.c.Execute(ctx, req)
	if err != nil {
		return abi.ExecResult{}, err
	}
	return roundtrip(res)
}

func (p *InProcess) Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error) {
	req, err := roundtrip(req)
	if err != nil {
		return abi.VerifyResult{}, err
	}
	res, err := p.c.Verify(ctx, req)
	if err != nil {
		return abi.VerifyResult{}, err
	}
	return roundtrip(res)
}

func (p *InProcess) Close() error { return nil }

// Registry — подключённые коннекторы.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Client
}

// NewRegistry — пустой реестр.
func NewRegistry() *Registry { return &Registry{m: map[string]Client{}} }

// Add регистрирует коннектор под именем из его манифеста.
func (r *Registry) Add(c Client) {
	r.mu.Lock()
	r.m[c.Manifest().Connector] = c
	r.mu.Unlock()
}

// Remove убирает коннектор из реестра и закрывает его.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	c := r.m[name]
	delete(r.m, name)
	r.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Each вызывает f для каждого коннектора (вне блокировки реестра).
func (r *Registry) Each(f func(Client)) {
	r.mu.RLock()
	list := make([]Client, 0, len(r.m))
	for _, c := range r.m {
		list = append(list, c)
	}
	r.mu.RUnlock()
	for _, c := range list {
		f(c)
	}
}

// Get возвращает коннектор.
func (r *Registry) Get(name string) (Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.m[name]
	return c, ok
}

// Manifests — манифесты всех коннекторов по имени.
func (r *Registry) Manifests() []manifest.Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]manifest.Manifest, 0, len(r.m))
	for _, c := range r.m {
		out = append(out, c.Manifest())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Connector < out[j].Connector })
	return out
}

// Lookup находит действие "connector.action".
func (r *Registry) Lookup(full string) (Client, manifest.Action, bool) {
	for i := 0; i < len(full); i++ {
		if full[i] == '.' {
			c, ok := r.Get(full[:i])
			if !ok {
				return nil, manifest.Action{}, false
			}
			m := c.Manifest()
			a, ok := m.Find(full[i+1:])
			return c, a, ok
		}
	}
	return nil, manifest.Action{}, false
}

// Catalog — аргументы всех действий для проверки условий политик.
func (r *Registry) Catalog() map[string][]string {
	out := map[string][]string{}
	for _, m := range r.Manifests() {
		for _, a := range m.Actions {
			out[m.Full(a)] = a.ArgNames()
		}
	}
	return out
}

// Close закрывает все коннекторы.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.m {
		_ = c.Close()
	}
}
