// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package procinfo

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
)

func lookup(pid int, keys []string) (Proc, error) {
	dir := "/proc/" + strconv.Itoa(pid) + "/"
	st, err := os.ReadFile(dir + "stat")
	if err != nil {
		return Proc{}, err
	}
	// pid (comm) state ppid …; comm может содержать пробелы и скобки.
	open, end := bytes.IndexByte(st, '('), bytes.LastIndexByte(st, ')')
	if open < 0 || end < open {
		return Proc{}, errors.New("procinfo: bad stat")
	}
	f := strings.Fields(string(st[end+1:]))
	if len(f) < 2 {
		return Proc{}, errors.New("procinfo: bad stat")
	}
	ppid, _ := strconv.Atoi(f[1])
	p := Proc{PID: pid, PPID: ppid, Name: string(st[open+1 : end])}
	p.Exe, _ = os.Readlink(dir + "exe")
	if cmd, err := os.ReadFile(dir + "cmdline"); err == nil {
		p.Arg0, _, _ = strings.Cut(string(cmd), "\x00")
	}
	if len(keys) > 0 {
		if env, err := os.ReadFile(dir + "environ"); err == nil {
			p.Env = envFilter(strings.Split(string(env), "\x00"), keys)
		}
	}
	return p, nil
}

func cwdOf(pid int) string {
	d, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	return d
}
