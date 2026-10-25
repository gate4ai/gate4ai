// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestLaunchRealChrome — настоящий Chrome запускается нашим Launch и отвечает
// по CDP на всех ОС: на Unix — pipe и Mux с токеном, на Windows — порт на
// 127.0.0.1 без --remote-allow-origins (подключение воркера идёт без Origin).
// В GitHub Actions это единственный тест с Chrome на Windows и macOS.
func TestLaunchRealChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><title>Launched</title><p id=p>ok"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cdpURL, proc, err := Launch(ctx, chrome, filepath.Join(t.TempDir(), "profile"), "--headless=new", "--disable-gpu")
	if err != nil {
		t.Fatalf("chrome did not start on %s: %v", runtime.GOOS, err)
	}
	defer StopChrome(proc, 3*time.Second)
	eng, err := DialCDP(ctx, cdpURL)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if title, err := eng.Title(ctx); err != nil || title != "Launched" {
		t.Fatal(title, err)
	}
	var text string
	if err := eng.Eval(ctx, "document.getElementById('p').textContent", &text); err != nil || text != "ok" {
		t.Fatal(text, err)
	}
	// Вторая вкладка того же Chrome — так открываются сеансы агентов.
	tab, err := eng.NewTab(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer tab.Close()
	if err := tab.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if err := eng.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}
