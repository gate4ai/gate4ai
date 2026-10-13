// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Офлайн-команды издателя и registry: ключ, пакет, подпись индекса.
func TestPublisherCommands(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) (int, map[string]any, string) {
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		env := &Env{Stdout: out, Stderr: errb, Getenv: func(string) string { return "" }}
		code := Run(context.Background(), env, args)
		var m map[string]any
		_ = json.Unmarshal(out.Bytes(), &m)
		return code, m, out.String() + errb.String()
	}
	code, k, out := run("connectors", "keygen", "-o", filepath.Join(dir, "pub.key"))
	if code != 0 || !strings.HasPrefix(k["public_key"].(string), "ed25519:") {
		t.Fatal(code, out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pub.key")); !strings.HasPrefix(string(b), "ed25519-private:") {
		t.Fatal("private key file")
	}
	src := filepath.Join(dir, "hello")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "manifest.yaml"), []byte("connector: hello\nversion: \"1\"\nruntime: wasm\nactions: [ { name: say, class: read } ]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "m.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o644)
	pkg := filepath.Join(dir, "hello.g4c")
	code, p, out := run("connectors", "pack", src, "--wasm", filepath.Join(dir, "m.wasm"), "--key", filepath.Join(dir, "pub.key"), "-o", pkg)
	if code != 0 || p["signed_by"] != k["public_key"] || len(p["sha256"].(string)) != 64 {
		t.Fatal(code, out)
	}
	index := map[string]any{"version": 1,
		"publishers": map[string]any{"acme": map[string]any{"keys": []string{k["public_key"].(string)}, "trust": "publisher"}},
		"connectors": map[string]any{"hello": map[string]any{"publisher": "acme", "repo": "https://github.com/acme/gate4ai-hello", "license": "AGPL-3.0-only",
			"versions": []any{map[string]any{"version": "1", "url": "hello.g4c", "sha256": p["sha256"], "digest": p["digest"], "capabilities": []string{}}}}}}
	ib, _ := json.MarshalIndent(index, "", "  ")
	ixFile := filepath.Join(dir, "index.json")
	os.WriteFile(ixFile, ib, 0o644)
	_, rk, _ := run("connectors", "keygen", "-o", filepath.Join(dir, "registry.key"))
	if code, _, out := run("registry", "sign", ixFile, "--key", filepath.Join(dir, "registry.key")); code != 0 {
		t.Fatal(out)
	}
	if code, r, out := run("registry", "check", ixFile, "--key", rk["public_key"].(string)); code != 0 || r["signature"] != "valid" {
		t.Fatal(out)
	}
	if code, _, _ := run("registry", "check", ixFile, "--key", k["public_key"].(string)); code == 0 {
		t.Fatal("index signature checked with a wrong key")
	}
	os.WriteFile(ixFile, []byte(`{"version":1,"publishers":{},"connectors":{"x":{"publisher":"nobody"}}}`), 0o644)
	if code, _, out := run("registry", "check", ixFile); code == 0 || !strings.Contains(out, "unknown publisher") {
		t.Fatal(out)
	}
}
