// SPDX-License-Identifier: AGPL-3.0-only

// Package agentsetup — подключение локальных агентов к gate4ai из Web UI:
// где лежит конфигурация MCP каждого клиента, ручная инструкция и запись
// сервера `gate4ai` (command — бинарник gate4ai, args — [mcp], env —
// GATE4AI_TOKEN) в его файл с резервной копией и сохранением прочих настроек.
package agentsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ServerName — имя сервера gate4ai в конфигурации клиентов.
const ServerName = "gate4ai"

// Форматы конфигурации.
const (
	FormatJSON   = "json"   // {"mcpServers": {name: {command, args, env}}}
	FormatVSCode = "vscode" // {"servers": {name: {type: "stdio", command, args, env}}}
	FormatTOML   = "toml"   // [mcp_servers.name] command, args, env (Codex)
)

// Client — поддерживаемый агент.
type Client struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Agent   string `json:"agent"` // имя агента по умолчанию
	Format  string `json:"format"`
	Restart string `json:"restart"` // что сделать после подключения
	Docs    string `json:"docs,omitempty"`
	Skill   bool   `json:"skill,omitempty"` // клиент понимает SKILL.md (Claude Code)
}

// Clients — клиенты с автоподключением, в порядке показа.
var Clients = []Client{
	{ID: "claude-code", Title: "Claude Code", Agent: "claude-code", Format: FormatJSON, Skill: true,
		Restart: "Перезапустите Claude Code (или выполните /mcp в открытой сессии).", Docs: "https://code.claude.com/docs/en/mcp"},
	{ID: "claude-desktop", Title: "Claude Desktop", Agent: "claude-desktop", Format: FormatJSON,
		Restart: "Полностью закройте Claude Desktop (из трея / меню) и откройте снова.", Docs: "https://modelcontextprotocol.io/quickstart/user"},
	{ID: "cursor", Title: "Cursor", Agent: "cursor", Format: FormatJSON,
		Restart: "Cursor подхватит сервер сам; если нет — Settings → MCP → обновить.", Docs: "https://docs.cursor.com/context/mcp"},
	{ID: "vscode", Title: "VS Code / Copilot", Agent: "vscode", Format: FormatVSCode,
		Restart: "В VS Code: Command Palette → «MCP: List Servers» → gate4ai → Start; в чате Copilot выберите режим Agent.", Docs: "https://code.visualstudio.com/docs/copilot/chat/mcp-servers"},
	{ID: "codex", Title: "Codex CLI", Agent: "codex", Format: FormatTOML,
		Restart: "Запустите codex заново — сервер появится в /mcp.", Docs: "https://github.com/openai/codex/blob/main/docs/config.md"},
	{ID: "gemini", Title: "Gemini CLI", Agent: "gemini", Format: FormatJSON,
		Restart: "Запустите gemini заново — сервер появится в /mcp.", Docs: "https://github.com/google-gemini/gemini-cli/blob/main/docs/tools/mcp-server.md"},
}

// Find — клиент по id.
func Find(id string) (Client, bool) {
	for _, c := range Clients {
		if c.ID == id {
			return c, true
		}
	}
	return Client{}, false
}

// Env — окружение пользователя (подменяется в тестах).
type Env struct {
	Home     string
	GOOS     string
	Getenv   func(string) string
	LookPath func(string) (string, error)
}

// Default — окружение текущего пользователя.
func Default() Env {
	h, _ := os.UserHomeDir()
	return Env{Home: h, GOOS: runtime.GOOS, Getenv: os.Getenv, LookPath: exec.LookPath}
}

func (e Env) appData() string {
	if v := e.Getenv("APPDATA"); v != "" {
		return v
	}
	return filepath.Join(e.Home, "AppData", "Roaming")
}

func (e Env) xdgConfig() string {
	if v := e.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(e.Home, ".config")
}

// ConfigPath — файл конфигурации MCP клиента (уровень пользователя).
func (e Env) ConfigPath(id string) string {
	switch id {
	case "claude-code":
		if d := e.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			return filepath.Join(d, ".claude.json")
		}
		return filepath.Join(e.Home, ".claude.json")
	case "claude-desktop":
		switch e.GOOS {
		case "darwin":
			return filepath.Join(e.Home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
		case "windows":
			return filepath.Join(e.appData(), "Claude", "claude_desktop_config.json")
		}
		return filepath.Join(e.xdgConfig(), "Claude", "claude_desktop_config.json")
	case "cursor":
		return filepath.Join(e.Home, ".cursor", "mcp.json")
	case "vscode":
		switch e.GOOS {
		case "darwin":
			return filepath.Join(e.Home, "Library", "Application Support", "Code", "User", "mcp.json")
		case "windows":
			return filepath.Join(e.appData(), "Code", "User", "mcp.json")
		}
		return filepath.Join(e.xdgConfig(), "Code", "User", "mcp.json")
	case "codex":
		if d := e.Getenv("CODEX_HOME"); d != "" {
			return filepath.Join(d, "config.toml")
		}
		return filepath.Join(e.Home, ".codex", "config.toml")
	case "gemini":
		return filepath.Join(e.Home, ".gemini", "settings.json")
	}
	return ""
}

// binaries — исполняемые файлы, по которым видно, что клиент установлен.
var binaries = map[string][]string{
	"claude-code": {"claude"}, "cursor": {"cursor"}, "vscode": {"code"}, "codex": {"codex"}, "gemini": {"gemini"},
}

// Installed — клиент, похоже, установлен (есть каталог конфигурации или бинарник).
func (e Env) Installed(id string) bool {
	p := e.ConfigPath(id)
	if p == "" {
		return false
	}
	if _, err := os.Stat(filepath.Dir(p)); err == nil && id != "claude-code" {
		return true
	}
	if _, err := os.Stat(p); err == nil {
		return true
	}
	for _, b := range binaries[id] {
		if e.LookPath != nil {
			if _, err := e.LookPath(b); err == nil {
				return true
			}
		}
	}
	return false
}

// Connected — в конфигурации клиента уже есть сервер gate4ai.
func (e Env) Connected(id string) bool {
	c, ok := Find(id)
	if !ok {
		return false
	}
	b, err := os.ReadFile(e.ConfigPath(id))
	if err != nil {
		return false
	}
	if c.Format == FormatTOML {
		return tomlSection.Match(b)
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	servers, _ := m[serversKey(c.Format)].(map[string]any)
	_, ok = servers[ServerName]
	return ok
}

func serversKey(format string) string {
	if format == FormatVSCode {
		return "servers"
	}
	return "mcpServers"
}

// entry — описание сервера gate4ai для клиента.
func entry(format, bin, token string) map[string]any {
	e := map[string]any{"command": bin, "args": []string{"mcp"}, "env": map[string]string{"GATE4AI_TOKEN": token}}
	if format == FormatVSCode {
		e["type"] = "stdio"
	}
	return e
}

// Snippet — ручная инструкция: файл и фрагмент для вставки.
func (e Env) Snippet(id, bin, token string) (path, text string) {
	c, _ := Find(id)
	if c.Format == FormatTOML {
		return e.ConfigPath(id), tomlBlock(bin, token)
	}
	return e.ConfigPath(id), strings.TrimSpace(string(marshal(map[string]any{serversKey(c.Format): map[string]any{ServerName: entry(c.Format, bin, token)}})))
}

// Result — что сделано с файлом клиента.
type Result struct {
	Path   string   `json:"path"`
	Backup string   `json:"backup,omitempty"`
	Notes  []string `json:"notes,omitempty"`
}

// Connect записывает сервер gate4ai в конфигурацию клиента.
func (e Env) Connect(id, bin, token, skillMD string) (Result, error) {
	c, ok := Find(id)
	if !ok {
		return Result{}, fmt.Errorf("unknown client %q", id)
	}
	if bin == "" || token == "" {
		return Result{}, errors.New("binary and token are required")
	}
	p := e.ConfigPath(id)
	res := Result{Path: p}
	old, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	var out []byte
	if c.Format == FormatTOML {
		out = []byte(tomlSet(string(old), tomlBlock(bin, token)))
	} else {
		m := map[string]any{}
		if len(strings.TrimSpace(string(old))) > 0 {
			if err := json.Unmarshal(old, &m); err != nil {
				return res, fmt.Errorf("%s is not valid JSON (%v): fix it or add the server by hand", p, err)
			}
		}
		key := serversKey(c.Format)
		servers, _ := m[key].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers[ServerName] = entry(c.Format, bin, token)
		m[key] = servers
		out = marshal(m)
	}
	if res.Backup, err = backup(p, old); err != nil {
		return res, err
	}
	if err := writePrivate(p, out); err != nil {
		return res, err
	}
	if c.Skill && skillMD != "" {
		sp := filepath.Join(e.skillDir(), "SKILL.md")
		if err := writePrivate(sp, []byte(skillMD)); err == nil {
			res.Notes = append(res.Notes, "skill: "+sp)
		}
	}
	return res, nil
}

// Disconnect убирает сервер gate4ai из конфигурации клиента.
func (e Env) Disconnect(id string) (Result, error) {
	c, ok := Find(id)
	if !ok {
		return Result{}, fmt.Errorf("unknown client %q", id)
	}
	p := e.ConfigPath(id)
	res := Result{Path: p}
	old, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	} else if err != nil {
		return res, err
	}
	var out []byte
	if c.Format == FormatTOML {
		out = []byte(tomlSet(string(old), ""))
	} else {
		m := map[string]any{}
		if err := json.Unmarshal(old, &m); err != nil {
			return res, fmt.Errorf("%s is not valid JSON: %v", p, err)
		}
		if servers, _ := m[serversKey(c.Format)].(map[string]any); servers != nil {
			delete(servers, ServerName)
		}
		out = marshal(m)
	}
	if res.Backup, err = backup(p, old); err != nil {
		return res, err
	}
	if c.Skill {
		_ = os.RemoveAll(e.skillDir())
	}
	return res, writePrivate(p, out)
}

// skillDir — каталог skill gate4ai для Claude Code.
func (e Env) skillDir() string {
	if d := e.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "skills", ServerName)
	}
	return filepath.Join(e.Home, ".claude", "skills", ServerName)
}

// marshal — JSON с отступами без HTML-экранирования (< > & как есть).
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return b.Bytes()
}

// backup сохраняет прежнее содержимое рядом (<файл>.gate4ai.bak).
func backup(p string, old []byte) (string, error) {
	if old == nil {
		return "", nil
	}
	b := p + ".gate4ai.bak"
	return b, os.WriteFile(b, old, 0o600)
}

// writePrivate пишет файл атомарно; токен внутри — права 0600.
func writePrivate(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm() & 0o600
		if mode == 0 {
			mode = 0o600
		}
	}
	tmp := p + ".gate4ai.tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ---- TOML (Codex): секция [mcp_servers.gate4ai] ----

var tomlSection = regexp.MustCompile(`(?m)^\[mcp_servers\.` + ServerName + `\]\s*$`)

func tomlQuote(s string) string {
	b, _ := json.Marshal(s) // базовая строка TOML совместима с JSON-экранированием
	return string(b)
}

func tomlBlock(bin, token string) string {
	return "[mcp_servers." + ServerName + "]\ncommand = " + tomlQuote(bin) + "\nargs = [\"mcp\"]\nenv = { GATE4AI_TOKEN = " + tomlQuote(token) + " }\n"
}

// tomlSet заменяет секцию gate4ai (и её подтаблицы) на block ("" — удаляет).
func tomlSet(doc, block string) string {
	lines := strings.SplitAfter(doc, "\n")
	var out []string
	skip := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			skip = t == "[mcp_servers."+ServerName+"]" || strings.HasPrefix(t, "[mcp_servers."+ServerName+".")
		}
		if !skip {
			out = append(out, l)
		}
	}
	s := strings.Join(out, "")
	if block == "" {
		return s
	}
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if s != "" && !strings.HasSuffix(s, "\n\n") {
		s += "\n"
	}
	return s + block
}
