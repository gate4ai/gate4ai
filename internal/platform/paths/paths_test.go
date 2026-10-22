// SPDX-License-Identifier: AGPL-3.0-only

package paths

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// CLI без XDG_RUNTIME_DIR находит сокет daemon, запущенного с ним, и наоборот.
func TestFindOwnerSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fallback paths are Linux-specific")
	}
	// /run/user/<uid> подменить нельзя: запущенный daemon пользователя найдётся первым.
	if _, err := os.Stat(filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "gate4ai", "gate4ai.sock")); err == nil {
		t.Skip("a gate4ai daemon of this user is running")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("GATE4AI_HOME", "")
	t.Setenv("GATE4AI_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(home, "xdg"))
	if got := FindOwnerSocket(); got != filepath.Join(home, "xdg", "gate4ai", "gate4ai.sock") {
		t.Fatalf("no socket anywhere: %s", got)
	}
	alt := filepath.Join(home, ".local", "state", "gate4ai", "run", "gate4ai.sock")
	os.MkdirAll(filepath.Dir(alt), 0o700)
	os.WriteFile(alt, nil, 0o600)
	if got := FindOwnerSocket(); got != alt {
		t.Fatalf("fallback: %s", got)
	}
	t.Setenv("GATE4AI_SOCKET", "/x/explicit.sock")
	if got := FindOwnerSocket(); got != "/x/explicit.sock" {
		t.Fatal(got)
	}
}

// Имя pipe — по SID, а не по короткому имени (PC\alex и CORP\alex различаются).
func TestPipeNameUsesSID(t *testing.T) {
	a := pipeName("gate4ai-", &user.User{Uid: "S-1-5-21-1-2-3-1001", Username: `PC\alex`})
	b := pipeName("gate4ai-", &user.User{Uid: "S-1-5-21-9-8-7-1001", Username: `CORP\alex`})
	if a != `\\.\pipe\gate4ai-S-1-5-21-1-2-3-1001` || a == b {
		t.Fatalf("pipe names: %s, %s", a, b)
	}
	if got := pipeName("gate4ai-agent-", nil); got != `\\.\pipe\gate4ai-agent-user` {
		t.Fatal(got)
	}
}
