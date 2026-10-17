// SPDX-License-Identifier: AGPL-3.0-only

package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	go Serve(context.Background(), sr, sw, func(ctx context.Context, m string, p json.RawMessage) (any, error) {
		switch m {
		case "echo":
			var v map[string]any
			json.Unmarshal(p, &v)
			return v, nil
		case "slow":
			time.Sleep(50 * time.Millisecond)
			return "slow", nil
		case "fail":
			return nil, errors.New("boom")
		}
		return nil, &Error{Code: CodeMethodNotFound, Message: "no such method"}
	})
	c := NewClient(cr, cw)
	ctx := context.Background()
	var out map[string]any
	if err := c.Call(ctx, "echo", map[string]any{"a": 1}, &out); err != nil || out["a"].(float64) != 1 {
		t.Fatal(out, err)
	}
	// Конкурентные вызовы не блокируют друг друга.
	done := make(chan string, 2)
	go func() { var s string; c.Call(ctx, "slow", nil, &s); done <- s }()
	go func() { var v map[string]any; c.Call(ctx, "echo", map[string]any{}, &v); done <- "fast" }()
	if first := <-done; first != "fast" {
		t.Fatal("calls are serialized")
	}
	<-done
	var re *Error
	if err := c.Call(ctx, "fail", nil, nil); !errors.As(err, &re) || re.Message != "boom" {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "nope", nil, nil); !errors.As(err, &re) || re.Code != CodeMethodNotFound {
		t.Fatal(err)
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := c.Call(tctx, "slow", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	sw.Close()
	<-c.Done()
	if err := c.Call(ctx, "echo", nil, nil); err != ErrClosed {
		t.Fatal(err)
	}
}
