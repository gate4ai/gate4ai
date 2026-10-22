// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// Токен ссылки входа не попадает в argv открывателя браузера (код-ревью E.11).
func TestOpenLoginURLKeepsTokenOutOfArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows ссылка открывается как есть")
	}
	dir := t.TempDir()
	oldOpen, oldDir, oldSnap := OpenURL, handoffDir, snapBrowser
	t.Cleanup(func() { OpenURL, handoffDir, snapBrowser = oldOpen, oldDir, oldSnap })
	handoffDir = func() (string, error) { return dir, nil }
	snapBrowser = func() bool { return false }
	var opened string
	OpenURL = func(u string) error { opened = u; return nil }
	link := `http://127.0.0.1:7777/login?t=secret-token"</script>`
	if err := OpenLoginURL(link); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(opened, "secret-token") || !strings.HasPrefix(opened, "file://") {
		t.Fatalf("argv gets %q", opened)
	}
	path := strings.TrimPrefix(opened, "file://")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("handoff file: %v %v", fi, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "secret-token") || strings.Contains(string(b), `token"</script>`) {
		t.Fatalf("page: %s", b)
	}
	snapBrowser = func() bool { return true }
	if err := OpenLoginURL(link); err != nil || opened != link {
		t.Fatalf("snap fallback: %q", opened)
	}
}

// --reset-ui-login не повторяется при restart и не попадает в автозапуск (E.14).
func TestWithoutOneShot(t *testing.T) {
	got := withoutOneShot([]string{"--home", "/h", "--reset-ui-login", "-reset-ui-login=true", "--public", ":443"})
	if strings.Join(got, " ") != "--home /h --public :443" {
		t.Fatal(got)
	}
}

// contract и fixture — встроенные команды со своей справкой (E.19).
func TestHelpContractFixture(t *testing.T) {
	for _, c := range []string{"contract", "fixture"} {
		if !isCommand(c) {
			t.Fatal(c)
		}
	}
	if !strings.Contains(usage, "daemon start [flags] | stop | restart | status") {
		t.Fatal("usage without restart")
	}
}
