// SPDX-License-Identifier: AGPL-3.0-only

package wasm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

var (
	probeOnce sync.Once
	probeWasm []byte
	probeMan  manifest.Manifest
	probeErr  error
)

func probe(t *testing.T) ([]byte, manifest.Manifest) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a WASM module")
	}
	probeOnce.Do(func() {
		dir, _ := os.MkdirTemp("", "g4aprobe")
		out := filepath.Join(dir, "probe.wasm")
		if probeErr = BuildGo(context.Background(), "testdata/probe", out); probeErr != nil {
			return
		}
		probeWasm, probeErr = os.ReadFile(out)
		y, _ := os.ReadFile("testdata/probe/manifest.yaml")
		probeMan, _ = manifest.Parse(y)
		os.RemoveAll(dir)
	})
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	return probeWasm, probeMan
}

type vault map[string]string

func (v vault) Value(a string) (string, error) {
	if s, ok := v[a]; ok {
		return s, nil
	}
	return "", errors.New("no such secret")
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

var engineCache = filepath.Join(os.TempDir(), "gate4ai-test-wasm-cache")

func load(t *testing.T, logs io.Writer) *Connector {
	t.Helper()
	bin, man := probe(t)
	e, err := NewEngine(engineCache, Services{Secrets: vault{"@probe": "s3cr3t-value"}, KV: func(n string) hostfuncs.KV { return KVDir(t.TempDir())(n) },
		Log: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.Load(context.Background(), bin, man)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func run(t *testing.T, c *Connector, action string, args map[string]any) (abi.ExecResult, error) {
	t.Helper()
	return c.Execute(context.Background(), abi.ExecRequest{Action: action, Args: args, Settings: map[string]any{"token": "@probe"}})
}

func TestCapabilityDeniedForEveryFunction(t *testing.T) {
	c := load(t, io.Discard)
	res, err := run(t, c, "deny_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Structured.(map[string]any)
	if len(got) != 6 {
		t.Fatal(got)
	}
	for fn, code := range got {
		if code != abi.CodeCapabilityDenied {
			t.Errorf("%s: %v", fn, code)
		}
	}
}

func TestKVEvidenceAndVerify(t *testing.T) {
	c := load(t, io.Discard)
	res, err := run(t, c, "kv", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Structured.(map[string]any)
	if r["value"] != "1" || r["quota"] != abi.CodeLimit || len(res.Evidence) != 1 || string(res.Evidence[0].Data) != "1" {
		t.Fatal(res)
	}
	v, err := c.Verify(context.Background(), abi.VerifyRequest{Action: "kv", Settings: map[string]any{"token": "@probe"}})
	if err != nil || v.Status != abi.Verified {
		t.Fatal(v, err)
	}
}

// time.Sleep в коннекторе работает (WASI nanosleep) и не роняет модуль.
func TestSleep(t *testing.T) {
	c := load(t, io.Discard)
	res, err := run(t, c, "sleep", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ms, _ := res.Structured.(map[string]any)["slept_ms"].(float64); ms < 1100 {
		t.Fatal(res.Structured)
	}
}

func TestLimits(t *testing.T) {
	c := load(t, io.Discard)
	_, err := run(t, c, "spin", nil)
	var e *abi.Error
	if !errors.As(err, &e) || e.Code != abi.CodeLimit {
		t.Fatal("time limit:", err)
	}
	if _, err := run(t, c, "hog", nil); err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Fatal("memory limit:", err)
	}
	if _, err := run(t, c, "crash", nil); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatal("panic:", err)
	}
	// После сбоев коннектор продолжает работать: каждый вызов — новый экземпляр.
	if _, err := run(t, c, "settings", nil); err != nil {
		t.Fatal(err)
	}
}

func TestManifestMustMatchPackage(t *testing.T) {
	bin, man := probe(t)
	e, _ := NewEngine("", Services{})
	man.Folders = append(man.Folders, access.Folder{Path: "/", Access: access.Allow})
	if _, err := e.Load(context.Background(), bin, man); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatal("package manifest with wider capabilities accepted:", err)
	}
	man.Runtime = manifest.RuntimeBuiltin
	if _, err := e.Load(context.Background(), bin, man); err == nil {
		t.Fatal("non-wasm runtime accepted")
	}
	if _, err := e.Load(context.Background(), []byte("not wasm"), probeMan); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestLazyLoad(t *testing.T) {
	bin, man := probe(t)
	e, _ := NewEngine(engineCache, Services{Secrets: vault{"@probe": "x"}})
	l := e.LoadLazy(bin, man)
	defer l.Close()
	if l.Manifest().Connector != "probe" {
		t.Fatal("package manifest must be available at once")
	}
	res, err := l.Execute(context.Background(), abi.ExecRequest{Action: "settings", Settings: map[string]any{"token": "@probe"}})
	if err != nil || res.Structured.(map[string]any)["token"] != "@probe" {
		t.Fatal(res, err)
	}
	bad := man
	bad.Version = "9"
	lb := e.LoadLazy(bin, bad)
	if _, err := lb.Execute(context.Background(), abi.ExecRequest{Action: "settings"}); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatal("mismatched manifest must fail on call:", err)
	}
	lb.Close()
}

// noMemoryModule — модуль с экспортами ABI, но без линейной памяти:
// type ()->(), четыре пустые функции, их экспорт.
func noMemoryModule() []byte {
	b := []byte{0, 'a', 's', 'm', 1, 0, 0, 0}
	sec := func(id byte, body []byte) { b = append(append(b, id, byte(len(body))), body...) }
	sec(1, []byte{1, 0x60, 0, 0})
	sec(3, []byte{4, 0, 0, 0, 0})
	exp := []byte{4}
	for i, n := range []string{abi.ExportAlloc, abi.ExportManifest, abi.ExportExecute, abi.ExportVerify} {
		exp = append(append(append(exp, byte(len(n))), n...), 0, byte(i))
	}
	sec(7, exp)
	sec(10, []byte{4, 2, 0, 0x0b, 2, 0, 0x0b, 2, 0, 0x0b, 2, 0, 0x0b})
	return b
}

// Модуль без памяти не загружается и не роняет daemon (раньше — nil-разыменование
// в хосте при чтении манифеста в фоновой загрузке).
func TestModuleWithoutMemory(t *testing.T) {
	e, _ := NewEngine("", Services{})
	man := manifest.Manifest{Connector: "nomem", Version: "1", Runtime: manifest.RuntimeWasm}
	if _, err := e.Load(context.Background(), noMemoryModule(), man); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatal("module without memory:", err)
	}
	l := e.LoadLazy(noMemoryModule(), man)
	if _, err := l.Execute(context.Background(), abi.ExecRequest{Action: "x"}); err == nil {
		t.Fatal("lazy load of a module without memory must fail")
	}
	l.Close()
}

// memory_mb больше предела wazero (4096) — ошибка загрузки, а не паника в
// фоновой горутине (раньше daemon падал при каждом старте).
func TestHugeMemoryLimit(t *testing.T) {
	e, _ := NewEngine("", Services{})
	man := manifest.Manifest{Connector: "huge", Version: "1", Runtime: manifest.RuntimeWasm, Limits: manifest.Limits{MemoryMB: 5000}}
	if _, err := e.Load(context.Background(), noMemoryModule(), man); err == nil || !strings.Contains(err.Error(), "memory_mb") {
		t.Fatal("memory_mb 5000:", err)
	}
	man.Limits.MemoryMB = 1 << 28 // uint32(MemoryMB)*16 переполнялся
	l := e.LoadLazy(noMemoryModule(), man)
	if _, err := l.Execute(context.Background(), abi.ExecRequest{Action: "x"}); err == nil {
		t.Fatal("lazy load with a huge memory limit must fail")
	}
	l.Close()
}

// Одновременных экземпляров коннектора не больше MaxConcurrentCalls: лишний
// вызов ждёт свободного места и по сроку получает limit.
func TestConcurrentCallsLimited(t *testing.T) {
	c := load(t, io.Discard)
	for i := 0; i < MaxConcurrentCalls; i++ {
		c.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Execute(ctx, abi.ExecRequest{Action: "settings", Settings: map[string]any{"token": "@probe"}})
	if err == nil {
		t.Fatal("call ran above MaxConcurrentCalls")
	}
	for i := 0; i < MaxConcurrentCalls; i++ {
		<-c.slots
	}
	if _, err := run(t, c, "settings", nil); err != nil {
		t.Fatal(err)
	}
}
