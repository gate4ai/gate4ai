// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
)

func setup(t *testing.T) (*hosttest.Host, connector.Connector, string) {
	home := t.TempDir()
	notes := filepath.Join(home, "Documents", "obsidian")
	os.MkdirAll(filepath.Join(notes, "sub"), 0o755)
	os.WriteFile(filepath.Join(notes, "a.md"), []byte("# A\n"), 0o644)
	os.WriteFile(filepath.Join(home, "secret.txt"), []byte("top secret"), 0o600)
	os.Symlink(filepath.Join(home, "secret.txt"), filepath.Join(notes, "link.md"))
	os.Symlink(home, filepath.Join(notes, "escape"))
	m, c, err := Manifest(), connector.Connector(Files{}), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := hosttest.New(m, map[string]any{"roots": []any{"~/Documents/obsidian"}}, nil)
	h.Home = home
	return h, c, home
}

func TestReadListAppendWrite(t *testing.T) {
	h, c, home := setup(t)
	res, err := h.Execute(c, "read", map[string]any{"path": "~/Documents/obsidian/a.md"})
	if err != nil || res.Content[0].Type != abi.ContentText || res.Content[0].Text != "# A\n" || res.Structured.(map[string]any)["size"] != 4.0 {
		t.Fatal(res, err)
	}
	// Изображение — блок image, прочие двоичные файлы — двоичный resource.
	dir := filepath.Join(home, "Documents/obsidian")
	os.WriteFile(filepath.Join(dir, "p.png"), []byte("\x89PNG\x00\x01"), 0o644)
	os.WriteFile(filepath.Join(dir, "d.bin"), []byte{0, 1, 2}, 0o644)
	res, err = h.Execute(c, "read", map[string]any{"path": "~/Documents/obsidian/p.png"})
	if err != nil || res.Content[0].Type != abi.ContentImage || res.Content[0].MIMEType != "image/png" {
		t.Fatal(res, err)
	}
	res, err = h.Execute(c, "read", map[string]any{"path": "~/Documents/obsidian/d.bin"})
	if err != nil || res.Content[0].Type != abi.ContentResource || string(res.Content[0].Data) != "\x00\x01\x02" || !strings.HasPrefix(res.Content[0].URI, "file://") {
		t.Fatal(res, err)
	}
	os.Remove(filepath.Join(dir, "p.png"))
	os.Remove(filepath.Join(dir, "d.bin"))
	res, err = h.Execute(c, "list", map[string]any{"path": "~/Documents/obsidian"})
	if err != nil || len(res.Structured.(map[string]any)["entries"].([]any)) != 4 {
		t.Fatal(res, err)
	}
	args := map[string]any{"path": "~/Documents/obsidian/a.md", "text": "- item"}
	res, err = h.Execute(c, "append", args)
	if err != nil || len(res.Evidence) != 1 {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, "Documents/obsidian/a.md"))
	if string(b) != "# A\n- item" {
		t.Fatal(string(b))
	}
	if v, _ := h.Verify(c, "append", args, res); v.Status != abi.Verified {
		t.Fatal(v)
	}
	// append создаёт новый файл в разрешённой папке.
	if _, err := h.Execute(c, "append", map[string]any{"path": "~/Documents/obsidian/sub/new.md", "text": "x"}); err != nil {
		t.Fatal(err)
	}
	wargs := map[string]any{"path": "~/Documents/obsidian/b.md", "content": "new file"}
	res, err = h.Execute(c, "write", wargs)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := h.Verify(c, "write", wargs, res); v.Status != abi.Verified {
		t.Fatal(v)
	}
	os.WriteFile(filepath.Join(home, "Documents/obsidian/b.md"), []byte("tampered"), 0o644)
	if v, _ := h.Verify(c, "write", wargs, res); v.Status != abi.NotConfirmed {
		t.Fatal("tamper not detected")
	}
}

func TestEscapes(t *testing.T) {
	h, c, home := setup(t)
	bad := []string{
		"~/secret.txt",
		"~/Documents/obsidian/../../secret.txt",
		"~/Documents/obsidian/link.md",
		"~/Documents/obsidian/escape/secret.txt",
		"Documents/obsidian/a.md",
		filepath.Join(home, "Documents", "obsidian2", "x"),
		"/etc/passwd",
	}
	for _, p := range bad {
		for _, action := range []string{"read", "append"} {
			args := map[string]any{"path": p}
			if action == "append" {
				args["text"] = "pwned"
			}
			_, err := h.Execute(c, action, args)
			if err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
				t.Errorf("%s %s: %v", action, p, err)
			}
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, "secret.txt")); string(b) != "top secret" {
		t.Fatal("secret modified")
	}
	h.Settings = map[string]any{}
	if _, err := h.Execute(c, "read", map[string]any{"path": "~/x"}); err == nil || !strings.Contains(err.Error(), "roots") {
		t.Fatal("no roots must deny everything", err)
	}
	// Обрезанный по max_bytes двоичный файл не отдаётся.
	h.Settings = map[string]any{"roots": []any{"~/Documents/obsidian"}, "max_bytes": 2.0}
	os.WriteFile(filepath.Join(home, "Documents/obsidian/bin.dat"), []byte{0, 1, 2}, 0o644)
	if _, err := h.Execute(c, "read", map[string]any{"path": "~/Documents/obsidian/bin.dat"}); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatal("truncated binary read", err)
	}
}

func TestManifest(t *testing.T) {
	m := Manifest()
	if w, _ := m.Find("write"); w.Class != "write" {
		t.Fatal("write must default to ask")
	}
	if m.Runtime != "builtin" || len(m.Capabilities.Files) != 1 || m.Capabilities.Network != nil {
		t.Fatal("files connector must have only file capabilities", m.Capabilities)
	}
}

// Закрытые пути: скрытое ниже корня, ключи, каталоги секретов — даже при корне ~.
func TestProtected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	mk := func(rel, data string) {
		p := filepath.Join(home, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(data), 0o600)
	}
	mk(".ssh/id_ed25519", "PRIVATE")
	mk(".aws/credentials", "AWS")
	mk(".claude.json", "{}")
	mk(".config/app/conf.toml", "x=1")
	mk("Documents/notes.md", "notes")
	mk("Documents/.git/config", "git")
	mk("Documents/server.pem", "PEM")
	mk("Documents/backup/id_rsa.pub", "PUB")
	mk("Documents/Vault.KDBX", "KDBX")
	mk("Library/Keychains/login.keychain-db", "KC")
	mk("Library/Keychains/notes.txt", "KC")
	mk("Library/Application Support/Google/Chrome/Default/Cookies", "C")
	os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "Documents", "keys"))
	m, c := Manifest(), connector.Connector(Files{})
	h := hosttest.New(m, map[string]any{"roots": []any{"~"}}, nil)
	h.Home = home
	denied := func(p string, actions ...string) {
		t.Helper()
		for _, action := range actions {
			args := map[string]any{"path": p}
			switch action {
			case "append":
				args["text"] = "x"
			case "write":
				args["content"] = "x"
			}
			_, err := h.Execute(c, action, args)
			if err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) || !strings.Contains(err.Error(), "protected") {
				t.Errorf("%s %s: %v", action, p, err)
			}
		}
	}
	allowed := func(p string) {
		t.Helper()
		if _, err := h.Execute(c, "read", map[string]any{"path": p}); err != nil {
			t.Errorf("read %s: %v", p, err)
		}
	}
	all := []string{"read", "append", "write"}
	denied("~/.ssh/id_ed25519", all...)
	denied("~/.ssh", "list")
	denied("~/.aws/credentials", all...)
	denied("~/.claude.json", all...)
	denied("~/.ssh/new_key", "write") // новый файл в скрытой папке
	denied("~/Documents/.git/config", "read")
	denied("~/Documents/server.pem", all...)
	denied("~/Documents/backup/id_rsa.pub", "read")
	denied("~/Documents/Vault.KDBX", "read")
	denied("~/Library/Keychains/login.keychain-db", "read")
	denied("~/Library/Keychains/notes.txt", "read")
	denied("~/Library/Keychains", "list")
	denied("~/Library/Application Support/Google/Chrome/Default/Cookies", "read")
	denied("~/Documents/keys/id_ed25519", "read") // симлинк в ~/.ssh
	allowed("~/Documents/notes.md")
	if b, _ := os.ReadFile(filepath.Join(home, ".ssh/id_ed25519")); string(b) != "PRIVATE" {
		t.Fatal("key modified")
	}

	// Список не падает, а молча пропускает закрытое.
	names := func(p string) []string {
		t.Helper()
		res, err := h.Execute(c, "list", map[string]any{"path": p})
		if err != nil {
			t.Fatal(p, err)
		}
		var out []string
		for _, e := range res.Structured.(map[string]any)["entries"].([]any) {
			out = append(out, e.(map[string]any)["name"].(string))
		}
		return out
	}
	if got := strings.Join(names("~"), ","); got != "Documents,Library" {
		t.Fatal(got)
	}
	if got := strings.Join(names("~/Documents"), ","); got != "backup,keys,notes.md" {
		t.Fatal(got)
	}
	if got := strings.Join(names("~/Library"), ","); got != "Application Support" {
		t.Fatal(got)
	}

	// Скрытый корень, заданный владельцем, открыт; скрытое ниже него — нет.
	h.Settings = map[string]any{"roots": []any{"~/.config/app"}}
	allowed("~/.config/app/conf.toml")
	mk(".config/app/.cache/x", "x")
	denied("~/.config/app/.cache/x", "read")

	// allow_hidden открывает только скрытое: ключи и каталоги секретов закрыты.
	h.Settings = map[string]any{"roots": []any{"~"}, "allow_hidden": true}
	allowed("~/.aws/credentials")
	allowed("~/Documents/.git/config")
	denied("~/.ssh/id_ed25519", "read")
	denied("~/Library/Keychains/notes.txt", "read")
	if got := strings.Join(names("~"), ","); got != ".aws,.claude.json,.config,.ssh,Documents,Library" {
		t.Fatal(got)
	}

	// allow_paths открывает путь или папку целиком, остальное — по-прежнему.
	h.Settings = map[string]any{"roots": []any{"~"}, "allow_paths": []any{"~/.ssh/id_ed25519", "~/Library/Keychains"}}
	allowed("~/.ssh/id_ed25519")
	allowed("~/Library/Keychains/login.keychain-db")
	denied("~/.aws/credentials", "read")
	denied("~/Documents/server.pem", "read")
	if got := strings.Join(names("~/Library/Keychains"), ","); got != "login.keychain-db,notes.txt" {
		t.Fatal(got)
	}
}

// Дописывание в файл больше предела fs_read (16 МиБ) подтверждается по хвосту,
// а не даёт ложное not_confirmed.
func TestAppendToLargeFileVerified(t *testing.T) {
	h, c, home := setup(t)
	big := filepath.Join(home, "Documents", "obsidian", "big.log")
	if err := os.WriteFile(big, make([]byte, 17<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"path": "~/Documents/obsidian/big.log", "text": "tail line"}
	res, err := h.Execute(c, "append", args)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := h.Verify(c, "append", args, res); err != nil || v.Status != abi.Verified {
		t.Fatal(v, err)
	}
	args["text"] = "other line"
	if v, _ := h.Verify(c, "append", args, res); v.Status != abi.NotConfirmed {
		t.Fatal("wrong tail confirmed:", v)
	}
}
