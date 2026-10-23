// SPDX-License-Identifier: AGPL-3.0-only

package agentkeys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithinDir(t *testing.T) {
	root := filepath.FromSlash("/home/u/proj")
	for dir, want := range map[string]bool{
		"/home/u/proj": true, "/home/u/proj/sub/x": true, "/home/u/proj2": false, "/home/u": false, "/": false, "": false,
		"/home/u/proj/../other": false,
	} {
		if WithinDir(filepath.FromSlash(dir), root) != want {
			t.Errorf("WithinDir(%q) != %v", dir, want)
		}
	}
	if WithinDir("/x", "") {
		t.Error("empty root")
	}
}

func TestSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	if err := Save(dir, Key{Agent: "Claude Code/Proj", Kind: "claude-code", Key: "g4a_x"}); err != nil {
		t.Fatal(err)
	}
	p := Path(dir, "Claude Code/Proj")
	if filepath.Base(p) != "claude-code-proj.json" {
		t.Fatal(p)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
	ks := Load(dir)
	if len(ks) != 1 || ks[0].Key != "g4a_x" || ks[0].Created.IsZero() {
		t.Fatalf("%+v", ks)
	}
	Remove(dir, "Claude Code/Proj")
	if len(Load(dir)) != 0 {
		t.Fatal("not removed")
	}
}
