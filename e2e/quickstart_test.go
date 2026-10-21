// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// quickstartYAML — первый блок ```yaml из docs/quickstart.md ядра.
func quickstartYAML(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(src(t, "gate4ai"), "docs", "quickstart.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "```yaml\n")
	if i < 0 {
		t.Fatal("docs/quickstart.md: no yaml block")
	}
	j := strings.Index(s[i+8:], "```")
	return s[i+8 : i+8+j]
}

// Пример конфигурации из docs/quickstart.md принимается daemon с официальными
// коннекторами (files встроен в ядро), и правило из него работает: files.append
// в разрешённую папку проходит с проверкой, чтение вне корней запрещено.
func TestQuickstartFlow(t *testing.T) {
	t.Parallel()
	g := startGate(t)
	for _, c := range []string{"mail", "browser"} {
		g.install(c)
	}
	cfgFile := filepath.Join(t.TempDir(), "quickstart.yaml")
	os.WriteFile(cfgFile, []byte(quickstartYAML(t)), 0o600)
	if r := g.run("", "", "policy", "set", cfgFile, "-m", "docs/quickstart.md"); r.code != 0 {
		t.Fatalf("quickstart config rejected: %d %s", r.code, r.out)
	}

	notes := filepath.Join(g.user, "Documents", "obsidian")
	os.MkdirAll(notes, 0o755)
	token := g.run("", "", "agents", "token", "claude-ai").json(t)["token"].(string)
	r := g.run(token, "", "files", "append", "--path", "~/Documents/obsidian/log.md", "--text", "- done")
	if res := r.json(t); r.code != 0 || res["status"] != "ok" || res["verified"] != true {
		t.Fatalf("files.append: %d %s", r.code, r.out)
	}
	if b, _ := os.ReadFile(filepath.Join(notes, "log.md")); string(b) != "- done" {
		t.Fatalf("file content %q", b)
	}
	if r := g.run(token, "", "files", "read", "--path", "/etc/passwd"); r.code == 0 || strings.Contains(r.out, "root:") {
		t.Fatalf("read outside roots: %d %s", r.code, r.out)
	}
}
