// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
)

// startAsk — daemon с браузерным фейком, где download ждёт одобрения владельца.
func startAsk(t *testing.T) (*harness, string) {
	t.Helper()
	src, err := os.ReadFile("../../../connectors/browser/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	man, err := manifest.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	c, err := connectors.NewInProcess(&browserFake{man: man, args: map[string]map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	h := startWith(t, `
agents:
  a: { profile: web }
profiles:
  web:
    connectors: [browser]
    rules:
      - { id: dl, action: browser.download, effect: ask }
      - { id: all, action: "browser.*", effect: allow }
`, c)
	_, out := h.run(t, "", "agents", "token", "a")
	return h, jsonOf(t, out)["token"].(string)
}

// Файл ответа переживает одобрение: pending → approve → status --out пишет
// файл; подсказка pending называет точную команду с тем же путём; status без
// --out не печатает base64.
func TestOutAfterApproval(t *testing.T) {
	h, tok := startAsk(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "my dl.pdf")
	code, out := h.run(t, tok, "browser", "download", "--session", "s1", "--url", "https://a.example.com/a.pdf", "--out", path)
	res := jsonOf(t, out)
	if code != ExitPending {
		t.Fatal(code, out)
	}
	id := res["id"].(string)
	if want := "gate4ai status " + id + " --wait 10m --out '" + path + "'"; !strings.Contains(res["hint"].(string), want) {
		t.Fatal("hint:", out)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a file before the decision")
	}
	if code, out := h.run(t, "", "approvals", "approve", id); code != ExitOK {
		t.Fatal(out)
	}
	// Без --out — метаданные файла и подсказка, без base64.
	code, out = h.run(t, tok, "status", id, "--wait", "2s")
	if code != ExitOK || strings.Contains(out, base64.StdEncoding.EncodeToString(pdfFile)) {
		t.Fatal(code, out)
	}
	b := jsonOf(t, out)["content"].([]any)[0].(map[string]any)
	if b["data"] != nil || b["sha256"] != pdfSum || b["bytes"] != float64(len(pdfFile)) || !strings.Contains(b["hint"].(string), "--out") {
		t.Fatal(out)
	}
	// С --out — файл на диске, путь в result.file.
	code, out = h.run(t, tok, "status", id, "--out", path)
	if code != ExitOK {
		t.Fatal(code, out)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, pdfFile) {
		t.Fatalf("%q %v", got, err)
	}
	if r := jsonOf(t, out)["result"].(map[string]any); r["file"] != path || r["sha256"] != pdfSum {
		t.Fatal(out)
	}
}

// --wait --out: файл пишется, когда владелец одобрил во время ожидания.
func TestOutWaitApproval(t *testing.T) {
	h, tok := startAsk(t)
	path := filepath.Join(t.TempDir(), "a.pdf")
	type ran struct {
		code int
		out  string
	}
	done := make(chan ran, 1)
	go func() {
		code, out := h.run(t, tok, "browser", "download", "--session", "s1", "--url", "https://a.example.com/a.pdf", "--out", path, "--wait", "10s")
		done <- ran{code, out}
	}()
	approved := false
	for i := 0; i < 200 && !approved; i++ {
		_, out := h.run(t, "", "approvals", "list", "--json")
		var list []map[string]any
		if jsonInto(out, &list) && len(list) > 0 {
			if code, out := h.run(t, "", "approvals", "approve", list[0]["id"].(string)); code != ExitOK {
				t.Fatal(out)
			}
			approved = true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !approved {
		t.Fatal("no pending approval")
	}
	r := <-done
	if r.code != ExitOK {
		t.Fatal(r.code, r.out)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, pdfFile) {
		t.Fatalf("%q %v", got, err)
	}
	if jsonOf(t, r.out)["result"].(map[string]any)["file"] != path {
		t.Fatal(r.out)
	}
}

func jsonInto(s string, v any) bool { return json.Unmarshal([]byte(s), v) == nil }

// Файл, чей sha256 не совпал с result.sha256, — ошибка, а не тихий успех.
func TestOutSHA256Mismatch(t *testing.T) {
	res := map[string]any{"status": "ok", "result": map[string]any{"sha256": strings.Repeat("0", 64)},
		"content": []any{map[string]any{"type": "resource", "data": base64.StdEncoding.EncodeToString(pdfFile)}}}
	if err := saveImage(res, filepath.Join(t.TempDir(), "a.pdf"), ""); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatal(err)
	}
}
