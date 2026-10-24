// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	check := func(file any) {
		t.Helper()
		path, _ := file.(string)
		if filepath.Dir(path) != shotDir || !strings.HasPrefix(filepath.Base(path), id+"-") {
			t.Fatal("screenshot outside the session folder:", file)
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != "\x89PNG-fake" {
			t.Fatal("screenshot file:", err)
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
		if fmt.Sprint(m["bytes"]) != "9" || res.Evidence[0].MIME != "image/png" || len(res.Content) != 1 {
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
