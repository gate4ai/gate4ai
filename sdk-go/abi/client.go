// SPDX-License-Identifier: Apache-2.0

package abi

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

// Transport — вызов функции хоста: запрос JSON → Response JSON. В WASM-модуле —
// импорт host_call; в нативных тестах коннектора — хост в том же процессе.
type Transport interface {
	Call(fn string, req []byte) []byte
}

// Host — типизированные функции хоста для кода коннектора.
type Host struct {
	T Transport
}

func (h *Host) call(fn string, req, resp any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return Errorf(CodeBadRequest, "marshal: %v", err)
	}
	return Decode(h.T.Call(fn, b), resp)
}

// callAs — call с ответом типа T.
func callAs[T any](h *Host, fn string, req any) (T, error) {
	var out T
	err := h.call(fn, req, &out)
	return out, err
}

// HTTP — http_request.
func (h *Host) HTTP(r HTTPRequest) (HTTPResponse, error) {
	return callAs[HTTPResponse](h, FnHTTPRequest, r)
}

// FSRead — fs_read.
func (h *Host) FSRead(path string, max int64) (FSReadResponse, error) {
	return callAs[FSReadResponse](h, FnFSRead, FSRequest{Path: path, Max: max})
}

// FSReadAt — fs_read с позиции offset (отрицательное — столько байт с конца файла).
func (h *Host) FSReadAt(path string, offset, max int64) (FSReadResponse, error) {
	return callAs[FSReadResponse](h, FnFSRead, FSRequest{Path: path, Max: max, Offset: offset})
}

// FSWrite — fs_write (атомарно: временный файл и переименование).
func (h *Host) FSWrite(path string, data []byte) (FSWriteResponse, error) {
	return callAs[FSWriteResponse](h, FnFSWrite, FSRequest{Path: path, Data: data})
}

// FSAppend — fs_append (создаёт файл, если его нет).
func (h *Host) FSAppend(path string, data []byte) (FSWriteResponse, error) {
	return callAs[FSWriteResponse](h, FnFSAppend, FSRequest{Path: path, Data: data})
}

// FSList — fs_list.
func (h *Host) FSList(path string) (FSListResponse, error) {
	return callAs[FSListResponse](h, FnFSList, FSRequest{Path: path})
}

// FSStat — fs_stat.
func (h *Host) FSStat(path string) (FSStatResponse, error) {
	return callAs[FSStatResponse](h, FnFSStat, FSRequest{Path: path})
}

// KVGet — kv_get.
func (h *Host) KVGet(key string) ([]byte, bool, error) {
	out, err := callAs[KVResponse](h, FnKVGet, KVRequest{Key: key})
	return out.Value, out.Found, err
}

// KVSet — kv_set.
func (h *Host) KVSet(key string, v []byte) error {
	return h.call(FnKVSet, KVRequest{Key: key, Value: v}, nil)
}

// KVDelete — kv_delete.
func (h *Host) KVDelete(key string) error { return h.call(FnKVDelete, KVRequest{Key: key}, nil) }

// Evidence — evidence_add.
func (h *Host) Evidence(name, mime string, data []byte) error {
	return h.call(FnEvidenceAdd, EvidenceItem{Name: name, MIME: mime, Data: data}, nil)
}

// Log — log.
func (h *Host) Log(level, msg string) { _ = h.call(FnLog, LogRequest{Level: level, Msg: msg}, nil) }

// ServiceCall — service_call: действие другого коннектора. Структурированный
// результат поставщика декодируется в out (nil — не нужен); возвращается
// результат целиком (content, result_ref).
func (h *Host) ServiceCall(target, action string, args any, out any) (ExecResult, error) {
	var a map[string]any
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return ExecResult{}, Errorf(CodeBadRequest, "marshal: %v", err)
		}
		if err := json.Unmarshal(b, &a); err != nil {
			return ExecResult{}, Errorf(CodeBadRequest, "args must be an object: %v", err)
		}
	}
	var res ExecResult
	if err := h.call(FnServiceCall, ServiceCallRequest{Target: target, Action: action, Args: a}, &res); err != nil {
		return res, err
	}
	if out != nil && res.Structured != nil {
		b, _ := json.Marshal(res.Structured)
		if err := json.Unmarshal(b, out); err != nil {
			return res, Errorf(CodeBadRequest, "%s.%s result: %v", target, action, err)
		}
	}
	return res, nil
}

// Dial — tls_dial: соединение (net.Conn) поверх conn_read / conn_write.
func (h *Host) Dial(r DialRequest) (net.Conn, error) {
	var out DialResponse
	if err := h.call(FnTLSDial, r, &out); err != nil {
		return nil, err
	}
	return &Conn{h: h, id: out.Conn, remote: r.Host}, nil
}

// Conn — соединение, открытое хостом. Чтение опрашивает хост с короткими
// ожиданиями и уступает планировщику: в WASM один поток, и долгий блокирующий
// вызов хоста остановил бы остальные горутины модуля (например, ту, что пишет).
type Conn struct {
	h      *Host
	id     int
	remote string

	mu       sync.Mutex
	buf      []byte
	eof      bool
	closed   bool
	deadline time.Time
}

// ErrClosed — соединение закрыто.
var ErrClosed = errors.New("connection closed")

func (c *Conn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.buf) > 0 {
			n := copy(p, c.buf)
			c.buf = c.buf[n:]
			c.mu.Unlock()
			return n, nil
		}
		if c.closed {
			c.mu.Unlock()
			return 0, ErrClosed
		}
		if c.eof {
			c.mu.Unlock()
			return 0, io.EOF
		}
		dl := c.deadline
		c.mu.Unlock()
		if !dl.IsZero() && time.Now().After(dl) {
			return 0, os.ErrDeadlineExceeded
		}
		var out ConnReadResponse
		if err := c.h.call(FnConnRead, ConnRead{Conn: c.id, Max: 64 << 10, WaitMS: 20}, &out); err != nil {
			return 0, err
		}
		c.mu.Lock()
		c.buf = append(c.buf, out.Data...)
		c.eof = c.eof || out.EOF
		c.mu.Unlock()
		if len(out.Data) == 0 && !out.EOF {
			runtime.Gosched()
		}
	}
}

func (c *Conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, ErrClosed
	}
	out, err := callAs[ConnWriteResponse](c.h, FnConnWrite, ConnWrite{Conn: c.id, Data: p})
	return out.N, err
}

// Close — conn_close.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.h.call(FnConnClose, ConnClose{Conn: c.id}, nil)
}

type addr string

func (a addr) Network() string { return "gate4ai" }
func (a addr) String() string  { return string(a) }

func (c *Conn) LocalAddr() net.Addr  { return addr("connector") }
func (c *Conn) RemoteAddr() net.Addr { return addr(c.remote) }

// SetDeadline — срок чтения (запись выполняет хост с собственным таймаутом).
func (c *Conn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

func (c *Conn) SetWriteDeadline(time.Time) error { return nil }
