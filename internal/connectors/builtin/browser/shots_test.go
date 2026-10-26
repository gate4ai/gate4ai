// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// testPNG — настоящий PNG w×h: фейковые движки отдают его вместо
// строки-заглушки, чтобы сигнатура и размер в ответе проверялись честно.
func testPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// Снимки сеанса агента — файлы в каталоге данных (browser-shots), путь — в
// ответе; act --screenshot прикладывает снимок окна, неудачный expect_* —
// путь к снимку в ошибке; у страницы вызывающего коннектора — только content.
func TestAgentScreenshots(t *testing.T) {
	t.Setenv("GATE4AI_HOME", t.TempDir())
	h, b, eng := setup(t, nil)
	open, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := open["session"].(string)
	shotDir := filepath.Join(os.Getenv("GATE4AI_HOME"), "browser-shots")
	want := testPNG(64, 48)
	// Файл снимка — настоящий PNG: сигнатура, размер как у ответа движка
	// (не 4 байта заголовка), открывается декодером.
	check := func(file any) {
		t.Helper()
		path, _ := file.(string)
		if filepath.Dir(path) != shotDir || !strings.HasPrefix(filepath.Base(path), id+"-") {
			t.Fatal("screenshot outside the session folder:", file)
		}
		b, err := os.ReadFile(path)
		if err != nil || !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) || len(b) != len(want) || !bytes.Equal(b, want) {
			t.Fatalf("screenshot file %s: %d bytes, %v", path, len(b), err)
		}
		if img, err := png.Decode(bytes.NewReader(b)); err != nil || img.Bounds().Dx() != 64 || img.Bounds().Dy() != 48 {
			t.Fatal("screenshot file is not a decodable PNG:", err)
		}
	}
	for _, args := range []map[string]any{{}, {"viewport": true}, {"full_page": true}, {"selector": "form"}} {
		args["session"] = id
		res, err := h.Execute(b, "page_screenshot", args) // ответ — по output_schema
		if err != nil {
			t.Fatal(args, err)
		}
		m := res.Structured.(map[string]any)
		check(m["file"])
		if fmt.Sprint(m["bytes"]) != fmt.Sprint(len(want)) || fmt.Sprint(m["width"]) != "64" || fmt.Sprint(m["height"]) != "48" || res.Evidence[0].MIME != "image/png" || len(res.Content) != 1 {
			t.Fatalf("%v: %+v", args, res)
		}
	}
	if _, err := exec(b, "page_screenshot", map[string]any{"session": id, "selector": "form", "full_page": true}, nil); err == nil {
		t.Fatal("selector with full_page")
	}
	res, err := h.Execute(b, "act", map[string]any{"session": id, "selector": "#kw", "method": "type", "value": "a, b, c", "submit": "Enter", "screenshot": true})
	if err != nil {
		t.Fatal(err)
	}
	if ch := fmt.Sprint(res.Structured.(map[string]any)["changes"]); ch != "map[added:[map[tag:span text:a]] closed:[map[role:listbox tag:ul]]]" {
		t.Fatal("changes:", ch)
	}
	check(res.Structured.(map[string]any)["screenshot"].(map[string]any)["file"])
	if len(res.Evidence) != 1 || !strings.Contains(strings.Join(eng.keys, " "), "type:a, b, c Enter") {
		t.Fatalf("%+v %v", res, eng.keys)
	}
	_, err = agentExec(t, b, "act", map[string]any{"session": id, "selector": "#pay", "method": "click", "expect_selector": "#missing", "expect_timeout_ms": 1.0})
	if err == nil || !strings.Contains(err.Error(), "no expected effect") || !strings.Contains(err.Error(), "screenshot of the page: "+shotDir) {
		t.Fatal(err)
	}
	if _, err := agentExec(t, b, "close", map[string]any{"session": id}); err != nil {
		t.Fatal(err)
	}
}

// Движок отдал не PNG (пустой или обрезанный ответ Page.captureScreenshot):
// агент получает io_error, файла в browser-shots нет; act --screenshot —
// та же ошибка; снимок к ошибке act не прикладывается, сама ошибка — на месте.
func TestAgentScreenshotNotPNG(t *testing.T) {
	t.Setenv("GATE4AI_HOME", t.TempDir())
	h, b, eng := setup(t, nil)
	open, err := agentExec(t, b, "open", map[string]any{"url": "https://a.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	id := open["session"].(string)
	shotDir := filepath.Join(os.Getenv("GATE4AI_HOME"), "browser-shots")
	noFiles := func() {
		t.Helper()
		if files, _ := os.ReadDir(shotDir); len(files) != 0 {
			t.Fatal("a broken screenshot was saved:", files)
		}
	}
	for _, shot := range [][]byte{[]byte("\x89PNG"), testPNG(64, 48)[:20]} {
		eng.shot = shot
		_, err := h.Execute(b, "page_screenshot", map[string]any{"session": id})
		var ae *abi.Error
		if !errors.As(err, &ae) || ae.Code != abi.CodeIO || !strings.Contains(err.Error(), "no PNG") {
			t.Fatalf("%q: %v", shot, err)
		}
		noFiles()
		if _, err := h.Execute(b, "act", map[string]any{"session": id, "selector": "#kw", "method": "click", "screenshot": true}); !errors.As(err, &ae) || ae.Code != abi.CodeIO {
			t.Fatal("act --screenshot:", err)
		}
		noFiles()
	}
	_, err = agentExec(t, b, "act", map[string]any{"session": id, "selector": "#pay", "method": "click", "expect_selector": "#missing", "expect_timeout_ms": 1.0})
	if err == nil || !strings.Contains(err.Error(), "no expected effect") || strings.Contains(err.Error(), "screenshot of the page") {
		t.Fatal(err)
	}
	noFiles()
	// saveShot сам не пишет не-PNG.
	if _, err := saveShot(id, 9, []byte("\x89PNG")); err == nil {
		t.Fatal("saveShot wrote 4 bytes")
	}
	noFiles()
	eng.shot = nil
	if _, err := agentExec(t, b, "close", map[string]any{"session": id}); err != nil {
		t.Fatal(err)
	}
}
