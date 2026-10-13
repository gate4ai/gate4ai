// SPDX-License-Identifier: FSL-1.1-ALv2

// Package rpc — JSON-RPC 2.0 построчно (одно сообщение — одна строка) поверх
// любого потока: stdio дочерних процессов-коннекторов.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
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
	CodeApp            = -32000 // ошибка приложения (коннектора)
)

// ErrClosed — соединение закрыто (процесс коннектора завершился).
var ErrClosed = errors.New("rpc: connection closed")

// MaxLine — максимальный размер сообщения.
const MaxLine = 64 << 20

// Handler обрабатывает метод; возвращённая *Error передаётся как есть.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Serve читает запросы из r и пишет ответы в w до EOF.
func Serve(ctx context.Context, r io.Reader, w io.Writer, h Handler) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	var wmu sync.Mutex
	var wg sync.WaitGroup
	enc := json.NewEncoder(w)
	send := func(m Message) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = enc.Encode(m)
	}
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			send(Message{JSONRPC: "2.0", Error: &Error{Code: CodeParse, Message: err.Error()}})
			continue
		}
		wg.Add(1)
		go func(m Message) {
			defer wg.Done()
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
	wg.Wait()
	return sc.Err()
}

// Client — клиент JSON-RPC с мультиплексированием по id.
type Client struct {
	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[int64]chan Message
	closed  bool
	done    chan struct{}
}

// NewClient запускает чтение ответов из r.
func NewClient(r io.Reader, w io.Writer) *Client {
	c := &Client{w: w, pending: map[int64]chan Message{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

func (c *Client) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	for sc.Scan() {
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[*m.ID]
		delete(c.pending, *m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// Done закрывается, когда соединение оборвалось.
func (c *Client) Done() <-chan struct{} { return c.done }

// Call вызывает метод и декодирует результат в out (может быть nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	pb, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.next++
	id := c.next
	ch := make(chan Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	b, _ := json.Marshal(Message{JSONRPC: "2.0", ID: &id, Method: method, Params: pb})
	c.wmu.Lock()
	_, err = c.w.Write(append(b, '\n'))
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ErrClosed
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}
