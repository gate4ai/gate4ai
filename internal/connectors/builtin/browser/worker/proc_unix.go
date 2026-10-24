// SPDX-License-Identifier: AGPL-3.0-only

//go:build !windows

package worker

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ownGroup — Chrome в своей группе процессов: при остановке закрываются и
// его дочерние процессы (рендереры, GPU, crashpad), а не только главный.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// StopChrome завершает Chrome со всеми его процессами: SIGTERM группе (Chrome
// сохраняет профиль), через grace — SIGKILL; ждёт выхода главного процесса.
func StopChrome(p *os.Process, grace time.Duration) {
	if p == nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = p.Wait(); close(done) }()
	_ = syscall.Kill(-p.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(grace):
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		<-done
	}
	// Дочерние процессы группы, пережившие главный.
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
