// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"time"

	"github.com/gate4ai/gate4ai/e2e/testsite"
)

// Агентский профиль connector-dev, как у пользователя: browser.* → allow и
// gate4ai.change_policy → allow. Через CLI: reload страницы с несохранёнными
// изменениями (beforeunload) возвращается за timeout_ms, сеанс работает;
// page_screenshot --out пишет PNG и отдаёт путь; два агента работают каждый в
// своём сеансе, close --all одного не трогает другого; browser.post и правка
// списка сайтов ждут владельца, несмотря на allow; policy set предупреждает
// об этом; gate4ai skill обновляет установленный навык прежней версии.
func TestBrowserAgentProfile(t *testing.T) {
	t.Parallel()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		if chrome, err = exec.LookPath("chromium"); err != nil {
			t.Skip("needs Chrome")
		}
	}
	srv := httptest.NewServer(testsite.New().Handler()) // /publish — beforeunload, /other
	defer srv.Close()
	g := startGate(t)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	cfg := fmt.Sprintf(`agents:
  claude-code: { profile: connector-dev }
  codex: { profile: connector-dev }
profiles:
  connector-dev:
    connectors: [browser]
    rules:
      - { id: dev-browser-allow, action: "browser.*", effect: allow }
      - { id: gate4ai-change_policy-allow, action: gate4ai.change_policy, effect: allow }
access:
  sites: [{ domain: "127.0.0.1", access: allow }]
connectors:
  browser:
    engine: cdp
    chrome_path: %q
    profile_dir: %q
`, wrapper, filepath.Join(dir, "profile"))
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0o600)
	r := g.run("", "", "policy", "set", cfgFile, "-m", "connector-dev")
	if r.code != 0 || !strings.Contains(r.out, "allow has no effect on gate4ai.change_policy") || !strings.Contains(r.out, "browser.* does not cover browser.delete") {
		t.Fatalf("policy set warns about the rules: %d %s", r.code, r.out)
	}
	tokA := g.run("", "", "agents", "token", "claude-code").json(t)["token"].(string)
	tokB := g.run("", "", "agents", "token", "codex").json(t)["token"].(string)
	call := func(tok string, want int, args ...string) map[string]any {
		t.Helper()
		r := g.run(tok, "", args...)
		if r.code != want {
			t.Fatalf("%v: exit %d (want %d): %s", args, r.code, want, r.out)
		}
		res := r.json(t)
		if m, ok := res["result"].(map[string]any); ok && want == 0 {
			return m
		}
		return res
	}

	open := call(tokA, 0, "browser", "open", "--url", srv.URL+"/publish")
	sA := open["session"].(string)
	call(tokA, 0, "browser", "act", "--session", sA, "--selector", "#q", "--method", "press", "--value", "a")
	start := time.Now()
	// Срок с запасом на нагрузку CI (Chrome без окна рядом с другими тестами); зависание — это минуты.
	rl := call(tokA, 0, "browser", "reload", "--session", sA, "--wait_for_selector", "textarea", "--timeout_ms", "20000")
	if rl["title"] != "Publish" || time.Since(start) > 25*time.Second {
		t.Fatalf("reload with beforeunload: %v in %s", rl, time.Since(start))
	}
	if p := call(tokA, 0, "browser", "observe", "--session", sA); p["title"] != "Publish" {
		t.Fatalf("the session works after reload: %v", p)
	}

	// Скриншот — файл: путь в result.file, в content — путь вместо base64.
	png := filepath.Join(dir, "shot.png")
	shot := call(tokA, 0, "browser", "page_screenshot", "--session", sA, "--selector", "textarea", "--out", png)
	if b, err := os.ReadFile(png); err != nil || !bytes.HasPrefix(b, []byte("\x89PNG")) || shot["file"] != png {
		t.Fatalf("page_screenshot --out: %v %v", shot, err)
	}

	// Второй агент — свой сеанс в своей вкладке; close --all первого его не трогает.
	sB := call(tokB, 0, "browser", "open", "--url", srv.URL+"/other")["session"].(string)
	if res := call(tokA, 0, "browser", "close", "--all"); res["sessions"] != 1.0 {
		t.Fatalf("close --all of the first agent: %v", res)
	}
	if p := call(tokB, 0, "browser", "observe", "--session", sB); p["title"] != "Other" {
		t.Fatalf("the second agent's session survives: %v", p)
	}
	r = g.run(tokA, "", "browser", "observe", "--session", sA)
	if r.code == 0 || !strings.Contains(r.out, "closed by browser.close --all") {
		t.Fatalf("a closed session says who closed it: %d %s", r.code, r.out)
	}

	// Сайт в списке Браузера как allow покрывает и запросы, меняющие данные;
	// режима read у сайтов нет — доступ к домену либо есть, либо нет.
	// change_policy — всегда владелец.
	call(tokB, 0, "browser", "post", "--session", sB, "--url", srv.URL+"/api/items", "--body", `{"x":1}`)
	if r := g.run("", "", "access", "sites", "set", "127.0.0.1", "read"); r.code == 0 || !strings.Contains(r.out, "allow, connector, ask, deny") {
		t.Fatalf("read accepted for a site: %d %s", r.code, r.out)
	}
	if r := g.run("", "", "access", "sites", "set", "127.0.0.1", "allow"); r.code != 0 {
		t.Fatalf("access sites set: %d %s", r.code, r.out)
	}
	if res := call(tokB, 3, "gate4ai", "change_policy", "--summary", "allow grafana.com", "--changes",
		`[{"op":"set_site_access","domain":"grafana.com","access":"allow"}]`); res["status"] != "pending" {
		t.Fatalf("set_site_access under change_policy allow: %v", res)
	}
	call(tokB, 0, "browser", "close", "--session", sB)

	// Установленный навык прежней версии — на обёртку «выполни gate4ai skill».
	skill := filepath.Join(g.user, ".claude", "skills", "gate4ai", "SKILL.md")
	os.MkdirAll(filepath.Dir(skill), 0o700)
	os.WriteFile(skill, []byte("---\nname: gate4ai\ndescription: old\n---\n# read, screenshot, extract\n"), 0o600)
	if r := g.run(tokA, "", "skill"); r.code != 0 || !strings.Contains(r.out, "## Browser sessions") {
		t.Fatalf("gate4ai skill: %d %s", r.code, r.out)
	}
	if b, _ := os.ReadFile(skill); !strings.Contains(string(b), "run `gate4ai skill` and follow what it prints") {
		t.Fatalf("the installed skill is refreshed: %s", b)
	}
}
