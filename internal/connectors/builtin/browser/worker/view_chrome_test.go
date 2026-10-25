// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

const viewPage = `<!doctype html><title>view</title>
<button id=b style="position:absolute;left:0;top:0;width:50vw;height:50vh" onclick="this.textContent=+this.textContent+1">0</button>
<input id=i style="position:absolute;left:0;top:60vh;width:90vw">
<div id=n style="position:absolute;left:0;top:80vh">0</div>
<script>setInterval(()=>n.textContent=+n.textContent+1,100)</script>`

// Два сеанса — два окна: кадры идут у обоих сразу; клик и ввод человека
// доходят до страницы.
func TestViewRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	if _, err := FindChrome(); err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(viewPage))
	}))
	defer srv.Close()
	sites := domain.Sites{Domains: []string{"127.0.0.1"}}
	for _, engine := range []string{"cdp"} {
		t.Run(engine, func(t *testing.T) {
			svc := &Service{NewEngine: DefaultEngine, Launch: LaunchEnv, Env: envOf("linux", nil, false)}
			defer svc.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			st := Settings{ProfileDir: filepath.Join(t.TempDir(), "p")}
			var ss []*Session
			for range 2 {
				s, err := svc.Acquire(ctx, st, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Release()
				if _, err := s.Open(ctx, srv.URL+"/", sites); err != nil {
					t.Fatal(err)
				}
				ss = append(ss, s)
			}
			var mu sync.Mutex
			frames := map[int]int{}
			var last Frame
			var views []*View
			for i, s := range ss {
				v, err := s.View(ctx, ViewOptions{MaxWidth: 640}, func(f Frame) {
					mu.Lock()
					frames[i]++
					if i == 0 {
						last = f
					}
					mu.Unlock()
				})
				if err != nil {
					t.Fatal(err)
				}
				defer v.Close()
				views = append(views, v)
			}
			time.Sleep(1500 * time.Millisecond)
			mu.Lock()
			f0, f1, lf := frames[0], frames[1], last
			mu.Unlock()
			if f0 < 3 || f1 < 3 {
				t.Fatalf("frames of both sessions at once: %d and %d", f0, f1)
			}
			if !bytes.HasPrefix(lf.JPEG, []byte{0xff, 0xd8}) || lf.Width <= 0 || !strings.HasPrefix(lf.URL, srv.URL) {
				t.Fatalf("frame: %d bytes, %vx%v, %q", len(lf.JPEG), lf.Width, lf.Height, lf.URL)
			}
			// Клик по кнопке (левая верхняя четверть) и ввод в поле.
			v := views[0]
			for _, a := range []string{"down", "up"} {
				if err := v.Input(ctx, Input{Type: "mouse", Action: a, X: 0.2, Y: 0.2, Clicks: 1}); err != nil {
					t.Fatal(err)
				}
			}
			for _, a := range []string{"down", "up"} {
				if err := v.Input(ctx, Input{Type: "mouse", Action: a, X: 0.3, Y: 0.62, Clicks: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if err := v.Input(ctx, Input{Type: "text", Text: "привет"}); err != nil {
				t.Fatal(err)
			}
			for _, a := range []string{"down", "up"} {
				if err := v.Input(ctx, Input{Type: "key", Action: a, Key: "!", Code: "Digit1", KeyCode: 49, Text: "!", Modifiers: 8}); err != nil {
					t.Fatal(err)
				}
			}
			var got struct {
				B string `json:"b"`
				I string `json:"i"`
			}
			if err := ss[0].Executor.Engine.Eval(ctx, "({b: b.textContent, i: i.value})", &got); err != nil || got.B != "1" || got.I != "привет!" {
				t.Fatal(got, err)
			}
			var other string
			ss[1].Executor.Engine.Eval(ctx, "b.textContent", &other)
			if other != "0" {
				t.Fatal("input went to another session:", other)
			}
			if err := v.Input(ctx, Input{Type: "mouse", Action: "down", X: 2}); err == nil {
				t.Fatal("coordinates outside the page are refused")
			}
		})
	}
}
