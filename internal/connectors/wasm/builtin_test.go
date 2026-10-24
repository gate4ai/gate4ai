// SPDX-License-Identifier: AGPL-3.0-only

package wasm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// echoFS — коннектор ядра на PDK: пишет и читает файл через функции хоста.
type echoFS struct{}

func (echoFS) Execute(_ context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	p, _ := r.Args["path"].(string)
	w, err := h.FSAppend(p, []byte("x"))
	if err != nil {
		return abi.ExecResult{}, err
	}
	h.Evidence("e.txt", "text/plain", []byte("ok"))
	return abi.ExecResult{ResultRef: w.RealPath}, nil
}

func (echoFS) Verify(_ context.Context, h *abi.Host, r abi.VerifyRequest) (abi.VerifyResult, error) {
	p, _ := r.Args["path"].(string)
	f, err := h.FSRead(p, 10)
	if err != nil {
		return abi.VerifyResult{}, err
	}
	return abi.VerifyResult{Status: abi.Verified, Detail: string(f.Data)}, nil
}

func TestBuiltinUsesHostChecks(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "notes"), 0o755)
	e, err := NewEngine("", Services{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	m, err := manifest.Parse([]byte(`connector: echo
version: "1"
runtime: builtin
capabilities:
  files: ["${setting:root}/**"]
actions:
  - name: add
    class: write
    input_schema: { type: object, properties: { path: { type: string } }, required: [path] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Builtin(manifest.Manifest{Connector: "x", Runtime: manifest.RuntimeWasm}, echoFS{}); err == nil {
		t.Fatal("wasm runtime accepted as builtin")
	}
	b, err := e.Builtin(m, echoFS{})
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]any{"root": "~/notes"}
	res, err := b.Execute(context.Background(), abi.ExecRequest{Action: "add", Args: map[string]any{"path": "~/notes/a.md"}, Settings: set})
	if err != nil || res.ResultRef != filepath.Join(home, "notes", "a.md") || len(res.Evidence) != 1 {
		t.Fatal(res, err)
	}
	v, err := b.Verify(context.Background(), abi.VerifyRequest{Action: "add", Args: map[string]any{"path": "~/notes/a.md"}, Settings: set})
	if err != nil || v.Detail != "x" {
		t.Fatal(v, err)
	}
	_, err = b.Execute(context.Background(), abi.ExecRequest{Action: "add", Args: map[string]any{"path": "~/other.md"}, Settings: set})
	if err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
		t.Fatal("path outside capabilities:", err)
	}
}

// slowBuiltin ждёт отмены контекста вызова: встроенный коннектор получает
// срок и отмену хоста, как WASM и нативный.
type slowBuiltin struct{}

func (slowBuiltin) Execute(ctx context.Context, _ *abi.Host, _ abi.ExecRequest) (abi.ExecResult, error) {
	select {
	case <-ctx.Done():
		return abi.ExecResult{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return abi.ExecResult{ResultRef: "no ctx"}, nil
	}
}

func (slowBuiltin) Verify(context.Context, *abi.Host, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func TestBuiltinGetsCallContext(t *testing.T) {
	e, err := NewEngine("", Services{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	m, err := manifest.Parse([]byte("connector: slow\nversion: \"1\"\nruntime: builtin\nactions:\n  - name: wait\n    class: read\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Builtin(m, slowBuiltin{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	res, err := b.Execute(ctx, abi.ExecRequest{Action: "wait"})
	if err == nil || res.ResultRef == "no ctx" || time.Since(start) > 2*time.Second {
		t.Fatalf("builtin ignored the call context: %v %v after %s", res, err, time.Since(start))
	}
}
