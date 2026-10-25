// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"errors"
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

// TestDialogsRealChrome — страница с несохранёнными изменениями (beforeunload):
// reload по просьбе агента проходит, а не висит; alert прерывает действие
// ответом DialogError, остальные действия отвечают им сразу, пока агент не
// ответит на диалог; после ответа сессия работает. Две сессии — две вкладки.
func TestDialogsRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/other" {
			w.Write([]byte("<!doctype html><title>Other</title><p>Other page"))
			return
		}
		w.Write([]byte(`<!doctype html><title>Form</title><input id=q><button id=hi onclick="alert('Saved?')">Hi</button>
<button id=ask onclick="document.title = confirm('Leave?') ? 'yes' : 'no'">Ask</button>
<script>addEventListener('beforeunload', e => { if (document.getElementById('q').value) { e.preventDefault(); e.returnValue = 'unsaved'; } })</script>`))
	}))
	defer srv.Close()
	sites := domain.Sites{Domains: []string{"127.0.0.1"}}
	for _, engine := range []string{"cdp"} {
		t.Run(engine, func(t *testing.T) {
			dir := t.TempDir()
			wrapper := filepath.Join(dir, "chrome")
			os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			svc := New()
			defer svc.Close()
			st := Settings{ChromePath: wrapper, ProfileDir: filepath.Join(dir, "profile")}
			s, err := svc.Acquire(ctx, st, 0)
			if err != nil {
				t.Skip("chrome did not start:", err)
			}
			defer s.Release()
			if _, err := s.Open(ctx, srv.URL+"/form", sites); err != nil {
				t.Fatal(err)
			}
			// Ввод с клавиатуры: у страницы есть действие пользователя, и
			// beforeunload показывает диалог.
			if _, err := s.Act(ctx, ActRequest{Selector: "#q", Method: ActPress, Value: "a"}); err != nil {
				t.Fatal(err)
			}
			// Переход не по просьбе агента (navigate false): beforeunload — диалог агенту.
			err = s.Run(ctx, false, func(ctx context.Context) error { _, err := s.Reload(ctx); return err })
			var de *DialogError
			if !errors.As(err, &de) || de.Dialog.Type != "beforeunload" {
				t.Fatal("beforeunload:", err)
			}
			if _, _, err := s.HandleDialog(ctx, false, ""); err != nil { // остаться
				t.Fatal(err)
			}
			start := time.Now()
			rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
			err = s.Run(rctx, true, func(ctx context.Context) error { _, err := s.Reload(ctx); return err })
			rcancel()
			if err != nil || time.Since(start) > 25*time.Second {
				t.Fatalf("reload with beforeunload: %v in %s", err, time.Since(start))
			}
			if p, err := s.Observe(ctx, 0); err != nil || p.Title != "Form" {
				t.Fatal("the session works after reload:", p, err)
			}
			// alert во время act: действие прерывается диалогом, а не висит.
			start = time.Now()
			err = s.Run(ctx, false, func(ctx context.Context) error {
				_, err := s.Act(ctx, ActRequest{Selector: "#hi", Method: ActClick})
				return err
			})
			if !errors.As(err, &de) || de.Dialog.Type != "alert" || de.Dialog.Message != "Saved?" || time.Since(start) > 15*time.Second {
				t.Fatalf("alert: %v in %s", err, time.Since(start))
			}
			err = s.Run(ctx, false, func(ctx context.Context) error { _, err := s.Observe(ctx, 0); return err })
			if !errors.As(err, &de) || !strings.Contains(err.Error(), "browser.dialog") {
				t.Fatal("an open dialog answers at once:", err)
			}
			if d, u, err := s.HandleDialog(ctx, true, ""); err != nil || d.Type != "alert" || !strings.HasSuffix(u, "/form") {
				t.Fatal(d, u, err)
			}
			if _, _, err := s.HandleDialog(ctx, true, ""); !errors.Is(err, ErrNoDialog) {
				t.Fatal("no dialog:", err)
			}
			// confirm: агент отклоняет — страница видит false.
			_ = s.Run(ctx, false, func(ctx context.Context) error {
				_, err := s.Act(ctx, ActRequest{Selector: "#ask", Method: ActClick})
				return err
			})
			if d := s.Dialog(); d == nil || d.Type != "confirm" {
				t.Fatal("confirm:", d)
			}
			if _, _, err := s.HandleDialog(ctx, false, ""); err != nil {
				t.Fatal(err)
			}
			if title, _ := s.Engine.Title(ctx); title != "no" {
				t.Fatal("confirm dismissed:", title)
			}
			// Вторая сессия — своя вкладка: страница первой не меняется.
			s2, err := svc.Acquire(ctx, st, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s2.Open(ctx, srv.URL+"/other", sites); err != nil {
				t.Fatal(err)
			}
			if p, err := s.Observe(ctx, 0); err != nil || !strings.HasSuffix(p.URL, "/form") {
				t.Fatal("the first session's page:", p, err)
			}
			if p, err := s2.Observe(ctx, 0); err != nil || p.Title != "Other" {
				t.Fatal("the second session's page:", p, err)
			}
			s2.Release()
			if s.Gone(ctx) {
				t.Fatal("the first session is alive")
			}
		})
	}
}
