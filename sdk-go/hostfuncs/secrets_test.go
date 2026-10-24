// SPDX-License-Identifier: Apache-2.0

package hostfuncs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// Текстовые доказательства маскируются по значениям секретов, подставленным
// хостом в этом вызове (used); изображения — нет.
func TestEvidenceMasked(t *testing.T) {
	d := dispatcher(t, `capabilities: {}`, nil)
	d.used = []string{"tok-SECRET-123"}
	call(t, d, abi.FnEvidenceAdd, abi.EvidenceItem{Name: "a.txt", MIME: "text/plain", Data: []byte("key tok-SECRET-123")}, nil)
	call(t, d, abi.FnEvidenceAdd, abi.EvidenceItem{Name: "a.png", MIME: "image/png", Data: []byte("\x89PNG tok-SECRET-123")}, nil)
	ev := d.Evidence()
	if len(ev) != 2 || strings.Contains(string(ev[0].Data), "tok-SECRET") || !strings.Contains(string(ev[1].Data), "tok-SECRET") {
		t.Fatal(ev)
	}
	if got := d.Mask("x tok-SECRET-123 y"); got != "x [secret] y" {
		t.Fatal(got)
	}
}

// Подмена каталога симлинком после проверки пути не выводит запись за
// пределы разрешённого корня (TOCTOU).
func TestFSSymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "a"), 0o755)
	d := dispatcher(t, `capabilities: { files: ["${setting:root}/**"] }`, map[string]any{"root": ws})
	p := filepath.Join(ws, "a", "authorized_keys")
	loc, err := d.Check.Locate(p)
	if err != nil {
		t.Fatal(err)
	}
	// Гонка: после проверки ws/a заменён симлинком наружу.
	os.RemoveAll(filepath.Join(ws, "a"))
	if err := os.Symlink(outside, filepath.Join(ws, "a")); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{abi.FnFSWrite, abi.FnFSAppend} {
		if _, err := d.fsAt(fn, abi.FSRequest{Path: p, Data: []byte("ssh-ed25519 attacker")}, loc); err == nil {
			t.Errorf("%s: write through a swapped symlink", fn)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatal("file written outside the root:", ents[0].Name())
	}
	// Правило на один файл: подмена самого файла симлинком.
	one := filepath.Join(ws, "todo.md")
	os.WriteFile(one, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(ws, "other"), []byte("keep"), 0o644)
	d1 := dispatcher(t, `capabilities: { files: ["`+one+`"] }`, nil)
	loc, err = d1.Check.Locate(one)
	if err != nil || !loc.Exact {
		t.Fatal(loc, err)
	}
	os.Remove(one)
	os.Symlink("other", one)
	if _, err := d1.fsAt(abi.FnFSAppend, abi.FSRequest{Path: one, Data: []byte("bad")}, loc); err == nil {
		t.Fatal("append through a swapped file symlink")
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "other")); string(b) != "keep" {
		t.Fatal(string(b))
	}
	// Без подмены всё работает.
	os.Remove(one)
	if _, err := d1.fs(abi.FnFSAppend, abi.FSRequest{Path: one, Data: []byte("ok")}); err != nil {
		t.Fatal(err)
	}
}
