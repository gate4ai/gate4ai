// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tagsPage — поле тегов (тег — по запятой или Enter в keydown, как
// «Ключевые слова» на Хабре), поле пароля и длинная страница.
const tagsPage = `<!doctype html><meta charset=utf-8><title>Теги</title>
<style>body{margin:0;background:#fff} .chip{display:block}</style>
<div id=chips></div><input id=kw placeholder="Ключевые слова">
<form id=search style="display:none"><input name=q></form>
<form id=login><input type=password id=pw value="pass-e2e-777" style="width:200px;height:40px;padding:0;background:#fff;color:#fff;border:0"><button type=button id=go>Отправить</button></form>
<div style="height:3000px"></div><p>низ</p>
<script>
const kw = document.getElementById('kw');
kw.addEventListener('keydown', e => {
  if (e.key !== ',' && e.key !== 'Enter') return;
  e.preventDefault();
  const v = kw.value.trim();
  if (v) { const s = document.createElement('span'); s.className = 'chip'; s.textContent = v; document.getElementById('chips').appendChild(s); }
  kw.value = '';
});
</script>`

// Агент через CLI: type создаёт три тега одним вызовом, press запятой — ещё
// один; page_screenshot пишет PNG (--out, окно, вся страница, элемент); клик
// без ожидаемого результата отвечает not_found с путём к снимку; в
// доказательствах журнала поле пароля закрашено.
func TestBrowserTypeAndScreenshot(t *testing.T) {
	t.Parallel()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		if chrome, err = exec.LookPath("chromium"); err != nil {
			t.Skip("needs Chrome")
		}
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, tagsPage)
	}))
	t.Cleanup(site.Close)
	g := startGate(t)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu --window-size=1000,700 \"$@\"\n"), 0o755)
	cfg := fmt.Sprintf(`agents:
  claude-ai: { profile: assistant }
profiles:
  assistant:
    connectors: [browser]
    rules:
      - { id: browser-ok, action: "browser.*", effect: allow }
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
	if r := g.run("", "", "policy", "set", cfgFile, "-m", "type e2e"); r.code != 0 {
		t.Fatalf("policy: %d %s", r.code, r.out)
	}
	token := g.run("", "", "agents", "token", "claude-ai").json(t)["token"].(string)
	run := func(args ...string) map[string]any {
		t.Helper()
		r := g.run(token, "", args...)
		res := r.json(t)
		if r.code != 0 || res["status"] != "ok" {
			t.Fatalf("%v: %d %s", args, r.code, r.out)
		}
		if strings.Contains(r.out, "pass-e2e-777") {
			t.Fatalf("the password leaked: %s", r.out)
		}
		m, _ := res["result"].(map[string]any)
		return m
	}
	session := run("browser", "open", "--url", site.URL+"/")["session"].(string)

	res := run("browser", "act", "--session", session, "--selector", "#kw", "--method", "type", "--value", "a, b, c", "--submit", "Enter")
	added, _ := res["changes"].(map[string]any)["added"].([]any)
	if len(added) != 3 || added[0].(map[string]any)["text"] != "a" || added[2].(map[string]any)["text"] != "c" {
		t.Fatalf("type: %v", res)
	}
	run("browser", "act", "--session", session, "--selector", "#kw", "--method", "type", "--value", "сжатие данных")
	res = run("browser", "act", "--session", session, "--selector", "#kw", "--method", "press", "--value", ",")
	added, _ = res["changes"].(map[string]any)["added"].([]any)
	if len(added) != 1 || added[0].(map[string]any)["text"] != "сжатие данных" {
		t.Fatalf("press comma: %v", res)
	}
	if text := run("browser", "observe", "--session", session, "--selector", "#chips")["text"]; text != "a\nb\nc\nсжатие данных" {
		t.Fatalf("tags on the page: %q", text)
	}

	isPNG := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil || !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("%s is not a PNG: %v", path, err)
		}
		return b
	}
	out := filepath.Join(t.TempDir(), "shot.png")
	if res := run("browser", "page_screenshot", "--session", session, "--out", out); res["file"] != out || res["mode"] != "viewport" {
		t.Fatalf("--out: %v", res)
	}
	isPNG(out)
	view := run("browser", "page_screenshot", "--session", session, "--viewport")
	isPNG(view["file"].(string))
	full := run("browser", "page_screenshot", "--session", session, "--full_page")
	if img, err := png.DecodeConfig(bytes.NewReader(isPNG(full["file"].(string)))); err != nil || img.Height < 3000 {
		t.Fatalf("full page: %v %v", img, err)
	}
	form := run("browser", "page_screenshot", "--session", session, "--selector", "form")
	isPNG(form["file"].(string))
	run("browser", "page_screenshot", "--session", session, "--selector", "#pw")
	r := g.run(token, "", "browser", "page_screenshot", "--session", session, "--selector", "#search")
	if !strings.Contains(r.out, "display:none") {
		t.Fatalf("a hidden element is explained: %s", r.out)
	}

	// Клик без ожидаемого результата: not_found и снимок окна.
	r = g.run(token, "", "browser", "act", "--session", session, "--selector", "#go", "--method", "click",
		"--expect_selector", "#sent", "--expect_timeout_ms", "300", "--screenshot")
	msg, _ := errOf(r.json(t))["message"].(string)
	_, path, ok := strings.Cut(msg, "screenshot of the page: ")
	if r.code == 0 || !strings.Contains(msg, "no expected effect") || !ok {
		t.Fatalf("failed click: %d %s", r.code, r.out)
	}
	isPNG(strings.TrimSpace(path))
	run("browser", "close", "--session", session)

	// Доказательства в журнале: снимок поля пароля — без светлых пикселей.
	arch := filepath.Join(t.TempDir(), "journal.tar.gz")
	if r := g.run("", "", "export", "-o", arch); r.code != 0 {
		t.Fatal(r.out)
	}
	pw := 0
	for name, b := range untar(t, arch) {
		if !strings.HasPrefix(name, "evidence/") || !bytes.HasPrefix(b, []byte("\x89PNG")) {
			continue
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != 200 || img.Bounds().Dy() != 40 {
			continue
		}
		pw++
		for y := 0; y < 40; y++ {
			for x := 0; x < 200; x++ {
				if r, g, bl, _ := img.At(x, y).RGBA(); r>>8 > 40 || g>>8 > 40 || bl>>8 > 40 {
					t.Fatalf("the password field is not covered in the evidence at %d,%d", x, y)
				}
			}
		}
	}
	if pw != 1 {
		t.Fatalf("evidence with the password field: %d", pw)
	}
}

// untar — файлы архива tar.gz.
func untar(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = b
	}
}
