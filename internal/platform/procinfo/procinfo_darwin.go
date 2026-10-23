// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package procinfo

import (
	"bytes"
	"context"
	"encoding/binary"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func lookup(pid int, keys []string) (Proc, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Proc{}, err
	}
	name := kp.Proc.P_comm[:]
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	p := Proc{PID: pid, PPID: int(kp.Eproc.Ppid), Name: string(name)}
	// kern.procargs2: argc (int32), путь бинарника, нули, argv…, окружение…
	if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil && len(raw) > 4 {
		argc := int(binary.LittleEndian.Uint32(raw[:4]))
		parts := strings.Split(string(raw[4:]), "\x00")
		i := 0
		if len(parts) > 0 {
			p.Exe = parts[0]
			i = 1
		}
		for i < len(parts) && parts[i] == "" {
			i++
		}
		if i < len(parts) {
			p.Arg0 = parts[i]
		}
		if i+argc <= len(parts) && len(keys) > 0 {
			p.Env = envFilter(parts[i+argc:], keys)
		}
	}
	return p, nil
}

// cwdOf — через lsof: текущий каталог чужого процесса без cgo иначе не прочитать.
func cwdOf(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if d, ok := strings.CutPrefix(l, "n"); ok {
			return d
		}
	}
	return ""
}
