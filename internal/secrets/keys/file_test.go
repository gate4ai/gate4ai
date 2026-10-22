// SPDX-License-Identifier: AGPL-3.0-only

package keys

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Права существующих каталога и файла сужаются до 0700 / 0600 при записи.
func TestFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Старый временный файл с широкими правами не используется.
	if err := os.WriteFile(path+".tmp", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFile(path)
	if err := f.Set("a", "fake-value"); err != nil {
		t.Fatal(err)
	}
	if v, err := f.Get("a"); err != nil || v != "fake-value" {
		t.Fatal(v, err)
	}
	fi, _ := os.Stat(path)
	di, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
		t.Fatalf("file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != "keys.json" && e.Name() != "keys.json.tmp" {
			t.Fatal("temp file left:", e.Name())
		}
	}
}
