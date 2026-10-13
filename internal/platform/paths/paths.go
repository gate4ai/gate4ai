// SPDX-License-Identifier: FSL-1.1-ALv2

// Package paths — расположение данных и сокетов gate4.ai на каждой ОС.
package paths

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
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
		return `\\.\pipe\gate4ai-` + userName()
	}
	return filepath.Join(RunDir(), "gate4ai.sock")
}

// AgentSocket — сокет агентов (требует токен агента, прав владельца не даёт).
func AgentSocket() string {
	if s := os.Getenv("GATE4AI_AGENT_SOCKET"); s != "" {
		return s
	}
	if runtime.GOOS == "windows" {
		return `\\.\pipe\gate4ai-agent-` + userName()
	}
	return filepath.Join(RunDir(), "agent.sock")
}

func userName() string {
	u, err := user.Current()
	if err != nil {
		return "user"
	}
	n := filepath.Base(filepath.ToSlash(u.Username))
	return n
}
