// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

func envOf(goos string, vars map[string]string, xvfb bool) DisplayEnv {
	return DisplayEnv{GOOS: goos, Getenv: func(k string) string { return vars[k] },
		Look: func(name string) (string, error) {
			if name == "Xvfb" && xvfb {
				return "/usr/bin/Xvfb", nil
			}
			return "", exec.ErrNotFound
		}}
}

func TestResolveDisplay(t *testing.T) {
	desktop := map[string]string{"DISPLAY": ":0"}
	for _, c := range []struct {
		mode, goos string
		vars       map[string]string
		xvfb       bool
		want, err  string
	}{
		{"", "linux", desktop, false, DisplayHost, ""},
		{"auto", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true, DisplayHost, ""},
		{"auto", "linux", nil, true, DisplayVirtual, ""},
		{"auto", "linux", nil, false, DisplayHeadless, ""},
		{"auto", "darwin", nil, false, DisplayHost, ""},
		{"auto", "windows", nil, false, DisplayHost, ""},
		{"auto", "freebsd", nil, true, DisplayVirtual, ""},
		{"virtual", "linux", desktop, true, DisplayVirtual, ""},
		{"virtual", "linux", nil, false, "", "needs Xvfb"},
		{"virtual", "darwin", nil, true, "", "for Linux"},
		{"headless", "windows", nil, false, DisplayHeadless, ""},
		{"host", "linux", nil, true, "", "graphical session"},
		{"host", "darwin", nil, false, DisplayHost, ""},
		{"vnc", "linux", nil, false, "", "must be one of"},
	} {
		got, err := ResolveDisplay(c.mode, envOf(c.goos, c.vars, c.xvfb))
		if got != c.want || (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s on %s %v xvfb=%v: %q %v, want %q %q", c.mode, c.goos, c.vars, c.xvfb, got, err, c.want, c.err)
		}
	}
}

func TestXauthEntry(t *testing.T) {
	cookie := bytes.Repeat([]byte{7}, 16)
	b := XauthEntry(cookie)
	want := append([]byte{0xff, 0xff, 0, 0, 0, 0, 0, 18}, "MIT-MAGIC-COOKIE-1"...)
	want = append(append(want, 0, 16), cookie...)
	if !bytes.Equal(b, want) {
		t.Fatalf("%x\n%x", b, want)
	}
}

// Сервер без графики и без Xvfb: Chrome — в --headless=new, режим виден владельцу.
func TestServiceHeadlessFallback(t *testing.T) {
	f := newFakeChrome(t)
	var mu sync.Mutex
	var flags, env []string
	svc := &Service{NewEngine: DefaultEngine, Env: envOf("linux", nil, false),
		Launch: func(_ context.Context, _, _ string, e []string, extra ...string) (string, *os.Process, error) {
			mu.Lock()
			flags, env = extra, e
			mu.Unlock()
			return f.srv.URL, &os.Process{Pid: -1}, nil
		}}
	s, err := svc.Acquire(context.Background(), Settings{ChromePath: "/nonexistent/chrome", ProfileDir: t.TempDir()}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(flags, " ") != "--headless=new" || len(env) != 0 {
		t.Fatal(flags, env)
	}
	if m := svc.DisplayMode(); m != DisplayHeadless {
		t.Fatal(m)
	}
	svc.proc = nil // фейковый процесс: Close не должен его останавливать
	// virtual без Xvfb — ошибка с подсказкой, а не запуск.
	svc2 := &Service{NewEngine: DefaultEngine, Env: envOf("linux", nil, false), Launch: LaunchEnv}
	if _, err := svc2.Acquire(context.Background(), Settings{ChromePath: "/nonexistent/chrome", Display: DisplayVirtual}, 0); err == nil || !strings.Contains(err.Error(), "apt install xvfb") {
		t.Fatal(err)
	}
}

// Настоящий Chrome без экрана: сайт видит UA без «HeadlessChrome», client
// hints — с полными версиями.
func TestHeadlessRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	if _, err := FindChrome(); err != nil {
		t.Skip(err)
	}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.UserAgent()+" | "+r.Header.Get("Sec-CH-UA"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!doctype html><title>ua</title>"))
	}))
	defer srv.Close()
	for _, engine := range []string{"cdp"} {
		t.Run(engine, func(t *testing.T) {
			svc := &Service{NewEngine: DefaultEngine, Launch: LaunchEnv, Env: envOf(runtime.GOOS, nil, false)}
			if runtime.GOOS == "darwin" {
				svc.Env.GOOS = "linux" // auto на macOS — host; здесь нужен запасной режим
			}
			defer svc.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			s, err := svc.Acquire(ctx, Settings{ProfileDir: filepath.Join(t.TempDir(), "p")}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Release()
			if svc.DisplayMode() != DisplayHeadless {
				t.Fatal(svc.DisplayMode())
			}
			if _, err := s.Open(ctx, srv.URL+"/", domain.Sites{Domains: []string{"127.0.0.1"}}); err != nil {
				t.Fatal(err)
			}
			var ua string
			if err := s.Executor.Engine.Eval(ctx, "navigator.userAgent", &ua); err != nil || strings.Contains(ua, "Headless") || !strings.Contains(ua, "Chrome/") {
				t.Fatal(ua, err)
			}
			mu.Lock()
			last := seen[len(seen)-1]
			mu.Unlock()
			if strings.Contains(last, "Headless") {
				t.Fatal("the site saw headless:", last)
			}
			var full int
			if err := s.Executor.Engine.Eval(ctx, "navigator.userAgentData.getHighEntropyValues(['fullVersionList']).then(h => h.fullVersionList.length)", &full); err != nil || full == 0 {
				t.Fatal("client hints without versions:", full, err)
			}
		})
	}
}

// Настоящий Xvfb (если установлен): Chrome на виртуальном дисплее, обычный, не headless.
func TestVirtualDisplayRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("needs Chrome and Xvfb")
	}
	if _, err := FindChrome(); err != nil {
		t.Skip(err)
	}
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is not installed")
	}
	svc := &Service{NewEngine: DefaultEngine, Launch: LaunchEnv}
	defer svc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s, err := svc.Acquire(ctx, Settings{Display: DisplayVirtual, ProfileDir: filepath.Join(t.TempDir(), "p")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release()
	var ua string
	if err := s.Executor.Engine.Eval(ctx, "navigator.userAgent", &ua); err != nil || strings.Contains(ua, "Headless") {
		t.Fatal(ua, err)
	}
	x := svc.disp.x
	if !x.Alive() || !strings.HasPrefix(x.Display, ":") {
		t.Fatal("Xvfb is not running", x)
	}
	if fi, err := os.Stat(x.Auth); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal(fi, err)
	}
	s.Release()
	svc.Close()
	if x.Alive() {
		t.Fatal("Xvfb outlived the worker")
	}
}
