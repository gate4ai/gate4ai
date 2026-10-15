// SPDX-License-Identifier: FSL-1.1-ALv2

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
}

// RealDoctorEnv — окружение текущего процесса.
func RealDoctorEnv(daemon func() error) DoctorEnv {
	home, _ := os.UserHomeDir()
	return DoctorEnv{Home: home, GOOS: goos(), Environ: os.Environ(), Daemon: daemon, LookPath: exec.LookPath,
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		Readable: func(p string) bool {
			f, err := os.Open(p)
			if err != nil {
				return false
			}
			f.Close()
			return true
		},
		Writable: func(p string) bool {
			f, err := os.OpenFile(p, os.O_WRONLY, 0)
			if err != nil {
				return false
			}
			f.Close()
			return true
		}}
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

func goos() string {
	if v := os.Getenv("GATE4AI_GOOS"); v != "" {
		return v
	}
	return runtimeGOOS
}
