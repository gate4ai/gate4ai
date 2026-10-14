// SPDX-License-Identifier: FSL-1.1-ALv2

package wasm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolchain(t *testing.T) {
	for _, c := range []struct{ gomod, want string }{
		{"module x\n\ngo 1.27.1\n", "go1.27.1"},
		{"module x\n\ngo 1.27\n", "go1.27.0"},
		{"module x\n\ngo 1.27.1\n\ntoolchain go1.27.3\n", "go1.27.3"},
		{"module x\n", ""},
		{"module x\n\ngo 1.27.1\n\ntoolchain local\n", ""},
	} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte(c.gomod), 0o644)
		// go.mod ищется вверх от каталога исходников, как это делает go.
		sub := filepath.Join(dir, "cmd", "x")
		os.MkdirAll(sub, 0o755)
		got, err := Toolchain(sub)
		if c.want == "" {
			if err == nil {
				t.Errorf("%q: want error, got %s", c.gomod, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q %v, want %q", c.gomod, got, err, c.want)
		}
	}
	if _, err := Toolchain(t.TempDir()); err == nil {
		t.Error("no go.mod: want error")
	}
}
