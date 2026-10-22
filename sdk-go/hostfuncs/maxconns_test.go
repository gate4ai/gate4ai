// SPDX-License-Identifier: Apache-2.0

package hostfuncs

import (
	"context"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// Параллельные tls_dial не открывают больше MaxConns соединений: слот
// занимается до dial (B.L7).
func TestMaxConnsUnderConcurrentDial(t *testing.T) {
	d := dispatcher(t, "version: \"1\"\ncapabilities: { network: [ { host: 127.0.0.1, scheme: tcp } ] }\nactions: []\n", nil)
	release := make(chan struct{})
	var dials atomic.Int32
	d.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		<-release
		a, b := net.Pipe()
		go func() { <-ctx.Done(); b.Close() }()
		return a, nil
	}
	var wg sync.WaitGroup
	var ok, limited atomic.Int32
	for i := 0; i < MaxConns+4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r abi.DialResponse
			err := call(t, d, abi.FnTLSDial, abi.DialRequest{Host: "127.0.0.1", Port: 9, TLS: abi.TLSNone}, &r)
			switch code(err) {
			case "":
				ok.Add(1)
			case abi.CodeLimit:
				limited.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	for dials.Load()+limited.Load() < MaxConns+4 && limited.Load() < 4 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if ok.Load() != MaxConns || limited.Load() != 4 || dials.Load() != MaxConns {
		t.Fatal("open:", ok.Load(), "limited:", limited.Load(), "dials:", dials.Load())
	}
}
