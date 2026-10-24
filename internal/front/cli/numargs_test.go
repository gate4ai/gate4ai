// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
)

// browserFake — коннектор с манифестом browser: запоминает аргументы, с
// которыми до него дошёл вызов.
type browserFake struct {
	man  manifest.Manifest
	mu   sync.Mutex
	args map[string]map[string]any // действие → аргументы
}

func (b *browserFake) Manifest() manifest.Manifest { return b.man }
func (b *browserFake) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	b.mu.Lock()
	b.args[r.Action] = r.Args
	b.mu.Unlock()
	res := abi.ExecResult{Content: []abi.Content{abi.Text("ok")}}
	switch r.Action {
	case "observe", "read":
		res.Structured = map[string]any{"url": "https://a.example.com/", "title": "t"}
	case "open":
		res.Structured = map[string]any{"session": "s1", "url": "https://a.example.com/", "title": "t"}
	case "act":
		res.Structured = map[string]any{"url": "https://a.example.com/"}
	case "page_screenshot":
		res.Structured = map[string]any{"mode": "viewport", "width": 1, "height": 1, "bytes": len(fakePNG), "file": "/daemon/browser-shots/s1-1.png"}
		res.Content = []abi.Content{abi.Image("image/png", fakePNG)}
	case "download":
		res.Structured = map[string]any{"name": "a.pdf", "mime_type": "application/pdf", "size": len(pdfFile), "sha256": pdfSum, "via": "download"}
		res.Content = []abi.Content{abi.Resource("download:a.pdf", "application/pdf", "", pdfFile)}
	}
	return res, nil
}
func (b *browserFake) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified}, nil
}

var fakePNG = []byte("\x89PNG\r\n\x1a\n-fake-image")

// --out сохраняет изображение ответа в файл на стороне CLI, путь — в
// result.file; base64 изображения в вывод не попадает.
func TestScreenshotOut(t *testing.T) {
	src, err := os.ReadFile("../../connectors/builtin/browser/manifest.yaml")
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
      - { id: all, action: "browser.*", effect: allow }
      - { id: post, action: browser.post, effect: allow } # browser.* запросы с эффектом не покрывает
`, c)
	_, out := h.run(t, "", "agents", "token", "a")
	tok := jsonOf(t, out)["token"].(string)
	path := filepath.Join(t.TempDir(), "shot.png")
	code, out := h.run(t, tok, "browser", "page_screenshot", "--session", "s1", "--viewport", "--out", path)
	if code != ExitOK {
		t.Fatal(code, out)
	}
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, fakePNG) {
		t.Fatal("the file:", err)
	}
	res := jsonOf(t, out)
	if res["result"].(map[string]any)["file"] != path || strings.Contains(out, base64.StdEncoding.EncodeToString(fakePNG)) {
		t.Fatal(out)
	}
	// Без --out — путь от коннектора, изображение — только размером.
	code, out = h.run(t, tok, "browser", "page_screenshot", "--session", "s1")
	if code != ExitOK || jsonOf(t, out)["result"].(map[string]any)["file"] != "/daemon/browser-shots/s1-1.png" ||
		strings.Contains(out, base64.StdEncoding.EncodeToString(fakePNG)) || !strings.Contains(out, `"bytes": 19`) {
		t.Fatal(code, out)
	}
	// Изображения в ответе нет — --out сообщает об ошибке.
	if code, out := h.run(t, tok, "browser", "observe", "--session", "s1", "--out", path+"2"); code == ExitOK {
		t.Fatal(out)
	}
}

// Числовые аргументы CLI (--max_chars, --timeout_ms) доходят до коннектора
// через daemon без искажения: ровно N, а не значение по умолчанию.
func TestNumericArgsReachConnector(t *testing.T) {
	src, err := os.ReadFile("../../connectors/builtin/browser/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	man, err := manifest.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	fake := &browserFake{man: man, args: map[string]map[string]any{}}
	c, err := connectors.NewInProcess(fake)
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
      - { id: all, action: "browser.*", effect: allow }
      - { id: post, action: browser.post, effect: allow } # browser.* запросы с эффектом не покрывает
`, c)
	_, out := h.run(t, "", "agents", "token", "a")
	tok := jsonOf(t, out)["token"].(string)
	for _, tc := range []struct {
		action string
		args   []string
		want   map[string]float64
	}{
		{"get", []string{"--session", "s1", "--url", "https://a.example.com/api", "--max_chars", "200000"}, map[string]float64{"max_chars": 200000}},
		{"post", []string{"--session", "s1", "--url", "https://a.example.com/api", "--body", "{}", "--max_chars=1000000"}, map[string]float64{"max_chars": 1000000}},
		{"observe", []string{"--session", "s1", "--max_chars", "123457", "--timeout_ms", "45000"}, map[string]float64{"max_chars": 123457, "timeout_ms": 45000}},
		{"read", []string{"--url", "https://a.example.com/api", "--max_chars", "200000"}, map[string]float64{"max_chars": 200000}},
		{"open", []string{"--url", "https://a.example.com/", "--timeout_ms", "59999"}, map[string]float64{"timeout_ms": 59999}},
		{"act", []string{"--session", "s1", "--selector", "#x", "--method", "click", "--timeout_ms", "60000", "--expect_timeout_ms", "1"}, map[string]float64{"timeout_ms": 60000, "expect_timeout_ms": 1}},
	} {
		code, out := h.run(t, tok, append([]string{"browser", tc.action}, tc.args...)...)
		if code != ExitOK {
			t.Fatalf("%s: %d %s", tc.action, code, out)
		}
		got := fake.args[tc.action]
		for k, n := range tc.want {
			if f, ok := got[k].(float64); !ok || f != n {
				t.Errorf("%s --%s %s: the connector got %#v", tc.action, k, strconv.FormatFloat(n, 'f', -1, 64), got[k])
			}
		}
	}
	// Больше верхней границы — ошибка аргументов, а не тихая обрезка.
	if code, out := h.run(t, tok, "browser", "get", "--session", "s1", "--url", "https://a.example.com/api", "--max_chars", "1000001"); code != ExitUsage {
		t.Fatal("max_chars above the limit:", code, out)
	}
}

var (
	pdfFile = []byte("%PDF-1.4\n\x00\xff\xfe binary\n%%EOF")
	pdfSum  = fmt.Sprintf("%x", sha256.Sum256(pdfFile))
)

// --out пишет файл из ответа (двоичный resource) на диск агента; в выводе —
// file и bytes вместо base64.
func TestOutWritesContent(t *testing.T) {
	src, err := os.ReadFile("../../connectors/builtin/browser/manifest.yaml")
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
      - { id: all, action: "browser.*", effect: allow }
`, c)
	_, out := h.run(t, "", "agents", "token", "a")
	tok := jsonOf(t, out)["token"].(string)
	path := filepath.Join(t.TempDir(), "a.pdf")
	code, out := h.run(t, tok, "browser", "download", "--session", "s1", "--selector", "#doc", "--out", path)
	if code != ExitOK {
		t.Fatal(code, out)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, pdfFile) {
		t.Fatalf("%q %v", got, err)
	}
	res := jsonOf(t, out)
	blocks, _ := res["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["file"] != path || blocks[0].(map[string]any)["data"] != nil ||
		blocks[0].(map[string]any)["bytes"] != float64(len(pdfFile)) || res["result"].(map[string]any)["file"] != path {
		t.Fatal(out)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("a downloaded file is the owner's only:", st.Mode(), err)
	}
	// Перед именем действия — тоже опция вызова.
	path2 := filepath.Join(t.TempDir(), "b.pdf")
	if code, out := h.run(t, tok, "browser", "--out", path2, "download", "--session", "s1", "--url", "https://a.example.com/a.pdf"); code != ExitOK {
		t.Fatal(code, out)
	}
	if got, _ := os.ReadFile(path2); !bytes.Equal(got, pdfFile) {
		t.Fatalf("%q", got)
	}
}
