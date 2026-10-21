// SPDX-License-Identifier: Apache-2.0

// Package hostfuncs — реализация функций хоста ABI v1 поверх служб хоста:
// каждая функция сначала проверяется по возможностям коннектора (capability),
// секреты подставляются только хостом и только для адресов привязки, их
// значения вырезаются из ответов и логов. Один Dispatcher — один вызов
// коннектора: соединения закрываются в Close.
//
// Хост gate4.ai подключает сюда свои службы (Vault, хранилище, вызовы других
// коннекторов); тестовый хост коннекторов (hosttest) — фейки.
package hostfuncs

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/capability"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/gate4ai/sdk-go/netauth"
)

// Secrets — значения секретов по алиасу (Vault хоста).
type Secrets interface {
	Value(alias string) (string, error)
}

// KV — собственное хранилище коннектора.
type KV interface {
	Get(key string) ([]byte, bool, error)
	// Set сохраняет значение; quota — предел общего размера в байтах.
	Set(key string, v []byte, quota int64) error
	Delete(key string) error
}

// ServiceRouter — вызовы действий других коннекторов (service_call): хост
// проводит их через свои политики. rule — разрешение вызывающего из его
// манифеста (уже проверено: действие объявлено).
type ServiceRouter interface {
	ServiceCall(ctx context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error)
}

// Лимиты одного вызова.
const (
	MaxHTTPBody     = 16 << 20
	MaxFSRead       = 16 << 20
	DefaultFSRead   = 1 << 20
	MaxEvidence     = 32 << 20
	MaxConns        = 8
	maxConnReadWait = 200
)

// Dispatcher — функции хоста для одного вызова коннектора.
type Dispatcher struct {
	Ctx     context.Context
	Check   *capability.Checker
	Secrets Secrets
	KV      KV
	// Services — вызовы других коннекторов (nil — не поддерживаются).
	Services ServiceRouter
	// Log — диагностический лог коннектора (секреты уже вырезаны).
	Log func(level, msg string)
	// Dial и RootCAs — сеть (подменяются в тестах).
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	RootCAs *x509.CertPool
	Timeout time.Duration

	mu       sync.Mutex
	conns    map[int]*conn
	next     int
	used     []string
	evidence []abi.EvidenceItem
	evSize   int
}

func (d *Dispatcher) ctx() context.Context {
	if d.Ctx == nil {
		return context.Background()
	}
	return d.Ctx
}

func (d *Dispatcher) timeout() time.Duration {
	if d.Timeout == 0 {
		return 30 * time.Second
	}
	return d.Timeout
}

// Evidence — доказательства, добавленные через evidence_add.
func (d *Dispatcher) Evidence() []abi.EvidenceItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]abi.EvidenceItem(nil), d.evidence...)
}

// secret — значение секрета для адреса host (проверка привязки); значение
// запоминается для маскирования.
func (d *Dispatcher) secret(alias, host string) (string, error) {
	if err := d.Check.Secret(alias, host); err != nil {
		return "", err
	}
	if d.Secrets == nil {
		return "", abi.Errorf(abi.CodeNotSupported, "no vault")
	}
	v, err := d.Secrets.Value(alias)
	if err != nil {
		return "", abi.Errorf(abi.CodeNotFound, "secret %s: %v", alias, err)
	}
	if v != "" {
		d.mu.Lock()
		d.used = append(d.used, v)
		d.mu.Unlock()
	}
	return v, nil
}

// Mask вырезает значения секретов, подставленных в этом вызове.
func (d *Dispatcher) Mask(s string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, v := range d.used {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[secret]")
		}
	}
	return s
}

func (d *Dispatcher) maskBytes(b []byte) []byte {
	d.mu.Lock()
	used := d.used
	d.mu.Unlock()
	for _, v := range used {
		if len(v) >= 4 {
			b = bytes.ReplaceAll(b, []byte(v), []byte("[secret]"))
		}
	}
	return b
}

// Call — вызов функции хоста (abi.Transport).
func (d *Dispatcher) Call(fn string, req []byte) []byte {
	res, err := d.call(fn, req)
	if err != nil {
		if e, ok := err.(*abi.Error); ok {
			e.Message = d.Mask(e.Message)
			return abi.Fail(e)
		}
		return abi.Fail(abi.Errorf(abi.CodeIO, "%s", d.Mask(err.Error())))
	}
	return abi.OK(res)
}

func decode(req []byte, v any) error {
	if err := json.Unmarshal(req, v); err != nil {
		return abi.Errorf(abi.CodeBadRequest, "%v", err)
	}
	return nil
}

func (d *Dispatcher) call(fn string, req []byte) (any, error) {
	switch fn {
	case abi.FnHTTPRequest:
		var r abi.HTTPRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return d.http(r)
	case abi.FnTLSDial:
		var r abi.DialRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return d.dial(r)
	case abi.FnConnRead:
		var r abi.ConnRead
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		c, err := d.conn(r.Conn)
		if err != nil {
			return nil, err
		}
		return c.read(r.Max, r.WaitMS), nil
	case abi.FnConnWrite:
		var r abi.ConnWrite
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		c, err := d.conn(r.Conn)
		if err != nil {
			return nil, err
		}
		_ = c.c.SetWriteDeadline(time.Now().Add(d.timeout()))
		n, err := c.c.Write(r.Data)
		if err != nil {
			return abi.ConnWriteResponse{N: n}, abi.Errorf(abi.CodeIO, "write: %v", err)
		}
		return abi.ConnWriteResponse{N: n}, nil
	case abi.FnConnClose:
		var r abi.ConnClose
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		d.mu.Lock()
		c := d.conns[r.Conn]
		delete(d.conns, r.Conn)
		d.mu.Unlock()
		if c != nil {
			c.c.Close()
		}
		return struct{}{}, nil
	case abi.FnFSRead, abi.FnFSWrite, abi.FnFSAppend, abi.FnFSList, abi.FnFSStat:
		var r abi.FSRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return d.fs(fn, r)
	case abi.FnKVGet, abi.FnKVSet, abi.FnKVDelete:
		var r abi.KVRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return d.kv(fn, r)
	case abi.FnEvidenceAdd:
		var r abi.EvidenceItem
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.evSize+len(r.Data) > MaxEvidence {
			return nil, abi.Errorf(abi.CodeLimit, "evidence is larger than %d bytes", MaxEvidence)
		}
		d.evSize += len(r.Data)
		d.evidence = append(d.evidence, r)
		return struct{}{}, nil
	case abi.FnLog:
		var r abi.LogRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		if d.Log != nil {
			d.Log(r.Level, d.Mask(r.Msg))
		}
		return struct{}{}, nil
	case abi.FnServiceCall:
		var r abi.ServiceCallRequest
		if err := decode(req, &r); err != nil {
			return nil, err
		}
		return d.service(r)
	}
	return nil, abi.Errorf(abi.CodeNotSupported, "unknown host function %q", fn)
}

var placeholderRe = regexp.MustCompile(`\{\{secret:(@[a-zA-Z0-9_.\-]+)\}\}`)

func (d *Dispatcher) substitute(s, host string) (string, error) {
	var ferr error
	out := placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		alias := placeholderRe.FindStringSubmatch(m)[1]
		v, err := d.secret(alias, host)
		if err != nil && ferr == nil {
			ferr = err
		}
		return v
	})
	return out, ferr
}

func (d *Dispatcher) http(r abi.HTTPRequest) (abi.HTTPResponse, error) {
	var out abi.HTTPResponse
	if strings.Contains(r.URL, "{{secret:") {
		return out, abi.Errorf(abi.CodeBadRequest, "secrets are not allowed in the URL (use a header or the body)")
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Hostname() == "" {
		return out, abi.Errorf(abi.CodeBadRequest, "bad url %q", r.URL)
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	host := strings.ToLower(u.Hostname())
	if err := d.Check.Network(u.Scheme, host, port); err != nil {
		return out, err
	}
	method := r.Method
	if method == "" {
		method = "GET"
	}
	body, err := d.substitute(string(r.Body), host)
	if err != nil {
		return out, err
	}
	timeout := d.timeout()
	if r.TimeoutMS > 0 && time.Duration(r.TimeoutMS)*time.Millisecond < timeout {
		timeout = time.Duration(r.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(d.ctx(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return out, abi.Errorf(abi.CodeBadRequest, "%v", err)
	}
	for k, v := range r.Headers {
		if v, err = d.substitute(v, host); err != nil {
			return out, err
		}
		req.Header.Set(k, v)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: d.RootCAs, MinVersion: tls.VersionTLS12}, DialContext: d.Dial, Proxy: nil}
	defer tr.CloseIdleConnections()
	// Редиректы не выполняются: 3xx уходит модулю, следующий запрос снова проверяется.
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		return out, abi.Errorf(abi.CodeIO, "%s %s: %v", method, host, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxHTTPBody+1))
	if err != nil {
		return out, abi.Errorf(abi.CodeIO, "read body: %v", err)
	}
	if len(b) > MaxHTTPBody {
		return out, abi.Errorf(abi.CodeLimit, "response body is larger than %d bytes", MaxHTTPBody)
	}
	out.Status, out.Body, out.Headers = resp.StatusCode, d.maskBytes(b), map[string]string{}
	for k := range resp.Header {
		out.Headers[k] = d.Mask(resp.Header.Get(k))
	}
	return out, nil
}

// conn — соединение коннектора с фоновым чтением.
type conn struct {
	c     net.Conn
	mu    sync.Mutex
	buf   []byte
	eof   bool
	ready chan struct{}
}

func (c *conn) pump() {
	b := make([]byte, 32<<10)
	for {
		n, err := c.c.Read(b)
		c.mu.Lock()
		c.buf = append(c.buf, b[:n]...)
		if err != nil {
			c.eof = true
		}
		c.mu.Unlock()
		select {
		case c.ready <- struct{}{}:
		default:
		}
		if err != nil {
			return
		}
	}
}

func (c *conn) read(max, waitMS int) abi.ConnReadResponse {
	if max <= 0 || max > 1<<20 {
		max = 64 << 10
	}
	if waitMS > maxConnReadWait {
		waitMS = maxConnReadWait
	}
	c.mu.Lock()
	empty := len(c.buf) == 0 && !c.eof
	c.mu.Unlock()
	if empty && waitMS > 0 {
		select {
		case <-c.ready:
		case <-time.After(time.Duration(waitMS) * time.Millisecond):
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := min(max, len(c.buf))
	out := abi.ConnReadResponse{Data: append([]byte(nil), c.buf[:n]...)}
	c.buf = c.buf[n:]
	out.EOF = c.eof && len(c.buf) == 0
	return out
}

func (d *Dispatcher) conn(id int) (*conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.conns[id]
	if c == nil {
		return nil, abi.Errorf(abi.CodeNotFound, "connection %d", id)
	}
	return c, nil
}

func (d *Dispatcher) dial(r abi.DialRequest) (abi.DialResponse, error) {
	var out abi.DialResponse
	scheme := manifest.SchemeTLS
	if r.TLS == abi.TLSNone {
		scheme = manifest.SchemeTCP
	}
	port := r.Port
	if port == 0 {
		port = netauth.DefaultPort(r.Protocol, r.TLS)
	}
	host := strings.ToLower(r.Host)
	if err := d.Check.Network(scheme, host, port); err != nil {
		return out, err
	}
	cfg := netauth.Config{DialRequest: r, Dial: d.Dial, RootCAs: d.RootCAs, Timeout: d.timeout()}
	cfg.Port = port
	if r.Auth != nil {
		v, err := d.secret(r.Auth.Secret, host)
		if err != nil {
			return out, err
		}
		cfg.Password = v
	}
	d.mu.Lock()
	if len(d.conns) >= MaxConns {
		d.mu.Unlock()
		return out, abi.Errorf(abi.CodeLimit, "too many open connections")
	}
	d.mu.Unlock()
	nc, err := netauth.Dial(d.ctx(), cfg)
	if err != nil {
		return out, err
	}
	c := &conn{c: nc, buf: append([]byte(nil), nc.Greeting...), ready: make(chan struct{}, 1)}
	go c.pump()
	d.mu.Lock()
	if d.conns == nil {
		d.conns = map[int]*conn{}
	}
	d.next++
	d.conns[d.next] = c
	out.Conn = d.next
	d.mu.Unlock()
	return out, nil
}

func size(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (d *Dispatcher) fs(fn string, r abi.FSRequest) (any, error) {
	real, err := d.Check.Path(r.Path)
	if err != nil {
		return nil, err
	}
	ioErr := func(err error) error {
		if os.IsNotExist(err) {
			return abi.Errorf(abi.CodeNotFound, "%s: no such file or directory", r.Path)
		}
		return abi.Errorf(abi.CodeIO, "%v", err)
	}
	switch fn {
	case abi.FnFSStat:
		fi, err := os.Stat(real)
		if err != nil {
			if os.IsNotExist(err) {
				return abi.FSStatResponse{Size: -1, RealPath: real}, nil
			}
			return nil, ioErr(err)
		}
		return abi.FSStatResponse{Exists: true, Dir: fi.IsDir(), Size: fi.Size(), RealPath: real}, nil
	case abi.FnFSRead:
		max := r.Max
		if max <= 0 {
			max = DefaultFSRead
		}
		if max > MaxFSRead {
			max = MaxFSRead
		}
		fi, err := os.Stat(real)
		if err != nil {
			return nil, ioErr(err)
		}
		if fi.IsDir() {
			return nil, abi.Errorf(abi.CodeBadRequest, "%s is a directory", r.Path)
		}
		f, err := os.Open(real)
		if err != nil {
			return nil, ioErr(err)
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, max+1))
		if err != nil {
			return nil, ioErr(err)
		}
		out := abi.FSReadResponse{Size: fi.Size(), RealPath: real}
		if int64(len(b)) > max {
			b, out.Truncated = b[:max], true
		}
		out.Data = b
		return out, nil
	case abi.FnFSList:
		ents, err := os.ReadDir(real)
		if err != nil {
			return nil, ioErr(err)
		}
		out := abi.FSListResponse{Entries: []abi.FSEntry{}, RealPath: real}
		for _, e := range ents {
			it := abi.FSEntry{Name: e.Name(), Dir: e.IsDir()}
			if fi, err := e.Info(); err == nil && !e.IsDir() {
				it.Size = fi.Size()
			}
			out.Entries = append(out.Entries, it)
		}
		sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
		return out, nil
	case abi.FnFSAppend:
		before := size(real)
		f, err := os.OpenFile(real, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return nil, ioErr(err)
		}
		if _, err := f.Write(r.Data); err != nil {
			f.Close()
			return nil, ioErr(err)
		}
		if err := f.Close(); err != nil {
			return nil, ioErr(err)
		}
		return abi.FSWriteResponse{SizeBefore: before, SizeAfter: size(real), RealPath: real}, nil
	case abi.FnFSWrite:
		before := size(real)
		tmp, err := os.CreateTemp(filepath.Dir(real), ".gate4ai-*")
		if err != nil {
			return nil, ioErr(err)
		}
		_, werr := tmp.Write(r.Data)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			_ = os.Chmod(tmp.Name(), 0o644)
			werr = os.Rename(tmp.Name(), real)
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return nil, ioErr(werr)
		}
		return abi.FSWriteResponse{SizeBefore: before, SizeAfter: size(real), RealPath: real}, nil
	}
	return nil, abi.Errorf(abi.CodeNotSupported, "%s", fn)
}

func (d *Dispatcher) kv(fn string, r abi.KVRequest) (any, error) {
	quota, err := d.Check.Storage()
	if err != nil {
		return nil, err
	}
	if d.KV == nil {
		return nil, abi.Errorf(abi.CodeNotSupported, "no storage")
	}
	if r.Key == "" || len(r.Key) > 256 || !utf8.ValidString(r.Key) {
		return nil, abi.Errorf(abi.CodeBadRequest, "bad key")
	}
	switch fn {
	case abi.FnKVGet:
		v, ok, err := d.KV.Get(r.Key)
		if err != nil {
			return nil, abi.Errorf(abi.CodeIO, "%v", err)
		}
		return abi.KVResponse{Value: v, Found: ok}, nil
	case abi.FnKVSet:
		if err := d.KV.Set(r.Key, r.Value, quota); err != nil {
			if e, ok := err.(*abi.Error); ok {
				return nil, e
			}
			return nil, abi.Errorf(abi.CodeIO, "%v", err)
		}
		return struct{}{}, nil
	default:
		if err := d.KV.Delete(r.Key); err != nil {
			return nil, abi.Errorf(abi.CodeIO, "%v", err)
		}
		return struct{}{}, nil
	}
}

func (d *Dispatcher) service(r abi.ServiceCallRequest) (any, error) {
	rule, err := d.Check.Service(r.Target, r.Action)
	if err != nil {
		return nil, err
	}
	if d.Services == nil {
		return nil, abi.Errorf(abi.CodeNotSupported, "no service calls on this host")
	}
	return d.Services.ServiceCall(d.ctx(), rule, r)
}

// Close закрывает соединения вызова.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	conns := d.conns
	d.conns = nil
	d.mu.Unlock()
	for _, c := range conns {
		c.c.Close()
	}
}
