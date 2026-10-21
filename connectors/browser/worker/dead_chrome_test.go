// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestClosedRealChrome — окно Chrome закрыли: вызовы старого движка не висят,
// следующая сессия запускает Chrome заново.
func TestClosedRealChrome(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("needs Chrome")
	}
	chrome, err := FindChrome()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "chrome")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nexec '"+chrome+"' --headless=new --disable-gpu \"$@\"\n"), 0o755)
	var procs []*os.Process
	svc := &Service{NewEngine: DefaultEngine, Launch: func(ctx context.Context, c, p string, extra ...string) (string, *os.Process, error) {
		u, proc, err := Launch(ctx, c, p, extra...)
		if proc != nil {
			procs = append(procs, proc)
		}
		return u, proc, err
	}}
	t.Cleanup(func() {
		svc.Close()
		for _, p := range procs {
			StopChrome(p, 3*time.Second)
		}
	})
	st := Settings{Engine: "cdp", ChromePath: wrapper, ProfileDir: filepath.Join(dir, "profile")}
	ctx := context.Background() // как у нативного пакета: без срока
	s, err := svc.Acquire(ctx, st, 0)
	if err != nil {
		t.Skip("chrome did not start:", err)
	}
	StopChrome(procs[0], time.Second) // окно закрыли
	done := make(chan error, 1)
	go func() { _, err := s.Observe(ctx, 0); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("observe on a closed Chrome")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("call to a closed Chrome hangs")
	}
	s.Release()
	s, err = svc.Acquire(ctx, st, 0)
	if err != nil {
		t.Fatal("Chrome is not relaunched:", err)
	}
	defer s.Release()
	if len(procs) != 2 {
		t.Fatal("launches:", len(procs))
	}
	if _, err := s.x.Engine.URL(ctx); err != nil {
		t.Fatal("the new Chrome does not answer:", err)
	}
}
