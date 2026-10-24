// SPDX-License-Identifier: Apache-2.0

package capability

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

func isDenied(err error) bool {
	var e *abi.Error
	return errors.As(err, &e) && e.Code == abi.CodeCapabilityDenied
}

func checker(t *testing.T, yaml string, settings map[string]any, home string) *Checker {
	t.Helper()
	m, err := manifest.Parse([]byte("connector: x\n" + yaml))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(m, settings, home)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Ничего не объявлено — всё запрещено: вызовы других коннекторов, хранилище, файлы.
func TestUndeclaredDenied(t *testing.T) {
	c := checker(t, `capabilities: {}`, nil, "")
	if _, err := c.Service("browser", "open"); !isDenied(err) {
		t.Fatal("service not declared")
	}
	if _, err := c.Storage(); !isDenied(err) {
		t.Fatal("storage not declared")
	}
	if _, err := c.Path("/tmp/x"); !isDenied(err) {
		t.Fatal("files not declared")
	}
}

func TestPaths(t *testing.T) {
	home := t.TempDir()
	notes := filepath.Join(home, "notes")
	os.MkdirAll(filepath.Join(notes, "sub"), 0o755)
	secret := filepath.Join(home, "secret")
	os.MkdirAll(secret, 0o755)
	os.WriteFile(filepath.Join(secret, "key"), []byte("k"), 0o600)
	os.Symlink(secret, filepath.Join(notes, "link"))
	one := filepath.Join(home, "todo.md")
	c := checker(t, `capabilities: { files: ["${setting:roots}/**", "`+one+`"] }`, map[string]any{"roots": []any{"~/notes"}}, home)
	for _, p := range []string{"~/notes/a.md", notes + "/sub/b.md", notes, one} {
		if _, err := c.Path(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"~/notes/../secret/key", notes + "/link/key", "notes/a.md", home + "/other.md", "~/notes2/x"} {
		if _, err := c.Path(p); !isDenied(err) {
			t.Errorf("%s allowed: %v", p, err)
		}
	}
}

func TestServiceParams(t *testing.T) {
	const caps = `capabilities: { services: [ { target: browser, contract: 2, actions: [open, act],
  params: { mode: "${setting:mode}", domains: ["${setting:allowed}", fixed.example], limit: 3 } } ] }`
	c := checker(t, caps, map[string]any{"mode": "allowlist", "allowed": []any{"*.bank.example", "b.example"}}, "")
	r, err := c.Service("browser", "act")
	if err != nil || r.Contract != 2 {
		t.Fatal(r, err)
	}
	if fmt.Sprint(r.Params) != "map[domains:[*.bank.example b.example fixed.example] limit:3 mode:allowlist]" {
		t.Fatal(r.Params)
	}
	if _, err := c.Service("browser", "extract"); !isDenied(err) {
		t.Fatal("undeclared action", err)
	}
	// Пустая настройка: значение "" и пустой элемент списка.
	r, _ = checker(t, caps, nil, "").Service("browser", "open")
	if fmt.Sprint(r.Params) != "map[domains:[fixed.example] limit:3 mode:]" {
		t.Fatal(r.Params)
	}
	for _, bad := range []string{
		`capabilities: { services: [ { target: browser, contract: 0, actions: [open] } ] }`,
		`capabilities: { services: [ { target: browser, contract: 1, actions: [] } ] }`,
		`capabilities: { services: [ { target: Browser, contract: 1, actions: [open] } ] }`,
		`capabilities: { services: [ { target: b, contract: 1, actions: [open], params: { x: "${bad}" } } ] }`,
		`capabilities: { reveal: ["@key"] }`, // reveal — только builtin
	} {
		if _, err := manifest.Parse([]byte("connector: x\n" + bad)); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
}
