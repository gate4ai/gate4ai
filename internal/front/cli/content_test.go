// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Картинка в ответе (page_screenshot) — файл: --out или временный; в JSON —
// путь и размер вместо base64, путь первого — в result.file.
func TestSaveContent(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	res := func() map[string]any {
		return map[string]any{"status": "ok", "content": []any{
			map[string]any{"type": "image", "mime_type": "image/png", "data": base64.StdEncoding.EncodeToString(png)},
			map[string]any{"type": "text", "text": "hi"}}}
	}
	out := filepath.Join(t.TempDir(), "shot.png")
	r := res()
	if err := saveContent(r, out, "browser.page_screenshot"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != string(png) {
		t.Fatal(b, err)
	}
	blk := r["content"].([]any)[0].(map[string]any)
	if blk["data"] != nil || blk["path"] != out || blk["size"] != len(png) || r["result"].(map[string]any)["file"] != out {
		t.Fatalf("%+v", r)
	}
	r = res()
	if err := saveContent(r, "", "browser.page_screenshot"); err != nil {
		t.Fatal(err)
	}
	p := r["result"].(map[string]any)["file"].(string)
	defer os.Remove(p)
	if !strings.HasSuffix(p, ".png") || !strings.Contains(filepath.Base(p), "gate4ai-browser-page_screenshot-") {
		t.Fatal(p)
	}
	if b, _ := os.ReadFile(p); string(b) != string(png) {
		t.Fatal("temp file")
	}
}
