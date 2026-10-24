// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package worker

import (
	"os"
	"os/exec"
	"time"
)

func ownGroup(*exec.Cmd) {}

// StopChrome завершает Chrome и ждёт выхода главного процесса (дочерние
// процессы Chrome на Windows завершаются вместе с ним).
func StopChrome(p *os.Process, grace time.Duration) {
	if p == nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = p.Wait(); close(done) }()
	_ = p.Kill()
	select {
	case <-done:
	case <-time.After(grace):
	}
}

// pipeSupported — на Windows exec.Cmd.ExtraFiles не работает: порт отладки.
const pipeSupported = false
