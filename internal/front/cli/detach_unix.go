// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// detach отвязывает процесс от терминала: своя сессия, SIGHUP при закрытии
// терминала до него не доходит.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
