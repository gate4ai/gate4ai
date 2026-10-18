// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/connectors/store"
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

// Пакет, собранный из исходников, совпадает с пересборкой тех же исходников в
// другом каталоге (воспроизводимость для ревью registry); изменённый код — нет.
func TestPackVerifyReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("builds WASM")
	}
	// Временный модуль коннектора — вне общего go.work скриптов проекта (test-set.sh).
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	run := func(args ...string) (int, map[string]any, string) {
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		env := &Env{Stdout: out, Stderr: errb, Getenv: func(string) string { return "" }}
		code := Run(context.Background(), env, args)
		var m map[string]any
		_ = json.Unmarshal(out.Bytes(), &m)
		return code, m, out.String() + errb.String()
	}
	write := func(src, body string) {
		os.MkdirAll(src, 0o755)
		os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/hello\n\ngo "+strings.TrimPrefix(runtime.Version(), "go")+"\n"), 0o644)
		os.WriteFile(filepath.Join(src, "manifest.yaml"), []byte("connector: hello\nversion: \"1\"\nruntime: wasm\nactions: [ { name: say, class: read } ]\n"), 0o644)
		os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() { println(\""+body+"\") }\n"), 0o644)
	}
	a, b := filepath.Join(dir, "publisher", "hello"), filepath.Join(dir, "reviewer", "src")
	write(a, "hi")
	write(b, "hi")
	pkg := filepath.Join(dir, "hello-1.g4c")
	code, p, out := run("connectors", "pack", a, "-o", pkg)
	if code != 0 || p["go"] != runtime.Version() {
		t.Fatal(code, out)
	}
	code, v, out := run("connectors", "verify", b, pkg)
	if code != 0 || v["reproducible"] != true || v["digest"] != p["digest"] {
		t.Fatal("same sources in another directory must rebuild to the same package:", out)
	}
	write(b, "changed")
	if code, _, out := run("connectors", "verify", b, pkg); code == 0 || !strings.Contains(out, "connector.wasm differs") {
		t.Fatal("changed sources accepted:", out)
	}
}

// Нативный пакет: pack собирает под каждую платформу и список платформ;
// verify пересобирает каждую; одна платформа с -o file.g4c — только этот пакет.
func TestPackVerifyNative(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries")
	}
	// Временный модуль коннектора — вне общего go.work скриптов проекта (test-set.sh).
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	run := func(args ...string) (int, map[string]any, string) {
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		env := &Env{Stdout: out, Stderr: errb, Getenv: func(string) string { return "" }}
		code := Run(context.Background(), env, args)
		var m map[string]any
		_ = json.Unmarshal(out.Bytes(), &m)
		return code, m, out.String() + errb.String()
	}
	write := func(src, body string) {
		os.MkdirAll(src, 0o755)
		os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/nat\n\ngo "+strings.TrimPrefix(runtime.Version(), "go")+"\n"), 0o644)
		os.WriteFile(filepath.Join(src, "manifest.yaml"), []byte("connector: nat\nversion: \"1\"\nruntime: native\nplatforms: [linux/amd64, windows/amd64]\nactions: [ { name: say, class: read } ]\n"), 0o644)
		os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() { println(\""+body+"\") }\n"), 0o644)
	}
	a, b := filepath.Join(dir, "publisher", "nat"), filepath.Join(dir, "reviewer", "src")
	write(a, "hi")
	write(b, "hi")
	out := filepath.Join(dir, "rel", "nat-1"+store.SetExt)
	os.MkdirAll(filepath.Dir(out), 0o755)
	code, p, msg := run("connectors", "pack", a, "-o", out)
	if code != 0 || p["package"] != "nat-1"+store.SetExt || len(p["platforms"].(map[string]any)) != 2 {
		t.Fatal(code, msg)
	}
	for _, f := range []string{"nat-1-linux-amd64.g4c", "nat-1-windows-amd64.g4c"} {
		if _, err := os.Stat(filepath.Join(dir, "rel", f)); err != nil {
			t.Fatal(err)
		}
	}
	code, v, msg := run("connectors", "verify", b, out)
	if code != 0 || v["reproducible"] != true || v["digest"] != p["digest"] || v["platforms"] != 2.0 {
		t.Fatal("same sources must rebuild every platform:", msg)
	}
	write(b, "changed")
	if code, _, msg := run("connectors", "verify", b, out); code == 0 || !strings.Contains(msg, "differs") {
		t.Fatal("changed sources accepted:", msg)
	}
	one := filepath.Join(dir, "one.g4c")
	if code, p, msg := run("connectors", "pack", a, "-o", one, "--platform", "linux/amd64"); code != 0 || p["platform"] != "linux/amd64" {
		t.Fatal(code, msg)
	}
	pkg, err := os.ReadFile(one)
	if err != nil {
		t.Fatal(err)
	}
	if pp, err := store.Open(pkg); err != nil || !pp.Native() || pp.Sig.Platform != "linux/amd64" {
		t.Fatal(pp, err)
	}
}

// Сторонний издатель: pack → registry add (новый издатель — community) →
// check --fetch сверяет опубликованный пакет с записью; подмена пакета,
// повтор версии и чужой ключ отклоняются.
func TestRegistryAddAndFetch(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) (int, map[string]any, string) {
		out, errb := &bytes.Buffer{}, &bytes.Buffer{}
		env := &Env{Stdout: out, Stderr: errb, Getenv: func(string) string { return "" }}
		code := Run(context.Background(), env, args)
		var m map[string]any
		_ = json.Unmarshal(out.Bytes(), &m)
		return code, m, out.String() + errb.String()
	}
	run("connectors", "keygen", "-o", filepath.Join(dir, "acme.key"))
	run("connectors", "keygen", "-o", filepath.Join(dir, "other.key"))
	src := filepath.Join(dir, "hello")
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "manifest.yaml"), []byte("connector: hello\nversion: \"1\"\nruntime: wasm\nactions: [ { name: say, class: read } ]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "m.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o644)
	pack := func(key, out string) string {
		_, _, o := run("connectors", "pack", src, "--wasm", filepath.Join(dir, "m.wasm"), "--key", filepath.Join(dir, key), "-o", filepath.Join(dir, out))
		pj := filepath.Join(dir, out+".json")
		os.WriteFile(pj, []byte(o), 0o644)
		return pj
	}
	pj := pack("acme.key", "hello-1.g4c")
	ix := filepath.Join(dir, "index.json")
	os.WriteFile(ix, []byte(`{"version":1,"publishers":{},"connectors":{}}`), 0o644)

	if code, _, out := run("registry", "add", ix, pj, "--publisher", "acme"); code == 0 || !strings.Contains(out, "repo") {
		t.Fatal("new connector without repo/license accepted:", out)
	}
	code, r, out := run("registry", "add", ix, pj, "--publisher", "acme", "--repo", "https://github.com/acme/gate4ai-hello",
		"--license", "AGPL-3.0-only", "--url", "hello-1.g4c")
	if code != 0 || r["trust"] != "community" {
		t.Fatal(code, out)
	}
	if code, r, out := run("registry", "check", ix, "--fetch"); code != 0 || r["packages_verified"] != 1.0 {
		t.Fatal(code, out)
	}
	if code, _, out := run("registry", "add", ix, pj, "--publisher", "acme", "--url", "hello-1.g4c"); code == 0 || !strings.Contains(out, "immutable") {
		t.Fatal("same version added twice:", out)
	}
	// Другой ключ под тем же издателем — только решением ревьюера.
	os.WriteFile(filepath.Join(src, "manifest.yaml"), []byte("connector: hello\nversion: \"2\"\nruntime: wasm\nactions: [ { name: say, class: read } ]\n"), 0o644)
	pj2 := pack("other.key", "hello-2.g4c")
	if code, _, out := run("registry", "add", ix, pj2, "--publisher", "acme"); code == 0 || !strings.Contains(out, "not a key of this publisher") {
		t.Fatal("foreign key accepted:", out)
	}
	// URL по умолчанию — релиз GitHub из repo существующей записи.
	pj2 = pack("acme.key", "hello-2.g4c")
	if code, r, out := run("registry", "add", ix, pj2, "--publisher", "acme"); code != 0 ||
		r["url"] != "https://github.com/acme/gate4ai-hello/releases/download/v2/hello-2.g4c" {
		t.Fatal(code, out)
	}
	// Подменённый пакет не проходит --fetch.
	os.WriteFile(ix, []byte(strings.Replace(mustRead(t, ix), "https://github.com/acme/gate4ai-hello/releases/download/v2/hello-2.g4c", "hello-2.g4c", 1)), 0o644)
	os.WriteFile(filepath.Join(dir, "hello-1.g4c"), mustReadB(t, filepath.Join(dir, "hello-2.g4c")), 0o644)
	if code, _, out := run("registry", "check", ix, "--fetch"); code == 0 || !strings.Contains(out, "hello 1") {
		t.Fatal("substituted package passed:", out)
	}
}

func mustReadB(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustRead(t *testing.T, p string) string { return string(mustReadB(t, p)) }

// --watch реагирует только на исходники коннектора: .go, go.mod, go.sum, манифест;
// скрытые папки, vendor, testdata и прочие файлы не считаются.
func TestSourcesStamp(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644)
	base := sourcesStamp(dir)
	for _, f := range []string{".git/index", "README.md", "connector.wasm"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
		if sourcesStamp(dir) != base {
			t.Fatal("not a source file changed the stamp:", f)
		}
	}
	for _, f := range []string{"sub/x.go", "manifest.yaml", "go.sum"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
		if s := sourcesStamp(dir); s == base {
			t.Fatal("source change not seen:", f)
		} else {
			base = s
		}
	}
}
