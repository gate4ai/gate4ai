// SPDX-License-Identifier: AGPL-3.0-only

package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"slow"}`,
		`{"jsonrpc":"2.0","id":2,"method":"echo","params":{"a":1}}`,
		`{"jsonrpc":"2.0","id":3,"method":"fail"}`,
		`{"jsonrpc":"2.0","id":4,"method":"nope"}`,
		`{"jsonrpc":"2.0","method":"echo"}`,
		`not json`,
	}, "\n") + "\n"
	var out strings.Builder
	err := ServeWith(context.Background(), strings.NewReader(in), &out, func(ctx context.Context, m string, p json.RawMessage) (any, error) {
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
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Message
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(line, err)
		}
		got = append(got, m)
	}
	// Ответ без id — только на нераспознанную строку; на уведомление ответа нет.
	byID := map[int64]Message{}
	parseErrs := 0
	for _, m := range got {
		if m.ID == nil {
			if m.Error != nil && m.Error.Code == CodeParse {
				parseErrs++
			}
			continue
		}
		byID[*m.ID] = m
	}
	if len(got) != 5 || parseErrs != 1 {
		t.Fatalf("%+v", got)
	}
	// Конкурентные вызовы не блокируют друг друга: slow отвечает последним.
	if got[4].ID == nil || *got[4].ID != 1 || string(byID[1].Result) != `"slow"` {
		t.Fatal("calls are serialized:", out.String())
	}
	if string(byID[2].Result) != `{"a":1}` {
		t.Fatal(string(byID[2].Result))
	}
	if e := byID[3].Error; e == nil || e.Code != CodeApp || e.Message != "boom" {
		t.Fatal(e)
	}
	if e := byID[4].Error; e == nil || e.Code != CodeMethodNotFound {
		t.Fatal(e)
	}
}

// Не больше MaxInFlight обработчиков одновременно.
func TestServeLimitsInFlight(t *testing.T) {
	var mu sync.Mutex
	cur, peak := 0, 0
	release := make(chan struct{})
	var in strings.Builder
	for i := 0; i < MaxInFlight+20; i++ {
		fmt.Fprintf(&in, `{"jsonrpc":"2.0","id":%d,"method":"block"}`+"\n", i+1)
	}
	errc := make(chan error, 1)
	go func() {
		errc <- ServeWith(context.Background(), strings.NewReader(in.String()), io.Discard, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			mu.Lock()
			cur++
			peak = max(peak, cur)
			mu.Unlock()
			<-release
			mu.Lock()
			cur--
			mu.Unlock()
			return nil, nil
		}, nil)
	}()
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	p := peak
	mu.Unlock()
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if p != MaxInFlight {
		t.Fatalf("peak in-flight = %d, want %d", p, MaxInFlight)
	}
}

// После EOF долгий обработчик получает отмену контекста через DrainTimeout.
func TestServeCancelsAfterEOF(t *testing.T) {
	old := DrainTimeout
	DrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { DrainTimeout = old })
	errc := make(chan error, 1)
	go func() {
		errc <- ServeWith(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"wait"}`+"\n"), io.Discard,
			func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}, nil)
	}()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after EOF")
	}
}
