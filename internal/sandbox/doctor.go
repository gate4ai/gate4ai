// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Check — результат одной проверки doctor.
type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// DoctorEnv — окружение проверок (подменяется в тестах).
type DoctorEnv struct {
	Home     string
	GOOS     string
	Environ  []string
	Readable func(path string) bool
	Writable func(path string) bool
	Exists   func(path string) bool
	LookPath func(string) (string, error)
	Daemon   func() error
	// ClaudeSettings — настройки Claude Code (~/.claude/settings.json), "" — не проверять.
	ClaudeSettings string
	ReadFile       func(string) ([]byte, error)
	// Skill — SKILL.md gate4ai у Claude Code и его текущая версия из каталога
	// ("" — не проверять).
	SkillPath string
	Skill     func() (string, error)
}

// BashRule — правило Claude Code, которое отдаёт решения о командах gate4ai
// его политике (тот же текст пишет подключение Claude Code в Web UI).
const BashRule = "Bash(gate4ai *)"

// agentEnv — переменные, по которым видно, что команду запустил агент.
var agentEnv = []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CODEX_SANDBOX", "CODEX_MANAGED_BY_NPM", "GEMINI_CLI", "CURSOR_AGENT"}

// RealDoctorEnv — окружение текущего процесса.
func RealDoctorEnv(daemon func() error) DoctorEnv {
	home, _ := os.UserHomeDir()
	return DoctorEnv{Home: home, GOOS: goos(), Environ: os.Environ(), Daemon: daemon, LookPath: exec.LookPath, ReadFile: os.ReadFile,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		Readable: canOpen(os.O_RDONLY), Writable: canOpen(os.O_WRONLY)}
}

// canOpen — файл открывается с флагом flag.
func canOpen(flag int) func(string) bool {
	return func(p string) bool {
		f, err := os.OpenFile(p, flag, 0)
		if err == nil {
			f.Close()
		}
		return err == nil
	}
}

var secretEnvRe = regexp.MustCompile(`(?i)(TOKEN|API_?KEY|SECRET|PASSWORD|PASSWD|CREDENTIAL|PRIVATE_KEY|ACCESS_KEY)`)

// Doctor ищет обходные пути: агент запущен не через `gate4ai run`, доступен
// профиль Chrome / Claude in Chrome, токены в окружении, открыт Docker-сокет.
func Doctor(e DoctorEnv) []Check {
	var out []Check
	add := func(id, status, detail, fix string) { out = append(out, Check{id, status, detail, fix}) }
	h := func(p string) string { return filepath.Join(e.Home, filepath.FromSlash(p)) }
	env := map[string]string{}
	for _, kv := range e.Environ {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}

	if e.Daemon != nil {
		if err := e.Daemon(); err != nil {
			add("daemon", "fail", err.Error(), "start it: gate4ai daemon start (in the background) or gate4ai service install (also at every login)")
		} else {
			add("daemon", "ok", "daemon reachable", "")
		}
	}
	if env["GATE4AI_RUN_ID"] != "" {
		add("isolation", "ok", "running inside `gate4ai run` "+env["GATE4AI_RUN_ID"], "")
	} else {
		add("isolation", "warn", "this shell is not inside `gate4ai run`: an agent started here has the full rights of your user",
			"start agents with `gate4ai run <agent> --workspace <dir>` (sandbox-runtime), --container or --microvm")
	}

	agent := ""
	for _, k := range agentEnv {
		if env[k] != "" {
			agent = k
			break
		}
	}
	switch {
	case agent != "" && env["GATE4AI_TOKEN"] == "":
		add("agent-token", "warn", "this shell belongs to an agent ("+agent+") and has no GATE4AI_TOKEN: its gate4ai commands run with the owner's rights — it could approve its own requests (gate4ai approvals approve) and change the policy",
			"give the agent its own token: Web UI → Agents → connect Claude Code (writes the token into ~/.claude/settings.json), or gate4ai agents token <agent> and put it into GATE4AI_TOKEN of the agent's environment")
	case env["GATE4AI_TOKEN"] != "":
		add("agent-token", "ok", "GATE4AI_TOKEN is set: gate4ai commands run with the agent's profile, owner commands are refused", "")
	}
	if e.ClaudeSettings != "" {
		out = append(out, claudeChecks(e)...)
	}
	if e.SkillPath != "" && e.Skill != nil {
		if old, err := e.ReadFile(e.SkillPath); err == nil {
			if cur, err := e.Skill(); err == nil && cur != string(old) {
				add("skill", "warn", e.SkillPath+" is older than the catalog: the agent does not see some actions or their limits",
					"gate4ai skill > "+e.SkillPath+" (or connect Claude Code again in Web UI → Agents)")
			} else if err == nil {
				add("skill", "ok", e.SkillPath+" matches the catalog", "")
			}
		}
	}

	var browsers []string
	switch e.GOOS {
	case "darwin":
		browsers = []string{"Library/Application Support/Google/Chrome", "Library/Application Support/Chromium", "Library/Application Support/Firefox"}
	case "windows":
		browsers = []string{"AppData/Local/Google/Chrome/User Data", "AppData/Roaming/Mozilla/Firefox"}
	default:
		browsers = []string{".config/google-chrome", ".config/chromium", ".mozilla/firefox"}
	}
	var open []string
	for _, b := range browsers {
		if e.Readable(h(b)) {
			open = append(open, "~/"+b)
		}
	}
	if len(open) > 0 {
		add("browser-profiles", "warn", "browser profiles with live sessions are readable: "+strings.Join(open, ", "),
			"run agents via `gate4ai run` (profiles are closed to the sandbox); use the browser connector's dedicated profile instead")
	} else {
		add("browser-profiles", "ok", "no readable browser profiles", "")
	}

	var nmh []string
	switch e.GOOS {
	case "darwin":
		nmh = []string{"Library/Application Support/Google/Chrome/NativeMessagingHosts"}
	case "windows":
		nmh = nil
	default:
		nmh = []string{".config/google-chrome/NativeMessagingHosts", ".config/chromium/NativeMessagingHosts"}
	}
	claudeChrome := false
	for _, d := range nmh {
		if ents, err := os.ReadDir(h(d)); err == nil {
			for _, en := range ents {
				n := strings.ToLower(en.Name())
				if strings.Contains(n, "anthropic") || strings.Contains(n, "claude") {
					claudeChrome = true
				}
			}
		}
	}
	if claudeChrome {
		add("claude-in-chrome", "warn", "Claude in Chrome native messaging host is installed: agents can drive your main browser bypassing gate4ai",
			"disable the Claude in Chrome extension in your main profile or uninstall its native host")
	} else {
		add("claude-in-chrome", "ok", "Claude in Chrome bridge not found", "")
	}

	var leaked []string
	for k := range env {
		if k == "GATE4AI_TOKEN" || !secretEnvRe.MatchString(k) {
			continue
		}
		leaked = append(leaked, k)
	}
	sort.Strings(leaked)
	if len(leaked) > 0 {
		add("env-secrets", "warn", "secrets in the environment are visible to agents: "+strings.Join(leaked, ", "),
			"remove them from the shell profile; store them with `gate4ai vault set` and use egress secret substitution")
	} else {
		add("env-secrets", "ok", "no secret-looking variables in the environment", "")
	}

	sock := "/var/run/docker.sock"
	if e.GOOS == "windows" {
		sock = `\\.\pipe\docker_engine`
	}
	if e.Writable(sock) {
		add("docker-socket", "warn", "the Docker socket is writable: an agent can start a privileged container and escape",
			"do not mount it into agents; run agents via `gate4ai run` (the sandbox has no Docker socket)")
	} else {
		add("docker-socket", "ok", "Docker socket not writable", "")
	}

	if e.Readable(h(".ssh")) {
		add("ssh-keys", "warn", "~/.ssh is readable by processes of this user", "agents in `gate4ai run` cannot read it; do not run agents outside the sandbox")
	}

	if _, err := e.LookPath("srt"); err != nil {
		add("sandbox-runtime", "warn", "sandbox-runtime (srt) is not installed: `gate4ai run` level 1 is unavailable",
			"npm install -g @anthropic-ai/sandbox-runtime, or use --container")
	} else {
		add("sandbox-runtime", "ok", "srt found", "")
	}
	if _, err := e.LookPath("docker"); err != nil {
		if _, err := e.LookPath("podman"); err != nil {
			add("containers", "warn", "neither docker nor podman found: --container is unavailable", "install Docker or Podman")
		}
	}
	return out
}

// claudeChecks — настройки Claude Code: токен агента для команд Bash и правило
// разрешения команд gate4ai (иначе одно действие одобряют дважды: Claude Code
// и политика gate4ai — и решения расходятся).
func claudeChecks(e DoctorEnv) []Check {
	b, err := e.ReadFile(e.ClaudeSettings)
	if err != nil {
		if _, lerr := e.LookPath("claude"); lerr != nil {
			return nil // Claude Code нет
		}
		b = []byte("{}")
	}
	var st struct {
		Env         map[string]any `json:"env"`
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return []Check{{"claude-code-settings", "warn", e.ClaudeSettings + " is not valid JSON: " + err.Error(), "fix the file"}}
	}
	var out []Check
	if tok, _ := st.Env["GATE4AI_TOKEN"].(string); tok == "" {
		out = append(out, Check{"claude-code-token", "warn", "Claude Code has no GATE4AI_TOKEN in " + e.ClaudeSettings + ": its gate4ai commands run with the owner's rights",
			"Web UI → Agents → connect Claude Code, or add \"env\": {\"GATE4AI_TOKEN\": \"<gate4ai agents token claude-code>\"} to " + e.ClaudeSettings})
	} else {
		out = append(out, Check{"claude-code-token", "ok", "Claude Code runs gate4ai with its agent token", ""})
	}
	allowed := false
	for _, r := range st.Permissions.Allow {
		if r == "Bash(gate4ai *)" || r == "Bash(gate4ai:*)" || r == "Bash(gate4ai)" {
			allowed = true
		}
	}
	if allowed {
		out = append(out, Check{"claude-code-permission", "ok", "Claude Code leaves decisions about gate4ai commands to gate4ai's policy", ""})
	} else {
		out = append(out, Check{"claude-code-permission", "warn", "Claude Code asks about gate4ai commands itself, on top of gate4ai's policy: one action is approved twice and the decisions may differ",
			"add \"" + BashRule + "\" to permissions.allow in " + e.ClaudeSettings + " (Web UI → Agents → connect Claude Code does it); gate4ai's policy still decides every action"})
	}
	return out
}

var runtimeGOOS = runtime.GOOS

func goos() string {
	if v := os.Getenv("GATE4AI_GOOS"); v != "" {
		return v
	}
	return runtimeGOOS
}
