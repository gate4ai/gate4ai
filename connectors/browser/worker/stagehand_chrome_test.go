// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestStagehandRealChrome — движок Stagehand подключается к Chrome, запущенному
// нашим Launch (расширению нужен --remote-allow-origins).
func TestStagehandRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><title>Stagehand</title><p>ok"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cdpURL, proc, err := Launch(ctx, wrapper, filepath.Join(dir, "profile"), chromeFlags(Settings{Engine: "stagehand"})...)
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	defer StopChrome(proc, 3*time.Second)
	eng, err := DialStagehand(ctx, StagehandConfig{CDPURL: cdpURL})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if title, err := eng.Title(ctx); err != nil || title != "Stagehand" {
		t.Fatal(title, err)
	}
	// Запрос со страницы: Evaluate дожидается промиса fetch; JSON-страница —
	// тело из DOM без просмотрщика Chrome.
	x := &Executor{Engine: eng, Sites: domain.Sites{Domains: []string{"127.0.0.1"}}}
	if res, err := x.Fetch(ctx, FetchRequest{Method: "GET", URL: srv.URL + "/api"}); err != nil || res.Status != 200 || res.JSON.(map[string]any)["ok"] != true {
		t.Fatal(res, err)
	}
	if _, err := x.Open(ctx, srv.URL+"/api"); err != nil {
		t.Fatal(err)
	}
	if b, ok, err := x.RawDocument(ctx, 100); err != nil || !ok || b.Body != `{"ok":true}` {
		t.Fatal(b, ok, err)
	}
}
