// SPDX-License-Identifier: AGPL-3.0-only

package agentid

import (
	"os"
	"testing"

	"github.com/gate4ai/gate4ai/internal/platform/procinfo"
)

func cwds(m map[int]string) func(int) string { return func(pid int) string { return m[pid] } }

func TestFromChain(t *testing.T) {
	dirs := cwds(map[int]string{10: "/proj/sub", 20: "/proj/sub", 30: "/proj", 40: "/other", 50: "/proj2", 60: "/home"})
	cli := procinfo.Proc{PID: 10, Name: "gate4ai"}
	bash := procinfo.Proc{PID: 20, Name: "bash", Env: map[string]string{"CLAUDECODE": "1", SessionKey: "s1"}}
	claude := procinfo.Proc{PID: 30, Name: "claude", Exe: "/home/u/.local/share/claude/versions/2.1.3"}
	term := procinfo.Proc{PID: 60, Name: "gnome-terminal"}

	// gate4ai из Bash Claude Code: агент — claude, каталог — его каталог
	// запуска, а не каталог shell.
	c := FromChain([]procinfo.Proc{cli, bash, claude, term}, dirs)
	if c.Agent != "claude-code" || c.Dir != "/proj" || c.Session != "s1" || len(c.Chain) != 3 || c.Chain[2].Cwd != "/proj" || c.Chain[0].Cwd != "/proj/sub" {
		t.Fatalf("claude code: %+v", c)
	}

	// Подагент / вложенный claude -p из другого проекта: решает самый
	// внешний агент.
	inner := procinfo.Proc{PID: 50, Name: "claude"}
	bash2 := procinfo.Proc{PID: 40, Name: "bash", Env: map[string]string{"CLAUDECODE": "1", SessionKey: "s2"}}
	c = FromChain([]procinfo.Proc{cli, bash2, inner, bash, claude, term}, dirs)
	if c.Agent != "claude-code" || c.Dir != "/proj" || c.Session != "s1" || len(c.Chain) != 5 {
		t.Fatalf("nested: %+v", c)
	}

	// Claude Code из npm: процесс — node, агент узнаётся по признаку у ребёнка.
	node := procinfo.Proc{PID: 30, Name: "node", Exe: "/usr/bin/node"}
	c = FromChain([]procinfo.Proc{cli, bash, node, term}, dirs)
	if c.Agent != "claude-code" || c.Dir != "/proj" {
		t.Fatalf("npm claude: %+v", c)
	}

	// Отвязанный процесс с унаследованным признаком (setsid, cron): родителя
	// не видно — агент всё равно опознан.
	c = FromChain([]procinfo.Proc{cli, bash}, dirs)
	if c.Agent != "claude-code" || c.Dir != "/proj/sub" {
		t.Fatalf("detached: %+v", c)
	}

	// Человек в терминале: агента нет, каталог — свой.
	c = FromChain([]procinfo.Proc{cli, {PID: 20, Name: "bash"}, term}, dirs)
	if c.IsAgent() || c.Dir != "/proj/sub" || len(c.Chain) != 1 {
		t.Fatalf("owner: %+v", c)
	}

	// Codex по имени бинарника (Windows-путь).
	c = FromChain([]procinfo.Proc{cli, {PID: 30, Exe: `C:\Tools\codex.exe`}}, dirs)
	if c.Agent != "codex" {
		t.Fatalf("codex: %+v", c)
	}
	if FromChain(nil, dirs).IsAgent() {
		t.Fatal("empty chain")
	}
}

func TestFromEnv(t *testing.T) {
	env := map[string]string{"CLAUDECODE": "1"}
	if FromEnv(func(k string) string { return env[k] }) != "claude-code" || FromEnv(func(string) string { return "" }) != "" {
		t.Fatal("FromEnv")
	}
}

// Живой процесс: цепочка начинается с себя, каталог — текущий.
func TestIdentifySelf(t *testing.T) {
	ch := procinfo.Chain(os.Getpid(), Keys())
	if len(ch) == 0 {
		t.Skip("process info is not available on this OS")
	}
	if ch[0].PID != os.Getpid() || ch[0].PPID != os.Getppid() {
		t.Fatalf("chain: %+v", ch[0])
	}
	wd, _ := os.Getwd()
	if d := procinfo.CwdOf(os.Getpid()); d != "" && d != wd {
		t.Fatalf("cwd %q, want %q", d, wd)
	}
}
