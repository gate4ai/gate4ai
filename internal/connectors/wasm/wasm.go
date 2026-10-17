// SPDX-License-Identifier: AGPL-3.0-only

// Package wasm — среда исполнения коннекторов runtime: wasm. Модуль работает
// в wazero внутри daemon: у него нет файлов, сети, процессов и окружения
// хоста — только функции хоста ABI v1 (github.com/gate4ai/sdk-go/abi), каждая
// из которых проверяется по возможностям манифеста. Один экземпляр модуля —
// один вызов; память и время ограничены limits манифеста.
package wasm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/capability"
	"github.com/gate4ai/sdk-go/hostfuncs"
	"github.com/gate4ai/sdk-go/manifest"
)

// Services — службы хоста, которые получают коннекторы через функции хоста.
type Services struct {
	Secrets hostfuncs.Secrets
	// KV — хранилище коннектора по имени (nil — хранилища нет).
	KV func(connector string) hostfuncs.KV
	// Router — вызовы других коннекторов (service_call); nil — не поддерживаются.
	Router Router
	Log    *slog.Logger
	// Home — домашний каталог пользователя для "~" в путях.
	Home string
	// Dial и RootCAs — сеть (подменяются в тестах).
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	RootCAs *x509.CertPool
}

// Router проводит вызов service_call коннектора caller через политики хоста.
type Router interface {
	ServiceCall(ctx context.Context, caller string, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error)
}

// callerRouter — Router для одного вызывающего коннектора (hostfuncs.ServiceRouter).
type callerRouter struct {
	caller string
	r      Router
}

func (c callerRouter) ServiceCall(ctx context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	return c.r.ServiceCall(ctx, c.caller, rule, r)
}

// Engine — общая для коннекторов часть: кэш компиляции и службы хоста.
type Engine struct {
	svc   Services
	cache wazero.CompilationCache
}

// NewEngine — cacheDir "" — кэш компиляции только в памяти.
func NewEngine(cacheDir string, svc Services) (*Engine, error) {
	if svc.Log == nil {
		svc.Log = slog.Default()
	}
	cache := wazero.NewCompilationCache()
	if cacheDir != "" {
		c, err := wazero.NewCompilationCacheWithDir(cacheDir)
		if err != nil {
			return nil, err
		}
		cache = c
	}
	return &Engine{svc: svc, cache: cache}, nil
}

// Close освобождает кэш.
func (e *Engine) Close() error { return e.cache.Close(context.Background()) }

// Connector — загруженный WASM-коннектор (connectors.Client).
type Connector struct {
	e        *Engine
	man      manifest.Manifest
	limits   manifest.Limits
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	mu       sync.Mutex
	closed   bool
}

// callKey — состояние вызова в контексте функций хоста.
type callKey struct{}

type call struct {
	d       *hostfuncs.Dispatcher
	pending []byte
}

// Load компилирует модуль и сверяет его манифест с манифестом пакета.
func (e *Engine) Load(ctx context.Context, wasmBytes []byte, pkg manifest.Manifest) (*Connector, error) {
	if err := pkg.Validate(); err != nil {
		return nil, err
	}
	if pkg.Runtime != manifest.RuntimeWasm {
		return nil, fmt.Errorf("connector %s: runtime %q is not wasm", pkg.Connector, pkg.Runtime)
	}
	lim := pkg.Limits.WithDefaults()
	cfg := wazero.NewRuntimeConfig().WithCompilationCache(e.cache).
		WithMemoryLimitPages(uint32(lim.MemoryMB) * 16). // страница — 64 КиБ
		WithCloseOnContextDone(true)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	c := &Connector{e: e, man: pkg, limits: lim, rt: rt}
	if err := c.init(ctx, wasmBytes); err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("connector %s: %w", pkg.Connector, err)
	}
	return c, nil
}

func (c *Connector) init(ctx context.Context, wasmBytes []byte) error {
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, c.rt); err != nil {
		return err
	}
	_, err := c.rt.NewHostModuleBuilder(abi.ImportModule).
		NewFunctionBuilder().WithFunc(hostCall).Export(abi.ImportCall).
		NewFunctionBuilder().WithFunc(hostResult).Export(abi.ImportResult).
		Instantiate(ctx)
	if err != nil {
		return err
	}
	if c.compiled, err = c.rt.CompileModule(ctx, wasmBytes); err != nil {
		return fmt.Errorf("compile: %w", err)
	}
	for name, def := range c.compiled.ImportedFunctions() {
		mod, fn, _ := def.Import()
		if mod != abi.ImportModule && mod != wasi_snapshot_preview1.ModuleName {
			return fmt.Errorf("module imports %s.%s (%d): only %s and WASI are available", mod, fn, name, abi.ImportModule)
		}
	}
	for _, exp := range []string{abi.ExportAlloc, abi.ExportManifest, abi.ExportExecute, abi.ExportVerify} {
		if _, ok := c.compiled.ExportedFunctions()[exp]; !ok {
			return fmt.Errorf("module does not export %s", exp)
		}
	}
	var got manifest.Manifest
	if err := c.invoke(ctx, abi.ExportManifest, nil, nil, &got); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if !manifest.Same(got, c.man) {
		return errors.New("module manifest differs from the package manifest")
	}
	return nil
}

func hostCall(ctx context.Context, m api.Module, fnPtr, fnLen, reqPtr, reqLen uint32) int32 {
	cl, _ := ctx.Value(callKey{}).(*call)
	if cl == nil {
		return 0
	}
	fn, ok1 := m.Memory().Read(fnPtr, fnLen)
	req, ok2 := m.Memory().Read(reqPtr, reqLen)
	switch {
	case cl.d == nil:
		cl.pending = abi.Fail(abi.Errorf(abi.CodeNotSupported, "host functions are not available during %s", "manifest"))
	case !ok1 || !ok2:
		cl.pending = abi.Fail(abi.Errorf(abi.CodeBadRequest, "pointer out of module memory"))
	default:
		cl.pending = cl.d.Call(string(fn), bytes.Clone(req))
	}
	return int32(len(cl.pending))
}

func hostResult(ctx context.Context, m api.Module, dst uint32) {
	cl, _ := ctx.Value(callKey{}).(*call)
	if cl == nil {
		return
	}
	m.Memory().Write(dst, cl.pending)
	cl.pending = nil
}

// limitedBuffer — stderr модуля для сообщений об ошибках (паника Go и т. п.).
type limitedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := 4096 - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// invoke — новый экземпляр модуля, один экспорт, ответ в out.
func (c *Connector) invoke(ctx context.Context, export string, d *hostfuncs.Dispatcher, in []byte, out any) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("connector closed")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.limits.TimeoutS)*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, callKey{}, &call{d: d})
	stderr := &limitedBuffer{}
	cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize").
		WithSysWalltime().WithSysNanotime().WithSysNanosleep().WithRandSource(rand.Reader).
		WithStderr(stderr)
	mod, err := c.rt.InstantiateModule(ctx, c.compiled, cfg)
	if err != nil {
		return c.trap(ctx, err, stderr)
	}
	defer mod.Close(context.Background())
	args := []uint64{}
	if export != abi.ExportManifest {
		res, err := mod.ExportedFunction(abi.ExportAlloc).Call(ctx, uint64(len(in)))
		if err != nil {
			return c.trap(ctx, err, stderr)
		}
		p := uint32(res[0])
		if !mod.Memory().Write(p, in) {
			return errors.New("alloc returned a pointer out of memory")
		}
		args = []uint64{uint64(p), uint64(len(in))}
	}
	res, err := mod.ExportedFunction(export).Call(ctx, args...)
	if err != nil {
		return c.trap(ctx, err, stderr)
	}
	b, ok := mod.Memory().Read(uint32(res[0]>>32), uint32(res[0]))
	if !ok {
		return errors.New("module returned a response out of memory")
	}
	return abi.Decode(bytes.Clone(b), out)
}

func (c *Connector) trap(ctx context.Context, err error, stderr *limitedBuffer) error {
	if ctx.Err() == context.DeadlineExceeded {
		return abi.Errorf(abi.CodeLimit, "connector %s: time limit %ds exceeded", c.man.Connector, c.limits.TimeoutS)
	}
	msg := err.Error()
	if s := stderr.String(); s != "" {
		msg += ": " + s
	}
	return abi.Errorf(abi.CodeConnector, "connector %s crashed: %s", c.man.Connector, msg)
}

func (c *Connector) dispatcher(ctx context.Context, settings map[string]any) (*hostfuncs.Dispatcher, error) {
	return c.e.Dispatcher(ctx, c.man, c.limits, settings)
}

// Dispatcher — функции хоста для одного вызова коннектора m (WASM, встроенного
// или нативного): возможности манифеста с настройками экземпляра и службы хоста.
func (e *Engine) Dispatcher(ctx context.Context, m manifest.Manifest, lim manifest.Limits, settings map[string]any) (*hostfuncs.Dispatcher, error) {
	chk, err := capability.New(m, settings, e.svc.Home)
	if err != nil {
		return nil, err
	}
	var kv hostfuncs.KV
	if e.svc.KV != nil {
		kv = e.svc.KV(m.Connector)
	}
	name := m.Connector
	log := e.svc.Log
	var router hostfuncs.ServiceRouter
	if e.svc.Router != nil {
		router = callerRouter{caller: name, r: e.svc.Router}
	}
	return &hostfuncs.Dispatcher{Ctx: ctx, Check: chk, Secrets: e.svc.Secrets, KV: kv, Services: router,
		Dial: e.svc.Dial, RootCAs: e.svc.RootCAs, Timeout: time.Duration(lim.TimeoutS) * time.Second,
		Log: func(level, msg string) {
			lv := map[string]slog.Level{"debug": slog.LevelDebug, "warn": slog.LevelWarn, "error": slog.LevelError}[level]
			log.Log(context.Background(), lv, msg, "connector", name)
		}}, nil
}

// Manifest — манифест пакета (совпадает с манифестом модуля).
func (c *Connector) Manifest() manifest.Manifest { return c.man }

// Execute исполняет действие в новом экземпляре модуля.
func (c *Connector) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	var res abi.ExecResult
	d, err := c.dispatcher(ctx, req.Settings)
	if err != nil {
		return res, err
	}
	defer d.Close()
	in, _ := json.Marshal(req)
	if err := c.invoke(ctx, abi.ExportExecute, d, in, &res); err != nil {
		return res, err
	}
	res.Evidence = append(res.Evidence, d.Evidence()...)
	return res, nil
}

// Verify проверяет результат в новом экземпляре модуля.
func (c *Connector) Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error) {
	var res abi.VerifyResult
	d, err := c.dispatcher(ctx, req.Settings)
	if err != nil {
		return res, err
	}
	defer d.Close()
	in, _ := json.Marshal(req)
	if err := c.invoke(ctx, abi.ExportVerify, d, in, &res); err != nil {
		return res, err
	}
	res.Evidence = append(res.Evidence, d.Evidence()...)
	return res, nil
}

// Close выгружает модуль.
func (c *Connector) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.rt.Close(context.Background())
}

// Lazy — коннектор, который компилируется в фоне: манифест пакета доступен
// сразу (каталог действий и проверка политик при старте daemon), вызовы ждут
// окончания компиляции и сверки манифеста модуля.
type Lazy struct {
	man  manifest.Manifest
	done chan struct{}
	c    *Connector
	err  error
}

// LoadLazy начинает компиляцию в фоне.
func (e *Engine) LoadLazy(wasmBytes []byte, pkg manifest.Manifest) *Lazy {
	l := &Lazy{man: pkg, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		l.c, l.err = e.Load(context.Background(), wasmBytes, pkg)
		if l.err != nil {
			e.svc.Log.Error("connector not loaded", "connector", pkg.Connector, "err", l.err)
		}
	}()
	return l
}

func (l *Lazy) ready(ctx context.Context) (*Connector, error) {
	select {
	case <-l.done:
		return l.c, l.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Manifest — манифест пакета.
func (l *Lazy) Manifest() manifest.Manifest { return l.man }

// Execute — после компиляции.
func (l *Lazy) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	c, err := l.ready(ctx)
	if err != nil {
		return abi.ExecResult{}, err
	}
	return c.Execute(ctx, req)
}

// Verify — после компиляции.
func (l *Lazy) Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error) {
	c, err := l.ready(ctx)
	if err != nil {
		return abi.VerifyResult{}, err
	}
	return c.Verify(ctx, req)
}

// Close — после компиляции.
func (l *Lazy) Close() error {
	<-l.done
	if l.c != nil {
		return l.c.Close()
	}
	return nil
}
