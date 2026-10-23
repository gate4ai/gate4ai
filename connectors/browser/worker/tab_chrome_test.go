// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// TestTabRealChrome — на настоящем Chrome (оба движка): временная вкладка при
// открытой сессии читает другую страницу и закрывается; адрес, текст и
// состояние страницы сессии (значение поля) не меняются.
func TestTabRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/status" {
			w.Write([]byte("<!doctype html><title>Status</title><p>All systems operational"))
			return
		}
		w.Write([]byte("<!doctype html><title>Cabinet</title><p>Session page<input id=q>"))
	}))
	defer srv.Close()
	sites := domain.Sites{Domains: []string{"127.0.0.1"}}
	for _, engine := range []string{"cdp", "stagehand"} {
		t.Run(engine, func(t *testing.T) {
			dir := t.TempDir()
			wrapper := filepath.Join(dir, "chrome")
			os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			svc := New()
			defer svc.Close()
			st := Settings{Engine: engine, ChromePath: wrapper, ProfileDir: filepath.Join(dir, "profile")}
			s, err := svc.Acquire(ctx, st, 0)
			if err != nil {
				t.Skip("chrome did not start:", err)
			}
			defer s.Release()
			if _, err := s.Open(ctx, srv.URL+"/cabinet", sites); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Act(ctx, ActRequest{Selector: "#q", Method: ActFill, Value: "draft"}); err != nil {
				t.Fatal(err)
			}
			tab, err := svc.Tab(ctx, st, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tab.Open(ctx, srv.URL+"/status", sites); err != nil {
				t.Fatal(err)
			}
			if p, err := tab.Observe(ctx, 0); err != nil || p.Title != "Status" || !strings.Contains(p.Text, "operational") {
				t.Fatal(p, err)
			}
			tab.Release()
			p, err := s.Observe(ctx, 0)
			if err != nil || p.URL != srv.URL+"/cabinet" || p.Title != "Cabinet" {
				t.Fatal("the session's page changed:", p, err)
			}
			var v string
			if err := s.Engine.Eval(ctx, "document.getElementById('q').value", &v); err != nil || v != "draft" {
				t.Fatal("the session's field changed:", v, err)
			}
			if len(svc.tabsCh()) != 0 {
				t.Fatal("the tab is released")
			}
		})
	}
}
