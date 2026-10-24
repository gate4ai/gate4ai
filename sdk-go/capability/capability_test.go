// SPDX-License-Identifier: Apache-2.0

package capability

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/access"
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
	if _, err := c.Locate("/tmp/x", access.OpWrite); !isDenied(err) {
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
	os.WriteFile(one, []byte("- x"), 0o644)
	c := checker(t, `folders: [ { path: "${setting:roots}", access: append }, { path: "`+one+`", access: read } ]`,
		map[string]any{"roots": []any{"~/notes"}}, home)
	// Чтение внутри папок: и по ~, и по абсолютному пути, и сама папка.
	for _, p := range []string{"~/notes/a.md", notes + "/sub/b.md", notes, one} {
		if _, err := c.Path(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	// Режим append: чтение, список и добавление — да, перезапись — нет.
	for _, op := range []string{access.OpRead, access.OpList, access.OpAppend} {
		if _, err := c.Locate("~/notes/a.md", op); err != nil {
			t.Errorf("%s in append folder: %v", op, err)
		}
	}
	if _, err := c.Locate("~/notes/a.md", access.OpWrite); !isDenied(err) {
		t.Errorf("write in append folder allowed: %v", err)
	}
	// Строка на один файл в режиме read: чтение — да, запись и добавление — нет.
	loc, err := c.Locate(one, access.OpRead)
	if err != nil || !loc.Exact || loc.Real != one {
		t.Fatal(loc, err)
	}
	for _, op := range []string{access.OpWrite, access.OpAppend} {
		if _, err := c.Locate(one, op); !isDenied(err) {
			t.Errorf("%s of a read-only file allowed: %v", op, err)
		}
	}
	// Вне папок, выход через .. и симлинк, относительный путь — запрещено.
	for _, p := range []string{"~/notes/../secret/key", notes + "/link/key", notes + "/link", "notes/a.md", home + "/other.md", "~/notes2/x", home} {
		if _, err := c.Path(p); !isDenied(err) {
			t.Errorf("%s allowed: %v", p, err)
		}
	}
	// Режим deny в самой строке.
	d := checker(t, `folders: [ { path: "~/notes", access: deny } ]`, nil, home)
	if _, err := d.Path("~/notes/a.md"); !isDenied(err) {
		t.Errorf("deny folder allowed: %v", err)
	}
}

// Список папок Файлов gate4.ai (Global): папка, не перечисленная или закрытая
// там, недоступна и коннектору; любой другой режим — доступ по строке коннектора.
func TestGlobal(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "notes"), 0o755)
	c := checker(t, `folders: [ { path: "~/notes", access: allow } ]`, nil, home)
	for _, tc := range []struct {
		name   string
		global []access.Folder
		ok     bool
	}{
		{"nil", nil, true},
		{"home ask", []access.Folder{{Path: home, Access: access.Ask}}, true},
		{"home read", []access.Folder{{Path: "~", Access: access.Read}}, true},
		{"home deny", []access.Folder{{Path: home, Access: access.Deny}}, false},
		{"other folder", []access.Folder{{Path: "/srv/docs", Access: access.Allow}}, false},
		{"empty list", []access.Folder{}, false},
		{"notes denied below home", []access.Folder{{Path: home, Access: access.Allow}, {Path: filepath.Join(home, "notes"), Access: access.Deny}}, false},
	} {
		c.Global = tc.global
		_, err := c.Locate("~/notes/a.md", access.OpWrite)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && !isDenied(err) {
			t.Errorf("%s: allowed (%v)", tc.name, err)
		}
	}
}

func TestServices(t *testing.T) {
	c := checker(t, `capabilities: { services: [ { target: browser, contract: 2, actions: [open, act] } ] }`, nil, "")
	r, err := c.Service("browser", "act")
	if err != nil || r.Contract != 2 || r.Target != "browser" {
		t.Fatal(r, err)
	}
	if _, err := c.Service("browser", "extract"); !isDenied(err) {
		t.Fatal("undeclared action", err)
	}
	if _, err := c.Service("mail", "send"); !isDenied(err) {
		t.Fatal("undeclared target", err)
	}
	for _, bad := range []string{
		`capabilities: { services: [ { target: browser, contract: 0, actions: [open] } ] }`,
		`capabilities: { services: [ { target: browser, contract: 1, actions: [] } ] }`,
		`capabilities: { services: [ { target: Browser, contract: 1, actions: [open] } ] }`,
		`capabilities: { services: [ { target: b, contract: 1, actions: [open], params: { domains: [a.com] } } ] }`,
		`capabilities: { reveal: ["@key"] }`, // reveal — только builtin
	} {
		if _, err := manifest.Parse([]byte("connector: x\n" + bad)); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
}
