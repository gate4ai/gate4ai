// SPDX-License-Identifier: AGPL-3.0-only

// Package paths — расположение данных и сокетов gate4.ai на каждой ОС.
package paths

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
)

// Home — каталог данных (конфигурация, журнал, состояние). GATE4AI_HOME переопределяет.
func Home() string {
	if h := os.Getenv("GATE4AI_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "gate4ai")
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "gate4ai")
		}
		return filepath.Join(home, "AppData", "Local", "gate4ai")
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "gate4ai")
	}
	return filepath.Join(home, ".local", "state", "gate4ai")
}

// RunDir — каталог сокетов.
func RunDir() string {
	if h := os.Getenv("GATE4AI_HOME"); h != "" {
		return filepath.Join(h, "run")
	}
	if runtime.GOOS == "linux" {
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
			return filepath.Join(x, "gate4ai")
		}
	}
	return filepath.Join(Home(), "run")
}

// OwnerSocket — сокет владельца (трей, CLI человека).
func OwnerSocket() string {
	if s := os.Getenv("GATE4AI_SOCKET"); s != "" {
		return s
	}
	if runtime.GOOS == "windows" {
		return pipeName("gate4ai-", currentUser())
	}
	return filepath.Join(RunDir(), "gate4ai.sock")
}

// AgentSocket — сокет агентов (требует токен агента, прав владельца не даёт).
func AgentSocket() string {
	if s := os.Getenv("GATE4AI_AGENT_SOCKET"); s != "" {
		return s
	}
	if runtime.GOOS == "windows" {
		return pipeName("gate4ai-agent-", currentUser())
	}
	return filepath.Join(RunDir(), "agent.sock")
}

// currentUser — текущий пользователь ОС (nil — неизвестен).
func currentUser() *user.User {
	u, err := user.Current()
	if err != nil {
		return nil
	}
	return u
}

// pipeName — named pipe Windows для пользователя u. В имени — SID (u.Uid на
// Windows), а не короткое имя: у PC\alex и CORP\alex оно одинаковое, и чужой
// пользователь мог бы заранее занять pipe с таким именем.
func pipeName(prefix string, u *user.User) string {
	id := "user"
	switch {
	case u == nil:
	case u.Uid != "":
		id = u.Uid
	case u.Username != "":
		id = filepath.Base(filepath.ToSlash(u.Username))
	}
	return `\\.\pipe\` + prefix + id
}

// FindOwnerSocket — сокет владельца, который есть на диске: CLI и трей могут
// быть запущены без XDG_RUNTIME_DIR (cron, su, часть ssh-сессий), а daemon —
// с ним, или наоборот. Если сокета нет ни в одном месте — OwnerSocket().
func FindOwnerSocket() string {
	return find(os.Getenv("GATE4AI_SOCKET"), OwnerSocket(), "gate4ai.sock")
}

// FindAgentSocket — то же для сокета агентов.
func FindAgentSocket() string {
	return find(os.Getenv("GATE4AI_AGENT_SOCKET"), AgentSocket(), "agent.sock")
}

func find(explicit, primary, name string) string {
	if explicit != "" || runtime.GOOS == "windows" || os.Getenv("GATE4AI_HOME") != "" {
		return primary
	}
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	for _, dir := range runDirCandidates() {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return primary
}

// runDirCandidates — все места, где daemon этого пользователя мог создать сокеты.
func runDirCandidates() []string {
	var out []string
	if runtime.GOOS == "linux" {
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
			out = append(out, filepath.Join(x, "gate4ai"))
		}
		out = append(out, filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "gate4ai"))
	}
	return append(out, filepath.Join(Home(), "run"))
}

// DaemonLog — журнал daemon, запущенного в фоне (`gate4ai daemon start`, автозапуск).
func DaemonLog() string { return filepath.Join(Home(), "daemon.log") }
