// SPDX-License-Identifier: AGPL-3.0-only

// Package procinfo — сведения о процессах текущего пользователя: родитель,
// имя и бинарник, текущий каталог, нужные переменные окружения. По ним daemon
// узнаёт, какой агент (Claude Code, Codex…) стоит за собеседником сокета и из
// какого каталога он запущен; CLI — то же о себе.
package procinfo

import (
	"path/filepath"
	"strings"
)

// Proc — процесс.
type Proc struct {
	PID  int    `json:"pid"`
	PPID int    `json:"-"`
	Name string `json:"name,omitempty"` // имя процесса (comm на Linux)
	Exe  string `json:"exe,omitempty"`  // путь к бинарнику ("" — неизвестен)
	Arg0 string `json:"-"`              // argv[0]
	Cwd  string `json:"cwd,omitempty"`  // текущий каталог ("" — не читали или неизвестен)
	// Env — запрошенные переменные окружения процесса (nil — окружение недоступно).
	Env map[string]string `json:"-"`
}

// Names — имена, под которыми процесс известен: имя, бинарник и argv[0]
// (без каталога и расширения .exe).
func (p Proc) Names() []string {
	var out []string
	for _, s := range []string{p.Name, p.Exe, p.Arg0} {
		if s == "" {
			continue
		}
		b := strings.TrimSuffix(filepath.Base(strings.ReplaceAll(s, `\`, "/")), ".exe")
		if b != "" && b != "." && b != "/" {
			out = append(out, b)
		}
	}
	return out
}

// Lookup — процесс pid; keys — переменные окружения, которые нужно прочитать.
// Текущий каталог не читается (CwdOf): на macOS это дорого.
func Lookup(pid int, keys []string) (Proc, error) { return lookup(pid, keys) }

// CwdOf — текущий каталог процесса ("" — неизвестен).
func CwdOf(pid int) string { return cwdOf(pid) }

// MaxChain — сколько предков читается самое большее.
const MaxChain = 64

// Chain — процесс pid и его предки до первого недоступного или до init.
func Chain(pid int, keys []string) []Proc {
	var out []Proc
	seen := map[int]bool{}
	for pid > 1 && len(out) < MaxChain && !seen[pid] {
		seen[pid] = true
		p, err := lookup(pid, keys)
		if err != nil {
			break
		}
		out = append(out, p)
		pid = p.PPID
	}
	return out
}

// envFilter — нужные переменные из окружения вида KEY=VALUE.
func envFilter(kvs []string, keys []string) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, want := range keys {
			if k == want {
				m[k] = v
			}
		}
	}
	return m
}
