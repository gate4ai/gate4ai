// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/platform/ipc"
)

// Второй daemon сообщает «already running», а не о занятом порте Web UI.
func TestDaemonAlreadyRunning(t *testing.T) {
	dir, _ := os.MkdirTemp("", "g4d")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "o.sock")
	l, err := ipc.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	err = daemonMain([]string{"--home", filepath.Join(dir, "h"), "--socket", sock, "--agent-socket", filepath.Join(dir, "a.sock"), "--keystore", "file", "--ui", ""}, false)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal(err)
	}
	err = daemonMain([]string{"--home", filepath.Join(dir, "h"), "--socket", "/" + strings.Repeat("s", 200), "--keystore", "file", "--ui", ""}, false)
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatal(err)
	}
	if code := runDaemon([]string{"--help"}, false); code != 0 {
		t.Fatal(code)
	}
}
