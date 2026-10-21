// SPDX-License-Identifier: Apache-2.0

package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// CallEnder — коннектор, у которого есть состояние на время корневого вызова
// (например, страница браузера): EndCall освобождает его.
type CallEnder interface {
	EndCall(call string)
}

// Serve — main нативного коннектора: обслуживает хост через stdin/stdout до
// shutdown или конца потока. Манифест проверяется сразу.
func Serve(manifestYAML []byte, c connector.Connector) {
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := ServeIO(m, c, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ServeIO — Serve поверх произвольных потоков (тесты и встраивание).
func ServeIO(m manifest.Manifest, c connector.Connector, r io.Reader, w io.Writer) error {
	if m.Runtime != manifest.RuntimeNative {
		return fmt.Errorf("connector %s: runtime %q is not native", m.Connector, m.Runtime)
	}
	done := make(chan struct{})
	lim := m.Limits.WithDefaults()
	var mu sync.Mutex
	cancels := map[string]context.CancelFunc{} // вызов → отмена
	var p *Peer
	p = NewPeer(r, w, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case MethodInitialize:
			var in InitializeParams
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, err
			}
			if in.Protocol != Protocol {
				return nil, fmt.Errorf("protocol %d is not supported (want %d)", in.Protocol, Protocol)
			}
			return InitializeResult{Protocol: Protocol, Manifest: m}, nil
		case MethodExecute, MethodVerify:
			var in CallParams
			if err := json.Unmarshal(params, &in); err != nil {
				return nil, err
			}
			op := connector.OpExecute
			if method == MethodVerify {
				op = connector.OpVerify
			}
			// Срок вызова — как у хоста (limits.timeout_s): вызов не висит, даже
			// если хост не дождался ответа и об отмене не сообщил.
			cctx, cancel := context.WithTimeout(ctx, time.Duration(lim.TimeoutS)*time.Second)
			mu.Lock()
			cancels[in.Call] = cancel
			mu.Unlock()
			defer func() {
				mu.Lock()
				delete(cancels, in.Call)
				mu.Unlock()
				cancel()
			}()
			return RawResponse(connector.HandleContext(cctx, m, c, transport{p: p, call: in.Call, ctx: cctx}, op, in.Request)), nil
		case MethodCancel:
			var in CancelParams
			if err := json.Unmarshal(params, &in); err == nil {
				mu.Lock()
				cancel := cancels[in.Call]
				mu.Unlock()
				if cancel != nil {
					cancel()
				}
			}
			return nil, nil
		case MethodEndCall:
			var in EndCallParams
			if err := json.Unmarshal(params, &in); err == nil {
				if e, ok := c.(CallEnder); ok {
					e.EndCall(in.Call)
				}
			}
			return nil, nil
		case MethodShutdown:
			select {
			case <-done:
			default:
				close(done)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("unknown method %q", method)
	})
	errc := make(chan error, 1)
	go func() { errc <- p.Run() }()
	select {
	case err := <-errc:
		return err
	case <-done:
		return nil
	}
}

// transport — функции хоста через собеседника в рамках вызова call.
type transport struct {
	p    *Peer
	call string
	ctx  context.Context // контекст вызова: функции хоста отменяются вместе с ним
}

func (t transport) Call(fn string, req []byte) []byte {
	var out RawResponse
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := t.p.Call(ctx, MethodHost, HostParams{Call: t.call, Fn: fn, Req: req}, &out); err != nil {
		return abi.Fail(abi.Errorf(abi.CodeIO, "host: %v", err))
	}
	return out
}
