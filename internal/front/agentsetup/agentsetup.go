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
		Restart: "Restart Claude Code (or run /mcp in an open session).", Docs: "https://code.claude.com/docs/en/mcp"},
	{ID: "claude-desktop", Title: "Claude Desktop", Agent: "claude-desktop", Format: FormatJSON,
		Restart: "Quit Claude Desktop completely (from the tray / menu) and open it again.", Docs: "https://modelcontextprotocol.io/quickstart/user"},
	{ID: "cursor", Title: "Cursor", Agent: "cursor", Format: FormatJSON,
		Restart: "Cursor picks the server up itself; if not — Settings → MCP → refresh.", Docs: "https://docs.cursor.com/context/mcp"},
	{ID: "vscode", Title: "VS Code / Copilot", Agent: "vscode", Format: FormatVSCode,
		Restart: "In VS Code: Command Palette → “MCP: List Servers” → gate4ai → Start; in Copilot chat choose the Agent mode.", Docs: "https://code.visualstudio.com/docs/copilot/chat/mcp-servers"},
	{ID: "codex", Title: "Codex CLI", Agent: "codex", Format: FormatTOML,
		Restart: "Start codex again — the server shows up in /mcp.", Docs: "https://github.com/openai/codex/blob/main/docs/config.md"},
	{ID: "gemini", Title: "Gemini CLI", Agent: "gemini", Format: FormatJSON,
		Restart: "Start gemini again — the server shows up in /mcp.", Docs: "https://github.com/google-gemini/gemini-cli/blob/main/docs/tools/mcp-server.md"},
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
		return tomlSection.Match(b) || tomlSet(string(b), "") != string(b)
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
	var err error
	res.Backup, err = rewrite(p, func(old []byte) ([]byte, error) {
		if c.Format == FormatTOML {
			return []byte(tomlSet(string(old), tomlBlock(bin, token))), nil
		}
		m := map[string]any{}
		if len(strings.TrimSpace(string(old))) > 0 {
			if err := decodeJSON(old, &m); err != nil {
				return nil, fmt.Errorf("%s is not valid JSON (%v): fix it or add the server by hand", p, err)
			}
		}
		key := serversKey(c.Format)
		servers, _ := m[key].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers[ServerName] = entry(c.Format, bin, token)
		m[key] = servers
		return marshal(m), nil
	})
	if err != nil {
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
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	var err error
	res.Backup, err = rewrite(p, func(old []byte) ([]byte, error) {
		if c.Format == FormatTOML {
			return []byte(tomlSet(string(old), "")), nil
		}
		m := map[string]any{}
		if err := decodeJSON(old, &m); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %v", p, err)
		}
		if servers, _ := m[serversKey(c.Format)].(map[string]any); servers != nil {
			delete(servers, ServerName)
		}
		return marshal(m), nil
	})
	if err != nil {
		return res, err
	}
	if c.Skill {
		_ = os.RemoveAll(e.skillDir())
	}
	return res, nil
}

// decodeJSON — разбор конфигурации клиента без потери точности чисел
// (целые больше 2^53 остаются как есть).
func decodeJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

// rewrite — read-modify-write файла клиента. Клиент (Claude Code —
// ~/.claude.json) переписывает свой файл сам: если файл изменился, пока мы
// готовили новую версию, правка повторяется по свежему содержимому, чтобы не
// откатить изменения клиента. Возвращает путь резервной копии.
func rewrite(p string, f func(old []byte) ([]byte, error)) (string, error) {
	for try := 0; ; try++ {
		old, err := os.ReadFile(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		out, err := f(old)
		if err != nil {
			return "", err
		}
		if now, _ := os.ReadFile(p); !bytes.Equal(now, old) && try < 3 {
			continue
		}
		bak, err := backup(p, old)
		if err != nil {
			return "", err
		}
		return bak, writePrivate(p, out)
	}
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

// backup сохраняет исходное содержимое рядом (<файл>.gate4ai.bak) один раз:
// уже существующую копию не затирает, иначе после второго подключения
// исходный файл был бы потерян.
func backup(p string, old []byte) (string, error) {
	if old == nil {
		return "", nil
	}
	b := p + ".gate4ai.bak"
	f, err := os.OpenFile(b, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return b, nil
	}
	if err != nil {
		return "", err
	}
	_, err = f.Write(old)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return b, err
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
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".gate4ai-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(mode) // CreateTemp даёт 0600; режим прежнего файла — не шире
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
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

// Ключ gate4ai вне секции [mcp_servers.gate4ai]: в таблице [mcp_servers]
// (gate4ai = {…}, gate4ai.command = …) и в корне документа
// (mcp_servers.gate4ai = {…}). Такие строки tomlSet тоже убирает — иначе
// новая секция дала бы дубликат ключа.
var (
	tomlKeyInServers = regexp.MustCompile(`^\s*(` + ServerName + `|"` + ServerName + `"|'` + ServerName + `')\s*[=.]`)
	tomlKeyInRoot    = regexp.MustCompile(`^\s*(mcp_servers|"mcp_servers")\s*\.\s*(` + ServerName + `|"` + ServerName + `"|'` + ServerName + `')\s*[=.]`)
	tomlHeader       = regexp.MustCompile(`^\[\s*([^\[\]]*?)\s*\]\s*(#.*)?$`)
)

// tomlSet заменяет секцию gate4ai (и её подтаблицы, и ключ gate4ai в других
// формах записи) на block ("" — удаляет).
func tomlSet(doc, block string) string {
	lines := strings.SplitAfter(doc, "\n")
	var out []string
	skip := false
	table := "" // текущая таблица ("" — корень документа)
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if m := tomlHeader.FindStringSubmatch(t); m != nil {
			table = strings.ReplaceAll(strings.ReplaceAll(m[1], " ", ""), `"`, "")
			skip = table == "mcp_servers."+ServerName || strings.HasPrefix(table, "mcp_servers."+ServerName+".")
		} else if strings.HasPrefix(t, "[[") {
			table, skip = "", false
		}
		if skip {
			continue
		}
		if (table == "mcp_servers" && tomlKeyInServers.MatchString(l)) || (table == "" && tomlKeyInRoot.MatchString(l)) {
			continue
		}
		out = append(out, l)
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
