// SPDX-License-Identifier: Apache-2.0

package native

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

type echo struct {
	mu    sync.Mutex
	ended []string
}

func (e *echo) Execute(_ context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	f, err := h.FSRead("~/a", 10)
	if err != nil {
		return abi.ExecResult{}, err
	}
	caller := ""
	if r.Caller != nil {
		caller = r.Caller.Connector
	}
	return abi.ExecResult{Structured: map[string]any{"data": string(f.Data), "caller": caller}}, nil
}

func (e *echo) Verify(context.Context, *abi.Host, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified}, nil
}

func (e *echo) EndCall(call string) { e.mu.Lock(); e.ended = append(e.ended, call); e.mu.Unlock() }

func TestServe(t *testing.T) {
	m, err := manifest.Parse([]byte("connector: echo\nruntime: native\nplatforms: [linux/amd64]\nactions:\n  - { name: read, class: read }\n"))
	if err != nil {
		t.Fatal(err)
	}
	toProc, procIn := io.Pipe()
	procOut, fromProc := io.Pipe()
	c := &echo{}
	served := make(chan error, 1)
	go func() { served <- ServeIO(m, c, toProc, fromProc) }()

	var hostCalls []string
	host := NewPeer(procOut, procIn, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var hp HostParams
		json.Unmarshal(params, &hp)
		hostCalls = append(hostCalls, method+":"+hp.Call+":"+hp.Fn)
		return RawResponse(abi.OK(abi.FSReadResponse{Data: []byte("hello")})), nil
	})
	go host.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := host.Call(ctx, MethodInitialize, InitializeParams{Protocol: 99}, nil); err == nil || !strings.Contains(err.Error(), "protocol 99") {
		t.Fatal("bad protocol accepted:", err)
	}
	var ini InitializeResult
	if err := host.Call(ctx, MethodInitialize, InitializeParams{Protocol: Protocol}, &ini); err != nil || ini.Manifest.Connector != "echo" {
		t.Fatal(ini, err)
	}
	req, _ := json.Marshal(abi.ExecRequest{Action: "read", Caller: &abi.Caller{Connector: "tbank", Call: "root1"}})
	var raw RawResponse
	if err := host.Call(ctx, MethodExecute, CallParams{Call: "c1", Request: req}, &raw); err != nil {
		t.Fatal(err)
	}
	var res abi.ExecResult
	if err := abi.Decode(raw, &res); err != nil {
		t.Fatal(err)
	}
	st := res.Structured.(map[string]any)
	if st["data"] != "hello" || st["caller"] != "tbank" || len(hostCalls) != 1 || hostCalls[0] != "host:c1:fs_read" {
		t.Fatal(st, hostCalls)
	}
	req, _ = json.Marshal(abi.ExecRequest{Action: "nope"})
	if err := host.Call(ctx, MethodExecute, CallParams{Call: "c2", Request: req}, &raw); err != nil {
		t.Fatal(err)
	}
	if err := abi.Decode(raw, nil); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatal("unknown action:", err)
	}
	host.Notify(MethodEndCall, EndCallParams{Call: "root1"})
	host.Notify(MethodShutdown, struct{}{})
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown ignored")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.ended) != 1 || c.ended[0] != "root1" {
		t.Fatal("end_call:", c.ended)
	}
}

func TestPeerClosed(t *testing.T) {
	r, w := io.Pipe()
	p := NewPeer(r, io.Discard, nil)
	done := make(chan struct{})
	go func() { p.Run(); close(done) }()
	errc := make(chan error, 1)
	go func() { errc <- p.Call(context.Background(), "x", nil, nil) }()
	time.Sleep(50 * time.Millisecond)
	w.Close()
	<-done
	if err := <-errc; err != ErrClosed {
		t.Fatal(err)
	}
	if err := p.Call(context.Background(), "x", nil, nil); err != ErrClosed {
		t.Fatal(err)
	}
}

// blocker — действие ждёт отмены своего контекста.
type blocker struct{ started chan struct{} }

func (b blocker) Execute(ctx context.Context, _ *abi.Host, _ abi.ExecRequest) (abi.ExecResult, error) {
	b.started <- struct{}{}
	<-ctx.Done()
	return abi.ExecResult{}, abi.Errorf(abi.CodeCancelled, "%v", ctx.Err())
}

func (blocker) Verify(context.Context, *abi.Host, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func TestServeCancelAndDeadline(t *testing.T) {
	m, err := manifest.Parse([]byte("connector: slow\nruntime: native\nplatforms: [linux/amd64]\nlimits: { timeout_s: 1 }\nactions:\n  - { name: read, class: read }\n"))
	if err != nil {
		t.Fatal(err)
	}
	toProc, procIn := io.Pipe()
	procOut, fromProc := io.Pipe()
	b := blocker{started: make(chan struct{}, 2)}
	go ServeIO(m, b, toProc, fromProc)
	host := NewPeer(procOut, procIn, nil)
	go host.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := json.Marshal(abi.ExecRequest{Action: "read"})
	call := func(id string) (error, time.Duration) {
		start := time.Now()
		var raw RawResponse
		if err := host.Call(ctx, MethodExecute, CallParams{Call: id, Request: req}, &raw); err != nil {
			return err, time.Since(start)
		}
		return abi.Decode(raw, nil), time.Since(start)
	}
	// Хост отменил вызов — действие узнаёт об этом сразу.
	done := make(chan error, 1)
	go func() { err, _ := call("c1"); done <- err }()
	<-b.started
	host.Notify(MethodCancel, CancelParams{Call: "c1"})
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), abi.CodeCancelled) {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancel is not delivered to the action")
	}
	// Хост молчит — срок вызова из limits.timeout_s.
	go func() { <-b.started }()
	if err, took := call("c2"); err == nil || took > 3*time.Second {
		t.Fatal("deadline:", err, took)
	}
}

// Собеседник перестал читать: Call и NotifyContext возвращаются по сроку, а не
// висят на записи в полный pipe.
func TestSendDoesNotHangOnStuckReader(t *testing.T) {
	inR, _ := io.Pipe()
	_, outW := io.Pipe() // никто не читает: запись блокируется
	p := NewPeer(inR, outW, nil)
	go p.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.Call(ctx, MethodExecute, struct{}{}, nil); err == nil {
		t.Fatal("call to a stuck peer succeeded")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if err := p.NotifyContext(ctx2, MethodShutdown, struct{}{}); err == nil {
		t.Fatal("notify to a stuck peer succeeded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatal("send hung for", d)
	}
}
