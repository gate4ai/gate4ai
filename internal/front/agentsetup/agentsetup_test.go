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
	r, err := e.Connect("cursor", "/usr/bin/gate4ai", "g4a_tok", "")
	if err != nil || r.Backup == "" {
		t.Fatal(r, err)
	}
	var m map[string]any
	b, _ := os.ReadFile(p)
	_ = json.Unmarshal(b, &m)
	s := m["mcpServers"].(map[string]any)
	g := s["gate4ai"].(map[string]any)
	if m["theme"] != "dark" || s["other"] == nil || g["command"] != "/usr/bin/gate4ai" || g["env"].(map[string]any)["GATE4AI_TOKEN"] != "g4a_tok" {
		t.Fatal(string(b))
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o077 != 0 {
		t.Fatal("token file readable by others", fi.Mode())
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
	r, err := e.Connect("claude-code", "/b/gate4ai", "t2", "---\nname: gate4ai\n---\n")
	if err != nil || len(r.Notes) != 1 {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".claude/skills/gate4ai/SKILL.md")); err != nil {
		t.Fatal("skill", err)
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
	if _, err := e.Connect("codex", `C:\gate4ai.exe`, "t\"3", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if strings.Count(s, "[mcp_servers.gate4ai]") != 1 || strings.Contains(s, "old") || strings.Contains(s, "gate4ai.env") ||
		!strings.Contains(s, "[mcp_servers.other]") || !strings.Contains(s, `model = "o4"`) ||
		!strings.Contains(s, `command = "C:\\gate4ai.exe"`) || !strings.Contains(s, `GATE4AI_TOKEN = "t\"3"`) {
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
