// SPDX-License-Identifier: Apache-2.0

// Package native — коннекторы runtime: native: отдельный процесс под надзором
// хоста. Обмен — JSON-RPC 2.0 построчно через stdin/stdout процесса, вызовы в
// обе стороны: хост вызывает initialize, execute, verify, end_call, shutdown;
// коннектор — host (функция хоста в рамках вызова, с той же проверкой
// возможностей, что у WASM). stderr процесса хост пишет в свой лог.
//
// Коннектор пишется на том же PDK (connector.Connector), что и WASM-модуль:
//
//	//go:embed manifest.yaml
//	var manifestYAML []byte
//
//	func main() { native.Serve(manifestYAML, &Browser{}) }
//
// Сборка: CGO_ENABLED=0 go build -trimpath -buildvcs=false (gate4ai connectors pack
// собирает под каждую платформу из manifest.platforms).
package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Protocol — версия протокола хост ↔ процесс.
const Protocol = 1

// Методы.
const (
	MethodInitialize = "initialize" // хост → процесс: InitializeParams → InitializeResult
	MethodExecute    = "execute"    // хост → процесс: CallParams{ExecRequest} → ответ abi (Response)
	MethodVerify     = "verify"     // хост → процесс: CallParams{VerifyRequest} → ответ abi (Response)
	MethodEndCall    = "end_call"   // хост → процесс, уведомление: EndCallParams
	MethodCancel     = "cancel"     // хост → процесс, уведомление: CancelParams — отменить вызов
	MethodShutdown   = "shutdown"   // хост → процесс, уведомление: завершиться
	MethodHost       = "host"       // процесс → хост: HostParams → ответ abi (Response)
)

// InitializeParams — initialize.
type InitializeParams struct {
	Protocol int `json:"protocol"`
}

// InitializeResult — ответ на initialize: манифест процесса хост сверяет с
// манифестом пакета.
type InitializeResult struct {
	Protocol int               `json:"protocol"`
	Manifest manifest.Manifest `json:"manifest"`
}

// CallParams — execute / verify: Call — идентификатор вызова (в нём процесс
// вызывает функции хоста), Request — abi.ExecRequest или abi.VerifyRequest.
type CallParams struct {
	Call    string          `json:"call"`
	Request json.RawMessage `json:"request"`
}

// HostParams — функция хоста Fn с запросом Req в рамках вызова Call.
type HostParams struct {
	Call string          `json:"call"`
	Fn   string          `json:"fn"`
	Req  json.RawMessage `json:"req"`
}

// CancelParams — вызов Call отменён (запрос, который его начал, закончился,
// или вышел срок): контекст Execute / Verify отменяется.
type CancelParams struct {
	Call string `json:"call"`
}

// EndCallParams — корневой вызов Call закончился: освободить его состояние.
type EndCallParams struct {
	Call string `json:"call"`
}

// message — сообщение JSON-RPC 2.0.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Handler обрабатывает входящий запрос или уведомление; результат — любой
// JSON (для уведомлений не отправляется).
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// MaxMessage — предел одного сообщения (доказательства и скриншоты внутри).
const MaxMessage = 64 << 20

// ErrClosed — соединение с собеседником закрыто.
var ErrClosed = errors.New("native: connection closed")

// Peer — один конец соединения: вызовы в обе стороны поверх пары потоков.
type Peer struct {
	h   Handler
	r   io.Reader
	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[int64]chan message
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewPeer — соединение поверх r (входящие сообщения) и w (исходящие).
func NewPeer(r io.Reader, w io.Writer, h Handler) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Peer{h: h, r: r, w: w, pending: map[int64]chan message{}, ctx: ctx, cancel: cancel}
}

// Run читает сообщения до конца потока; запросы обрабатываются параллельно,
// уведомления — по порядку.
// После возврата все незавершённые вызовы получают ErrClosed.
func (p *Peer) Run() error {
	sc := bufio.NewScanner(p.r)
	sc.Buffer(make([]byte, 64<<10), MaxMessage)
	var err error
	for sc.Scan() {
		var m message
		if e := json.Unmarshal(sc.Bytes(), &m); e != nil {
			err = fmt.Errorf("native: bad message: %v", e)
			break
		}
		switch {
		case m.Method != "" && m.ID == nil:
			p.handle(m) // уведомления — по порядку (end_call до shutdown)
		case m.Method != "":
			go p.handle(m)
		case m.ID != nil:
			p.mu.Lock()
			ch := p.pending[*m.ID]
			delete(p.pending, *m.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	if err == nil {
		err = sc.Err()
	}
	// Сначала соединение помечается законченным (Done), потом незавершённые
	// вызовы получают ErrClosed: тот, кто повторит вызов, уже видит Done.
	p.mu.Lock()
	p.closed = true
	p.cancel()
	for id, ch := range p.pending {
		close(ch)
		delete(p.pending, id)
	}
	p.mu.Unlock()
	return err
}

func (p *Peer) handle(m message) {
	var res any
	var err error
	if p.h == nil {
		err = fmt.Errorf("method %q is not supported", m.Method)
	} else {
		res, err = p.h(p.ctx, m.Method, m.Params)
	}
	if m.ID == nil {
		return // уведомление
	}
	out := message{JSONRPC: "2.0", ID: m.ID}
	if err != nil {
		out.Error = &rpcError{Code: -32000, Message: err.Error()}
	} else {
		b, e := json.Marshal(res)
		if e != nil {
			out.Error = &rpcError{Code: -32603, Message: e.Error()}
		} else {
			out.Result = b
		}
	}
	_ = p.send(out)
}

func (p *Peer) send(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(b) > MaxMessage {
		return fmt.Errorf("native: message is larger than %d bytes", MaxMessage)
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.w.Write(append(b, '\n'))
	return err
}

// Call вызывает метод собеседника и ждёт ответа (result — куда декодировать, nil — не нужен).
func (p *Peer) Call(ctx context.Context, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan message, 1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	p.next++
	id := p.next
	p.pending[id] = ch
	p.mu.Unlock()
	if err := p.send(message{JSONRPC: "2.0", ID: &id, Method: method, Params: raw}); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return fmt.Errorf("native: %s: %w", method, err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != nil {
			return fmt.Errorf("native: %s: %s", method, m.Error.Message)
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return ctx.Err()
	}
}

// Notify отправляет уведомление (без ответа).
func (p *Peer) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return p.send(message{JSONRPC: "2.0", Method: method, Params: raw})
}

// Done закрывается, когда соединение закончилось.
func (p *Peer) Done() <-chan struct{} { return p.ctx.Done() }

// RawResponse — ответ abi как есть (результат execute, verify, host).
type RawResponse = json.RawMessage
