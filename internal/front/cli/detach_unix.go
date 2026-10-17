// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// detach отвязывает процесс от терминала: своя сессия, SIGHUP при закрытии
// терминала до него не доходит.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
