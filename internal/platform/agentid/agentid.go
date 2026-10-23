// SPDX-License-Identifier: AGPL-3.0-only

// Package agentid узнаёт локального агента (Claude Code, Codex, Gemini CLI…)
// по процессу: по признакам, которые агент ставит в окружение своих дочерних
// процессов (CLAUDECODE=1 и т.п.), и по имени процесса среди предков.
//
// Корень агента — самый внешний процесс-агент в цепочке предков: подагенты и
// вложенные запуски агента (claude -p внутри Claude Code) относятся к нему, а
// его каталог запуска — каталог, из которого агент работает (cd в shell
// агента его не меняет). Процесс того же пользователя ОС, отвязанный от дерева
// и без признаков в окружении, так не опознаётся: это защита от случайного и
// «ленивого» обхода, а не граница изоляции (её даёт gate4ai run).
package agentid

import (
	"github.com/gate4ai/gate4ai/internal/platform/procinfo"
)

// envMarkers — переменные окружения, которые агент ставит своим дочерним
// процессам (shell, MCP-серверы), → тип агента.
var envMarkers = []struct{ Key, Agent string }{
	{"CLAUDECODE", "claude-code"},
	{"CLAUDE_CODE_ENTRYPOINT", "claude-code"},
	{"CODEX_SANDBOX", "codex"},
	{"CODEX_MANAGED_BY_NPM", "codex"},
	{"GEMINI_CLI", "gemini"},
	{"CURSOR_AGENT", "cursor"},
}

// procNames — имена процессов агентов → тип агента.
var procNames = map[string]string{
	"claude":       "claude-code",
	"codex":        "codex",
	"gemini":       "gemini",
	"cursor-agent": "cursor",
	"hermes":       "hermes",
	"openclaw":     "openclaw",
}

// SessionKey — идентификатор сессии Claude Code в окружении её процессов.
const SessionKey = "CLAUDE_CODE_SESSION_ID"

// Keys — переменные окружения, которые нужно читать у процессов.
func Keys() []string {
	out := []string{SessionKey}
	for _, m := range envMarkers {
		out = append(out, m.Key)
	}
	return out
}

// FromEnv — тип агента по окружению этого процесса ("" — признаков агента нет).
func FromEnv(getenv func(string) string) string {
	for _, m := range envMarkers {
		if getenv(m.Key) != "" {
			return m.Agent
		}
	}
	return ""
}

// ByName — тип агента по имени процесса ("" — не агент).
func ByName(p procinfo.Proc) string {
	for _, n := range p.Names() {
		if a, ok := procNames[n]; ok {
			return a
		}
	}
	return ""
}

// Caller — кто стоит за процессом.
type Caller struct {
	Agent   string          `json:"agent,omitempty"`   // тип агента; "" — агента среди предков нет
	Dir     string          `json:"dir,omitempty"`     // каталог запуска корня агента (без агента — каталог самого процесса)
	Session string          `json:"session,omitempty"` // сессия агента (Claude Code)
	Chain   []procinfo.Proc `json:"chain,omitempty"`   // от процесса до корня агента включительно
}

// IsAgent — среди предков есть агент.
func (c Caller) IsAgent() bool { return c.Agent != "" }

// Identify — кто стоит за процессом pid.
func Identify(pid int) Caller {
	return FromChain(procinfo.Chain(pid, Keys()), procinfo.CwdOf)
}

// FromChain — Caller по цепочке процессов (первый — сам процесс, дальше —
// предки); cwd читает каталог процесса.
func FromChain(chain []procinfo.Proc, cwd func(int) string) Caller {
	var c Caller
	if len(chain) == 0 {
		return c
	}
	root := -1
	for i, p := range chain {
		if a := ByName(p); a != "" {
			root, c.Agent = i, a
			continue
		}
		for _, m := range envMarkers {
			if p.Env[m.Key] == "" {
				continue
			}
			// Признак ставит агент своим детям: агент — родитель процесса
			// с признаком (если он виден), иначе — сам процесс.
			root, c.Agent = i, m.Agent
			if i+1 < len(chain) {
				root = i + 1
			}
			if s := p.Env[SessionKey]; s != "" {
				c.Session = s
			}
			break
		}
	}
	if root < 0 {
		c.Chain = chain[:1]
		c.Dir = cwd(chain[0].PID)
		c.Chain[0].Cwd = c.Dir
		return c
	}
	c.Chain = append([]procinfo.Proc(nil), chain[:root+1]...)
	c.Dir = cwd(c.Chain[root].PID)
	c.Chain[root].Cwd = c.Dir
	if root > 0 {
		c.Chain[0].Cwd = cwd(c.Chain[0].PID)
	}
	return c
}

// CheckOff — проверка агента выключена: только в сборке для стенда
// (DevStand) и только с GATE4AI_OWNER_CHECK=off.
func CheckOff(getenv func(string) string) bool {
	return DevStand && getenv("GATE4AI_OWNER_CHECK") == "off"
}
