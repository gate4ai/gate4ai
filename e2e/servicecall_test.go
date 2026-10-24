// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/e2e/testsite"
)

// WASM-коннектор вызывает действия нативного коннектора browser через
// service_call: настоящий daemon, процесс browser под его надзором, headless
// Chrome, тестовый сайт testsite. Сайты задаёт вызывающий (sites манифеста),
// browser в профиле агента не нужен; сайт вне его списка отклоняется.
func TestServiceCallToNativeBrowser(t *testing.T) {
	t.Parallel()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		if chrome, err = exec.LookPath("chromium"); err != nil {
			t.Skip("needs Chrome")
		}
	}
	srv := httptest.NewServer(testsite.New().Handler())
	defer srv.Close()

	g := startGate(t)
	g.installFrom(src(t, "e2e/testconnector"))
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	cfg := fmt.Sprintf(`agents:
  claude-ai: { profile: assistant }
profiles:
  assistant:
    connectors: [testconnector]
access:
  sites: [{ domain: "127.0.0.1", access: connector }]
connectors:
  testconnector: { domains: ["127.0.0.1"], site: %q }
  browser:
    engine: cdp
    chrome_path: %q
    profile_dir: %q
`, srv.URL, wrapper, filepath.Join(dir, "profile"))
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0o600)
	if r := g.run("", "", "policy", "set", cfgFile, "-m", "service_call e2e"); r.code != 0 {
		t.Fatalf("policy: %d %s", r.code, r.out)
	}
	token := g.run("", "", "agents", "token", "claude-ai").json(t)["token"].(string)

	r := g.run(token, "", "testconnector", "read")
	res := r.json(t)
	result, _ := res["result"].(map[string]any)
	rows, _ := result["rows"].([]any)
	if r.code != 0 || res["status"] != "ok" || result["title"] != "Показания" || len(rows) != len(testsite.Rows) {
		t.Fatalf("testconnector read: %d %s", r.code, r.out)
	}
	if first, _ := rows[0].(map[string]any); first["meter"] != testsite.Rows[0].Meter || first["value"] != testsite.Rows[0].Value {
		t.Fatalf("rows: %v", rows)
	}
	// Сайт вне списка вызывающего.
	r = g.run(token, "", "testconnector", "read", "--url", "http://localhost:1/")
	if r.code == 0 || !strings.Contains(r.out, "denied by policy") || !strings.Contains(r.out, "localhost") {
		t.Fatalf("site outside the connector sites: %d %s", r.code, r.out)
	}
	// Агент не видит действий browser только для коннекторов.
	if r := g.run(token, "", "browser", "open", "--url", srv.URL); r.code == 0 {
		t.Fatalf("service-only action visible to the agent: %s", r.out)
	}
}
