// SPDX-License-Identifier: FSL-1.1-ALv2

// Package sandbox — изоляция локальных агентов: `gate4ai run` в процессной
// песочнице Anthropic sandbox-runtime (srt), в контейнере Docker/Podman или в
// microVM через Docker Sandboxes; профиль песочницы генерируется из профиля
// агента; доступ к хосту — только через gate4ai (агентский сокет запуска и
// Egress Proxy); `gate4ai doctor` ищет обходные пути.
package sandbox

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Modes.
const (
	ModeSandbox   = "sandbox"   // уровень 1: sandbox-runtime (по умолчанию)
	ModeContainer = "container" // уровень 2: Docker / Podman
	ModeMicroVM   = "microvm"   // уровень 3: Docker Sandboxes
)

// Template — шаблон популярного агента.
type Template struct {
	Command []string
	Image   string // образ для контейнера
	// ClaudeMCP — агент читает .mcp.json и навыки из .claude/skills.
	ClaudeMCP bool
	// SandboxAgent — имя агента для `docker sandbox run`.
	SandboxAgent string
}

// Templates — шаблоны агентов.
var Templates = map[string]Template{
	"claude-code": {Command: []string{"claude"}, Image: "node:22-bookworm", ClaudeMCP: true, SandboxAgent: "claude"},
	"codex":       {Command: []string{"codex"}, Image: "node:22-bookworm", SandboxAgent: "codex"},
	"openclaw":    {Command: []string{"openclaw"}, Image: "node:22-bookworm"},
	"hermes":      {Command: []string{"hermes"}, Image: "python:3.12-bookworm"},
	"aider":       {Command: []string{"aider"}, Image: "python:3.12-bookworm"},
}

// TemplateFor — шаблон по имени агента («claude-code», «claude-code-work» → claude-code).
func TemplateFor(agent string) (Template, bool) {
	if t, ok := Templates[agent]; ok {
		return t, true
	}
	keys := make([]string, 0, len(Templates))
	for k := range Templates {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if strings.HasPrefix(agent, k) {
			return Templates[k], true
		}
	}
	return Template{}, false
}

// Run — параметры запуска, выданные daemon.
type Run struct {
	ID           string   `json:"id"`
	Agent        string   `json:"agent"`
	Token        string   `json:"token"`
	Socket       string   `json:"socket"`    // агентский сокет запуска на хосте
	ProxyURL     string   `json:"proxy_url"` // http://run-…:pass@127.0.0.1:port
	ProxyHost    string   `json:"proxy_host"`
	ProxyPort    int      `json:"proxy_port"`
	CAPEM        string   `json:"ca_pem"` // CA для доменов с подстановкой секретов
	AllowDomains []string `json:"allow_domains"`
	AskDomains   []string `json:"ask_domains"`
	SkillMD      string   `json:"skill_md"`
}

// Options — параметры CLI.
type Options struct {
	Mode      string
	Workspace string
	Command   []string // явная команда; иначе из шаблона
	Image     string
	Engine    string // docker | podman
	GateBin   string // путь к бинарнику gate4ai на хосте
	Home      string
	TmpDir    string // каталог для файлов запуска (настройки srt, CA)
}

// Spec — что запускать.
type Spec struct {
	Argv  []string          `json:"argv"`
	Env   []string          `json:"env"`
	Files map[string]string `json:"files"` // путь → содержимое (создать перед запуском)
}

// SensitivePaths — что закрыто для чтения в песочнице.
func SensitivePaths(home, goos string) []string {
	p := []string{"~/.ssh", "~/.gnupg", "~/.aws", "~/.config/gcloud", "~/.kube", "~/.docker", "~/.netrc",
		"~/.local/share/keyrings", "~/.mozilla", "~/.thunderbird"}
	switch goos {
	case "darwin":
		p = append(p, "~/Library/Keychains", "~/Library/Application Support/Google/Chrome", "~/Library/Application Support/Firefox",
			"~/Library/Mail", "~/Library/Cookies", "~/Library/Application Support/gate4ai")
	case "windows":
		p = append(p, "~/AppData/Local/Google/Chrome/User Data", "~/AppData/Roaming/Mozilla", "~/AppData/Local/gate4ai")
	default:
		p = append(p, "~/.config/google-chrome", "~/.config/chromium", "~/.local/state/gate4ai", "~/.gate4ai-chrome")
	}
	return p
}

// SRTSettings — настройки sandbox-runtime из профиля агента: запись только в
// рабочую папку, закрыто чтение секретов и профилей браузеров/почты, из
// unix-сокетов — только сокет gate4ai, сеть — только Egress Proxy gate4ai.
func SRTSettings(r Run, o Options) map[string]any {
	return map[string]any{
		"network": map[string]any{
			// Прямой выход — только к нашему прокси; домены решает Egress Proxy.
			"allowedDomains":    []string{"localhost"},
			"deniedDomains":     []string{},
			"allowUnixSockets":  []string{r.Socket},
			"allowLocalBinding": false,
			"httpProxyPort":     r.ProxyPort,
		},
		"filesystem": map[string]any{
			"denyRead":   SensitivePaths(o.Home, runtime.GOOS),
			"allowWrite": []string{o.Workspace},
			"denyWrite":  []string{},
		},
	}
}

func proxyEnv(proxy, caPath string) []string {
	return []string{"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "http_proxy=" + proxy, "https_proxy=" + proxy,
		"NO_PROXY=", "no_proxy=",
		"SSL_CERT_FILE=" + caPath, "NODE_EXTRA_CA_CERTS=" + caPath, "REQUESTS_CA_BUNDLE=" + caPath, "CURL_CA_BUNDLE=" + caPath}
}

// MCPConfig — .mcp.json для агентов, понимающих его (Claude Code).
func MCPConfig(gateBin string) string {
	b, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{
		"gate4ai": map[string]any{"command": gateBin, "args": []string{"mcp"}}}}, "", "  ")
	return string(b) + "\n"
}

// Build собирает команду запуска.
func Build(r Run, o Options) (Spec, error) {
	t, known := TemplateFor(r.Agent)
	cmd := o.Command
	if len(cmd) == 0 {
		if !known {
			return Spec{}, fmt.Errorf("no template for agent %q: pass the command after --", r.Agent)
		}
		cmd = t.Command
	}
	if o.Workspace == "" {
		return Spec{}, fmt.Errorf("--workspace is required")
	}
	files := map[string]string{}
	caHost := filepath.Join(o.TmpDir, "gate4ai-ca.pem")
	files[caHost] = r.CAPEM
	// Skill и MCP — в рабочей папке, чтобы агент их видел в любом режиме.
	files[filepath.Join(o.Workspace, ".gate4ai", "SKILL.md")] = r.SkillMD
	if t.ClaudeMCP {
		files[filepath.Join(o.Workspace, ".claude", "skills", "gate4ai", "SKILL.md")] = r.SkillMD
	}
	spec := Spec{Files: files}
	switch o.Mode {
	case "", ModeSandbox:
		settings := filepath.Join(o.TmpDir, "srt-settings.json")
		b, _ := json.MarshalIndent(SRTSettings(r, o), "", "  ")
		files[settings] = string(b)
		if t.ClaudeMCP {
			files[filepath.Join(o.Workspace, ".mcp.json")] = MCPConfig(o.GateBin)
		}
		spec.Argv = append([]string{"srt", "--settings", settings, "--"}, cmd...)
		spec.Env = append([]string{"GATE4AI_TOKEN=" + r.Token, "GATE4AI_AGENT_SOCKET=" + r.Socket, "GATE4AI_RUN_ID=" + r.ID}, proxyEnv(r.ProxyURL, caHost)...)
	case ModeContainer:
		engine := o.Engine
		if engine == "" {
			engine = "docker"
		}
		image := o.Image
		if image == "" {
			if image = t.Image; image == "" {
				return Spec{}, fmt.Errorf("--image is required for agent %q", r.Agent)
			}
		}
		if t.ClaudeMCP {
			files[filepath.Join(o.Workspace, ".mcp.json")] = MCPConfig("/usr/local/bin/gate4ai")
		}
		proxy := strings.Replace(r.ProxyURL, r.ProxyHost, "host.docker.internal", 1)
		argv := []string{engine, "run", "--rm", "-it", "--name", "gate4ai-" + r.ID,
			"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
			"--add-host", "host.docker.internal:host-gateway",
			"-v", o.Workspace + ":/workspace", "-w", "/workspace",
			"-v", r.Socket + ":/run/gate4ai.sock",
			"-v", o.GateBin + ":/usr/local/bin/gate4ai:ro",
			"-v", caHost + ":/etc/gate4ai/ca.pem:ro"}
		env := append([]string{"GATE4AI_TOKEN=" + r.Token, "GATE4AI_AGENT_SOCKET=/run/gate4ai.sock", "GATE4AI_RUN_ID=" + r.ID}, proxyEnv(proxy, "/etc/gate4ai/ca.pem")...)
		for _, e := range env {
			argv = append(argv, "-e", e)
		}
		spec.Argv = append(append(argv, image), cmd...)
		spec.Env = []string{"GATE4AI_TOKEN=" + r.Token} // для маскирования в --dry-run
	case ModeMicroVM:
		if !known || t.SandboxAgent == "" {
			return Spec{}, fmt.Errorf("Docker Sandboxes support only: claude-code, codex")
		}
		proxy := strings.Replace(r.ProxyURL, r.ProxyHost, "host.docker.internal", 1)
		argv := []string{"docker", "sandbox", "run", "--workspace", o.Workspace,
			"-e", "GATE4AI_TOKEN=" + r.Token, "-e", "GATE4AI_RUN_ID=" + r.ID,
			"-e", "HTTPS_PROXY=" + proxy, "-e", "HTTP_PROXY=" + proxy}
		spec.Argv = append(argv, t.SandboxAgent)
		spec.Env = []string{"GATE4AI_TOKEN=" + r.Token}
	default:
		return Spec{}, fmt.Errorf("unknown mode %q", o.Mode)
	}
	return spec, nil
}
