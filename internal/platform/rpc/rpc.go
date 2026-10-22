// SPDX-License-Identifier: AGPL-3.0-only

// Package rpc — JSON-RPC 2.0 построчно (одно сообщение — одна строка) поверх
// любого потока: MCP по stdio.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Message — запрос, ответ или уведомление JSON-RPC 2.0.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error — ошибка JSON-RPC.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Коды ошибок.
const (
	CodeParse          = -32700
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeApp            = -32000 // ошибка приложения
)

// MaxLine — максимальный размер сообщения.
const MaxLine = 64 << 20

// Handler обрабатывает метод; возвращённая *Error передаётся как есть.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// MaxInFlight — сколько запросов обрабатывается одновременно; следующие ждут
// в очереди чтения (собеседник не может завести неограниченно горутин).
const MaxInFlight = 64

// DrainTimeout — сколько после EOF ждать начатые обработчики, прежде чем
// отменить их контекст (долгие ожидания вроде status --wait не держат выход).
var DrainTimeout = 10 * time.Second

// Notify — уведомление собеседнику (сервер → клиент, без id).
type Notify func(method string, params any)

// ServeWith читает запросы из r и пишет ответы в w до EOF; сервер может сам
// слать уведомления: start (может быть nil) получает функцию отправки (в той же
// очереди записи, что и ответы).
func ServeWith(ctx context.Context, r io.Reader, w io.Writer, h Handler, start func(Notify)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, MaxInFlight)
	var wmu sync.Mutex
	var wg sync.WaitGroup
	enc := json.NewEncoder(w)
	send := func(m Message) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = enc.Encode(m)
	}
	if start != nil {
		start(func(method string, params any) {
			m := Message{JSONRPC: "2.0", Method: method}
			if params != nil {
				m.Params, _ = json.Marshal(params)
			}
			send(m)
		})
	}
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			send(Message{JSONRPC: "2.0", Error: &Error{Code: CodeParse, Message: err.Error()}})
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Add(1)
		go func(m Message) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := h(ctx, m.Method, m.Params)
			if m.ID == nil {
				return
			}
			out := Message{JSONRPC: "2.0", ID: m.ID}
			if err != nil {
				var re *Error
				if !errors.As(err, &re) {
					re = &Error{Code: CodeApp, Message: err.Error()}
				}
				out.Error = re
			} else {
				b, merr := json.Marshal(res)
				if merr != nil {
					out.Error = &Error{Code: CodeInternal, Message: merr.Error()}
				} else {
					out.Result = b
				}
			}
			send(out)
		}(m)
	}
	// EOF: начатые вызовы получают DrainTimeout на завершение, затем их
	// контекст отменяется.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	t := time.NewTimer(DrainTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		cancel()
		<-done
	}
	return sc.Err()
}
