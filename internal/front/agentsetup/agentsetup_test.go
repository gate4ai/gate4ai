// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(t *testing.T, goos string) Env {
	home := t.TempDir()
	return Env{Home: home, GOOS: goos, Getenv: func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", errors.New("no") }}
}

func TestConnectJSONKeepsOtherSettings(t *testing.T) {
	e := testEnv(t, "linux")
	p := e.ConfigPath("cursor")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(`{"mcpServers":{"other":{"command":"x"}},"theme":"dark"}`), 0o644)
	if !e.Installed("cursor") || e.Connected("cursor") {
		t.Fatal("state before")
	}
	r, err := e.Connect("cursor", "/usr/bin/gate4ai", "cursor", "")
	if err != nil || r.Backup == "" {
		t.Fatal(r, err)
	}
	var m map[string]any
	b, _ := os.ReadFile(p)
	_ = json.Unmarshal(b, &m)
	s := m["mcpServers"].(map[string]any)
	g := s["gate4ai"].(map[string]any)
	if m["theme"] != "dark" || s["other"] == nil || g["command"] != "/usr/bin/gate4ai" || g["env"].(map[string]any)["GATE4AI_AGENT"] != "cursor" || strings.Contains(string(b), "TOKEN") {
		t.Fatal(string(b))
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o077 != 0 {
		t.Fatal("config readable by others", fi.Mode())
	}
	if !e.Connected("cursor") {
		t.Fatal("connected")
	}
	if _, err := e.Disconnect("cursor"); err != nil || e.Connected("cursor") {
		t.Fatal("disconnect", err)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "other") {
		t.Fatal(string(b))
	}
	// Повреждённый файл не перезаписывается.
	_ = os.WriteFile(p, []byte(`{broken`), 0o600)
	if _, err := e.Connect("cursor", "/b", "t", ""); err == nil {
		t.Fatal("broken JSON overwritten")
	}
}

func TestConnectVSCodeAndClaudeCodeSkill(t *testing.T) {
	e := testEnv(t, "darwin")
	if _, err := e.Connect("vscode", "/b/gate4ai", "t1", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(e.Home, "Library/Application Support/Code/User/mcp.json"))
	if !strings.Contains(string(b), `"servers"`) || !strings.Contains(string(b), `"type": "stdio"`) {
		t.Fatal(string(b))
	}
	// Настройки Claude Code: свои ключи сохраняются, добавляется правило
	// разрешения команд gate4ai, а токен прежней схемы в env убирается (ключ
	// агента CLI находит сам).
	sp := filepath.Join(e.Home, ".claude/settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o700)
	_ = os.WriteFile(sp, []byte(`{"model": "opus", "permissions": {"allow": ["Bash(ls *)"]}, "env": {"FOO": "1", "GATE4AI_TOKEN": "old"}}`), 0o600)
	r, err := e.Connect("claude-code", "/b/gate4ai", "claude-code", "---\nname: gate4ai\n---\n")
	if err != nil || len(r.Notes) != 2 {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".claude/skills/gate4ai/SKILL.md")); err != nil {
		t.Fatal("skill", err)
	}
	var st struct {
		Model       string `json:"model"`
		Env         map[string]string
		Permissions struct{ Allow []string }
	}
	b, _ = os.ReadFile(sp)
	if err := json.Unmarshal(b, &st); err != nil || st.Model != "opus" || st.Env["FOO"] != "1" || st.Env["GATE4AI_TOKEN"] != "" ||
		strings.Join(st.Permissions.Allow, ",") != "Bash(ls *),"+BashRule {
		t.Fatal(string(b), err)
	}
	e.Connect("claude-code", "/b/gate4ai", "claude-code", "") // повторно: правило одно
	b, _ = os.ReadFile(sp)
	if strings.Count(string(b), BashRule) != 1 || strings.Contains(string(b), "GATE4AI_TOKEN") {
		t.Fatal(string(b))
	}
	if _, err := e.Disconnect("claude-code"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(sp)
	if strings.Contains(string(b), "GATE4AI_TOKEN") || strings.Contains(string(b), BashRule) || !strings.Contains(string(b), "Bash(ls *)") {
		t.Fatal("disconnect removes the token and the rule only:", string(b))
	}
	if p := e.ConfigPath("claude-desktop"); !strings.HasSuffix(p, "Library/Application Support/Claude/claude_desktop_config.json") {
		t.Fatal(p)
	}
	w := testEnv(t, "windows")
	if p := w.ConfigPath("claude-desktop"); !strings.Contains(p, filepath.Join("AppData", "Roaming", "Claude")) {
		t.Fatal(p)
	}
}

func TestConnectCodexTOML(t *testing.T) {
	e := testEnv(t, "linux")
	p := e.ConfigPath("codex")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("model = \"o4\"\n\n[mcp_servers.gate4ai]\ncommand = \"old\"\n\n[mcp_servers.gate4ai.env]\nX = \"1\"\n\n[mcp_servers.other]\ncommand = \"y\"\n"), 0o600)
	if _, err := e.Connect("codex", `C:\gate4ai.exe`, "co\"dex", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if strings.Count(s, "[mcp_servers.gate4ai]") != 1 || strings.Contains(s, "old") || strings.Contains(s, "gate4ai.env") ||
		!strings.Contains(s, "[mcp_servers.other]") || !strings.Contains(s, `model = "o4"`) ||
		!strings.Contains(s, `command = "C:\\gate4ai.exe"`) || !strings.Contains(s, `GATE4AI_AGENT = "co\"dex"`) {
		t.Fatal(s)
	}
	if !e.Connected("codex") {
		t.Fatal("connected")
	}
	_, _ = e.Disconnect("codex")
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "gate4ai") || !strings.Contains(string(b), "other") {
		t.Fatal(string(b))
	}
}

// Запись конфигурации клиента: числа не теряют точность, бэкап — исходный файл, временный
// файл не наследует права, другие формы записи TOML не дают дубликата.
func TestConnectReviewFixes(t *testing.T) {
	e := testEnv(t, "linux")
	p := e.ConfigPath("cursor")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	orig := `{"big":9007199254740993,"mcpServers":{}}`
	_ = os.WriteFile(p, []byte(orig), 0o600)
	// Чужой остаток временного файла с широкими правами не мешает.
	_ = os.WriteFile(p+".gate4ai.tmp", []byte("x"), 0o666)
	r, err := e.Connect("cursor", "/b", "t1", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "9007199254740993") {
		t.Fatal("number precision lost:", string(b))
	}
	if _, err := e.Connect("cursor", "/b", "t2", ""); err != nil {
		t.Fatal(err)
	}
	if bak, _ := os.ReadFile(r.Backup); string(bak) != orig {
		t.Fatal("backup overwritten:", string(bak))
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	for _, doc := range []string{
		"model = \"o3\"\nmcp_servers.gate4ai = { command = \"/old\" }\n\n[other]\nx = 1\n",
		"[mcp_servers]\ngate4ai = { command = \"/old\" }\nkeep = { command = \"k\" }\n",
		"[mcp_servers]\ngate4ai.command = \"/old\"\n",
	} {
		got := tomlSet(doc, tomlBlock("/new", "tok"))
		if strings.Contains(got, "/old") || strings.Count(got, "gate4ai") != 1 {
			t.Fatalf("%q → %q", doc, got)
		}
		if strings.Contains(doc, "keep") && !strings.Contains(got, "keep = ") {
			t.Fatal("other server removed:", got)
		}
		if strings.Contains(doc, "[other]") && !strings.Contains(got, "x = 1") {
			t.Fatal(got)
		}
	}
}

// Установленный навык прежней версии (полный каталог) заменяется обёрткой;
// совпадающий, чужой и отсутствующий файлы не трогаются.
func TestRefreshSkill(t *testing.T) {
	e := testEnv(t, "linux")
	stub := "---\nname: gate4ai\n---\nrun gate4ai skill\n"
	if ok, err := e.RefreshSkill(stub); ok || err != nil {
		t.Fatal("no file", ok, err)
	}
	p := e.SkillPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte("---\nname: gate4ai\ndescription: old\n---\n# old catalog: read, screenshot, extract\n"), 0o600)
	if ok, err := e.RefreshSkill(stub); !ok || err != nil {
		t.Fatal("old skill", ok, err)
	}
	if b, _ := os.ReadFile(p); string(b) != stub {
		t.Fatal(string(b))
	}
	if ok, _ := e.RefreshSkill(stub); ok {
		t.Fatal("already current")
	}
	_ = os.WriteFile(p, []byte("---\nname: mine\n---\n"), 0o600)
	if ok, _ := e.RefreshSkill(stub); ok {
		t.Fatal("not ours")
	}
}
