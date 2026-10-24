// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Разработка коннектора: установка из папки с исходниками (daemon собирает
// сам), update пересобирает из той же папки, --watch — на каждое изменение.
func TestConnectorFromSourceFolder(t *testing.T) {
	t.Parallel()
	g := startGate(t)
	dir := filepath.Join(t.TempDir(), "pagereader-src")
	if err := os.CopyFS(dir, os.DirFS(src(t, "e2e/testdata/pagereader"))); err != nil {
		t.Fatal(err)
	}
	// Папка вне репозитория, как у стороннего автора: свой go.mod, модуль gate4ai —
	// из этого репозитория (replace), зависимости — из кэша модулей.
	mod := "module example.com/pagereader-src\n\ngo 1.27.1\n\nrequire github.com/gate4ai/gate4ai v0.0.0\n\nreplace github.com/gate4ai/gate4ai => " + src(t, "") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir, tidy.Env = dir, append(g.env, "GOFLAGS=-mod=mod")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}

	setVersion := func(v string) {
		m := filepath.Join(dir, "manifest.yaml")
		b, _ := os.ReadFile(m)
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "version:") {
				lines[i] = `version: "` + v + `"`
			}
		}
		os.WriteFile(m, []byte(strings.Join(lines, "\n")), 0o644)
	}
	version := func() string {
		r := g.run("", "", "connectors", "list", "--json")
		list, ok := r.json(t)["connectors"].([]any)
		if !ok {
			t.Fatalf("connectors list: %s", r.out)
		}
		for _, c := range list {
			if c := c.(map[string]any); c["connector"] == "pagereader" {
				return c["version"].(string)
			}
		}
		return ""
	}

	r := g.run("", "", "connectors", "install", dir, "--yes")
	r.out = r.out[strings.Index(r.out, "{\n"):] // перед JSON — возможности для согласия (stderr)
	res := r.json(t)
	inst, _ := res["installed"].(map[string]any)
	if r.code != 0 || inst["source"] != "dir:"+dir || inst["trust"] != "local" {
		t.Fatalf("install from folder: %d %s", r.code, r.out)
	}

	setVersion("dev1")
	if r := g.run("", "", "connectors", "update", "pagereader"); r.code != 0 || version() != "dev1" {
		t.Fatalf("update from folder: %d %s (version %q)", r.code, r.out, version())
	}

	w := exec.Command(g.bin, "connectors", "update", "pagereader", "--watch", "--yes")
	w.Env = g.env
	wout := &lockedBuf{}
	w.Stdout, w.Stderr = wout, wout
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { w.Process.Signal(os.Interrupt); w.Wait() }()
	for start := time.Now(); !strings.Contains(wout.String(), "watching"); time.Sleep(100 * time.Millisecond) {
		if time.Since(start) > time.Minute {
			t.Fatalf("--watch did not start:\n%s", wout.String())
		}
	}
	setVersion("dev2")
	deadline := time.Now().Add(2 * time.Minute)
	for version() != "dev2" {
		if time.Now().After(deadline) {
			t.Fatalf("--watch did not rebuild:\n%s", wout.String())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// lockedBuf — вывод фонового процесса, который читает тест.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }
