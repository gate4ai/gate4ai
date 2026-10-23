// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Агент подключается сам: первая команда — код, владелец подтверждает его в
// Web UI, дальше агент работает своим ключом без перезапуска; сокет владельца
// агенту отказывает.
func TestAgentPairing(t *testing.T) {
	if underAgent {
		t.Skip("run by an agent: its owner socket check is off here (GATE4AI_OWNER_CHECK=off); runs in CI")
	}
	t.Parallel()
	g := startGate(t, "--ui", "127.0.0.1:0")
	agent := func(args ...string) cliResult {
		cmd := exec.Command(g.bin, args...)
		cmd.Env = append(append([]string{}, g.env...), "CLAUDECODE=1")
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return cliResult{code, string(out)}
	}
	// Команда владельца от агента — отказ, а не права владельца.
	if r := agent("approvals", "list"); r.code != 7 || !strings.Contains(r.out, "agent_required") {
		t.Fatalf("owner command from an agent: %d %s", r.code, r.out)
	}
	r := agent("tools", "--json")
	code, _ := r.json(t)["code"].(string)
	if r.code != 3 || code == "" {
		t.Fatalf("first call: %d %s", r.code, r.out)
	}
	if b, err := os.ReadFile(filepath.Join(g.home, "agents", "claude-code.json")); err != nil || !strings.Contains(string(b), `"pairing"`) {
		t.Fatalf("key file: %s %v", b, err)
	}

	// Запрос — в журнале daemon, а не во всплывающем уведомлении
	// (GATE4AI_DESKTOP_NOTIFY=log в харнессе); каналы уведомляют асинхронно.
	logged := func() bool {
		l := g.log.String()
		return strings.Contains(l, "desktop notification") && strings.Contains(l, code)
	}
	for end := time.Now().Add(5 * time.Second); !logged() && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
	}
	if !logged() {
		t.Fatalf("pairing request not in the daemon log:\n%s", g.log.String())
	}

	// Владелец: Web UI по одноразовой ссылке, запрос с тем же кодом, «Разрешить».
	lr := g.run("", "", "ui", "--print")
	login := strings.TrimSpace(lr.out)
	if lr.code != 0 || !strings.HasPrefix(login, "http://127.0.0.1:") {
		t.Fatalf("ui --print: %d %s", lr.code, lr.out)
	}
	base := login[:strings.Index(login, "/login")]
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(login) // свежая установка: вход ведёт в мастер первого запуска
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp, err = c.Get(base + "/agents"); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(b)
	m := regexp.MustCompile(`action="/pairings/(p_[0-9a-f]+)"`).FindStringSubmatch(page)
	csrf := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if m == nil || csrf == nil || !strings.Contains(page, code) {
		t.Fatalf("no pairing request with code %s on the Agents page:\n%s", code, page)
	}
	resp, err = c.PostForm(base+"/pairings/"+m[1], url.Values{"csrf": {csrf[1]}, "decision": {"approve"}, "profile": {"_new"}})
	if err != nil || resp.StatusCode >= 400 {
		t.Fatalf("approve: %v %v", resp, err)
	}
	resp.Body.Close()

	if r := agent("tools", "--json"); r.code != 0 {
		t.Fatalf("after approval: %d %s", r.code, r.out)
	}
	if r := agent("doctor"); !strings.Contains(r.out, "gate4ai commands run with the key of claude-code") {
		t.Fatalf("doctor: %s", r.out)
	}
}
