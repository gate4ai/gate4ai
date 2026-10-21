// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
}
