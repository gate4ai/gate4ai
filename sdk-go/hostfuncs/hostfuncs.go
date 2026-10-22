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
	"errors"
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
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs/mask"
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
	// MaxConnBuffer — предел непрочитанных модулем данных соединения: дальше
	// фоновое чтение ждёт, пока модуль заберёт данные.
	MaxConnBuffer = 8 << 20
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
	dialing  int // соединения, для которых занят слот MaxConns, но dial ещё идёт
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

// secret — значение секрета для соединения scheme://host:port (проверка
// привязки); значение запоминается для маскирования.
func (d *Dispatcher) secret(alias, scheme, host string, port int) (string, error) {
	if err := d.Check.Secret(alias, scheme, host, port); err != nil {
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

// masks — набор маскирования по секретам, подставленным в этом вызове
// (значения и производные формы, один проход).
func (d *Dispatcher) masks() *mask.Set {
	d.mu.Lock()
	defer d.mu.Unlock()
	return mask.New(d.used...)
}

// Mask вырезает значения секретов, подставленных в этом вызове.
func (d *Dispatcher) Mask(s string) string { return d.masks().String(s, "[secret]") }

func (d *Dispatcher) maskBytes(b []byte) []byte { return d.masks().Bytes(b, "[secret]") }

func (d *Dispatcher) secretsUsed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.used) > 0
}

// textual — доказательство с текстом (маскируется); изображения — нет.
func textual(mime string) bool {
	m := strings.ToLower(mime)
	return m == "" || strings.HasPrefix(m, "text/") || strings.Contains(m, "json") || strings.Contains(m, "xml") ||
		strings.Contains(m, "javascript") || strings.Contains(m, "x-www-form-urlencoded")
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

// handle разбирает запрос функции хоста и вызывает её обработчик.
func handle[Req, Res any](req []byte, f func(Req) (Res, error)) (any, error) {
	var r Req
	if err := json.Unmarshal(req, &r); err != nil {
		return nil, abi.Errorf(abi.CodeBadRequest, "%v", err)
	}
	return f(r)
}

func (d *Dispatcher) call(fn string, req []byte) (any, error) {
	switch fn {
	case abi.FnHTTPRequest:
		return handle(req, d.http)
	case abi.FnTLSDial:
		return handle(req, d.dial)
	case abi.FnConnRead:
		return handle(req, func(r abi.ConnRead) (any, error) {
			c, err := d.conn(r.Conn)
			if err != nil {
				return nil, err
			}
			return c.read(r.Max, r.WaitMS), nil
		})
	case abi.FnConnWrite:
		return handle(req, func(r abi.ConnWrite) (any, error) {
			c, err := d.conn(r.Conn)
			if err != nil {
				return nil, err
			}
			_ = c.c.SetWriteDeadline(time.Now().Add(d.timeout()))
			n, err := c.c.Write(r.Data)
			if err != nil {
				return nil, abi.Errorf(abi.CodeIO, "write: %v", err)
			}
			return abi.ConnWriteResponse{N: n}, nil
		})
	case abi.FnConnClose:
		return handle(req, func(r abi.ConnClose) (any, error) {
			d.mu.Lock()
			c := d.conns[r.Conn]
			delete(d.conns, r.Conn)
			d.mu.Unlock()
			if c != nil {
				c.close()
			}
			return struct{}{}, nil
		})
	case abi.FnFSRead, abi.FnFSWrite, abi.FnFSAppend, abi.FnFSList, abi.FnFSStat:
		return handle(req, func(r abi.FSRequest) (any, error) { return d.fs(fn, r) })
	case abi.FnKVGet, abi.FnKVSet, abi.FnKVDelete:
		return handle(req, func(r abi.KVRequest) (any, error) { return d.kv(fn, r) })
	case abi.FnEvidenceAdd:
		return handle(req, func(r abi.EvidenceItem) (any, error) {
			if textual(r.MIME) {
				r.Data = d.maskBytes(r.Data)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.evSize+len(r.Data) > MaxEvidence {
				return nil, abi.Errorf(abi.CodeLimit, "evidence is larger than %d bytes", MaxEvidence)
			}
			d.evSize += len(r.Data)
			d.evidence = append(d.evidence, r)
			return struct{}{}, nil
		})
	case abi.FnLog:
		return handle(req, func(r abi.LogRequest) (any, error) {
			if d.Log != nil {
				d.Log(r.Level, d.Mask(r.Msg))
			}
			return struct{}{}, nil
		})
	case abi.FnServiceCall:
		return handle(req, d.service)
	}
	return nil, abi.Errorf(abi.CodeNotSupported, "unknown host function %q", fn)
}

var (
	placeholderRe = regexp.MustCompile(`\{\{secret:(@[a-zA-Z0-9_.\-]+)\}\}`)
	// secretValueRe — значение заголовка с секретом: весь заголовок —
	// плейсхолдер, перед ним может стоять схема ("Bearer ", "Token ").
	secretValueRe = regexp.MustCompile(`^((?:[A-Za-z][A-Za-z0-9._~+/=-]{0,31} )?)\{\{secret:(@[a-zA-Z0-9_.\-]+)\}\}$`)
	// secretHeaderRe — заголовки, куда можно подставить секрет: заголовки
	// аутентификации, а не произвольные (их сервер может отразить в ответе).
	secretHeaderRe = regexp.MustCompile(`(?i)^(authorization|cookie)$|key|token|auth|secret|signature|session|password|credential`)
)

// substituteHeader подставляет секрет в заголовок name: только в заголовок
// аутентификации и только целиком (схема + плейсхолдер).
func (d *Dispatcher) substituteHeader(name, v, scheme, host string, port int) (string, error) {
	if !strings.Contains(v, "{{secret:") {
		return v, nil
	}
	// Сначала — право на секрет для этого адреса (capability_denied), затем —
	// место подстановки.
	for _, m := range placeholderRe.FindAllStringSubmatch(v, -1) {
		if err := d.Check.Secret(m[1], scheme, host, port); err != nil {
			return "", err
		}
	}
	m := secretValueRe.FindStringSubmatch(v)
	if m == nil || !secretHeaderRe.MatchString(name) {
		return "", abi.Errorf(abi.CodeBadRequest, "a secret may only be the whole value of an authentication header (Authorization: Bearer {{secret:@alias}}), not in %s", name)
	}
	val, err := d.secret(m[2], scheme, host, port)
	if err != nil {
		return "", err
	}
	return m[1] + val, nil
}

// noSecretHeaders — заголовки запроса, которые коннектор не задаёт: сжатие
// ответа (маскирование не видит значение в сжатом теле) и частичные ответы
// (часть значения). Сжатие согласует и снимает сам хост.
var noSecretHeaders = []string{"Accept-Encoding", "Te", "Range", "If-Range"}

func (d *Dispatcher) http(r abi.HTTPRequest) (abi.HTTPResponse, error) {
	var out abi.HTTPResponse
	if strings.Contains(r.URL, "{{secret:") {
		return out, abi.Errorf(abi.CodeBadRequest, "secrets are not allowed in the URL (use an authentication header)")
	}
	if bytes.Contains(r.Body, []byte("{{secret:")) {
		return out, abi.Errorf(abi.CodeBadRequest, "secrets are not allowed in the body (use an authentication header)")
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
	timeout := d.timeout()
	if r.TimeoutMS > 0 && time.Duration(r.TimeoutMS)*time.Millisecond < timeout {
		timeout = time.Duration(r.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(d.ctx(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(r.Body))
	if err != nil {
		return out, abi.Errorf(abi.CodeBadRequest, "%v", err)
	}
	for k, v := range r.Headers {
		if v, err = d.substituteHeader(k, v, u.Scheme, host, port); err != nil {
			return out, err
		}
		req.Header.Set(k, v)
	}
	for _, h := range noSecretHeaders {
		req.Header.Del(h)
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
	// Тело, которое хост не распаковал (сервер сжал без запроса), маскирование
	// не видит: если в вызове подставлялись секреты — отказ.
	if ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); ce != "" && ce != "identity" && !resp.Uncompressed && d.secretsUsed() {
		return out, abi.Errorf(abi.CodeIO, "%s %s: unsupported Content-Encoding %q in a call that uses secrets", method, host, ce)
	}
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
	c      net.Conn
	mu     sync.Mutex
	buf    []byte
	eof    bool
	closed bool
	ready  chan struct{}
	space  *sync.Cond // буфер освободился или соединение закрыто
}

func newConn(nc net.Conn, greeting []byte) *conn {
	c := &conn{c: nc, buf: append([]byte(nil), greeting...), ready: make(chan struct{}, 1)}
	c.space = sync.NewCond(&c.mu)
	return c
}

func (c *conn) close() {
	c.mu.Lock()
	c.closed = true
	c.space.Broadcast()
	c.mu.Unlock()
	c.c.Close()
}

func (c *conn) pump() {
	b := make([]byte, 32<<10)
	for {
		// Модуль не забирает данные — дальше не читаем (предел памяти).
		c.mu.Lock()
		for len(c.buf) >= MaxConnBuffer && !c.closed {
			c.space.Wait()
		}
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}
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
	waitMS = min(waitMS, maxConnReadWait)
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
	if n > 0 {
		c.space.Broadcast()
	}
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
		v, err := d.secret(r.Auth.Secret, scheme, host, port)
		if err != nil {
			return out, err
		}
		cfg.Password = v
	}
	// Слот занимается до dial (параллельные conn_open не превысят MaxConns) и
	// освобождается при ошибке или после вставки в conns.
	d.mu.Lock()
	if len(d.conns)+d.dialing >= MaxConns {
		d.mu.Unlock()
		return out, abi.Errorf(abi.CodeLimit, "too many open connections")
	}
	d.dialing++
	d.mu.Unlock()
	nc, err := netauth.Dial(d.ctx(), cfg)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialing--
	if err != nil {
		return out, err
	}
	c := newConn(nc, nc.Greeting)
	go c.pump()
	if d.conns == nil {
		d.conns = map[int]*conn{}
	}
	d.next++
	d.conns[d.next] = c
	out.Conn = d.next
	return out, nil
}

// errSwapped — компонент пути подменён между проверкой и открытием.
var errSwapped = errors.New("path changed during the operation (symlink swap?)")

// openRoot открывает разрешённый каталог как os.Root (дальше файлы
// открываются относительно дескриптора, симлинки — только внутри корня) и
// проверяет, что дескриптор — тот самый каталог: путь по-прежнему без
// симлинков и указывает на тот же каталог (подмена между проверкой и
// открытием — ошибка).
func openRoot(dir string) (*os.Root, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	st, err := r.Stat(".")
	if err == nil {
		var li os.FileInfo
		var real string
		real, err = filepath.EvalSymlinks(dir)
		if err == nil {
			li, err = os.Lstat(dir)
		}
		if err == nil && (real != dir || !os.SameFile(st, li)) {
			err = errSwapped
		}
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// sameEntry — открытый файл f — это запись rel корня, а не симлинк на другой
// файл (для правил без /**: разрешён только сам файл).
func sameEntry(r *os.Root, rel string, f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	li, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if li.Mode()&os.ModeSymlink != 0 || !os.SameFile(fi, li) {
		return errSwapped
	}
	return nil
}

func rootSize(r *os.Root, rel string) int64 {
	fi, err := r.Stat(rel)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (d *Dispatcher) fs(fn string, r abi.FSRequest) (any, error) {
	loc, err := d.Check.Locate(r.Path)
	if err != nil {
		return nil, err
	}
	return d.fsAt(fn, r, loc)
}

// fsAt — операция над уже проверенным путём: открытие идёт относительно
// корня, так что подмена компонента пути после проверки не выводит за него.
func (d *Dispatcher) fsAt(fn string, r abi.FSRequest, loc capability.Location) (any, error) {
	real := loc.Real
	ioErr := func(err error) error {
		if os.IsNotExist(err) {
			return abi.Errorf(abi.CodeNotFound, "%s: no such file or directory", r.Path)
		}
		return abi.Errorf(abi.CodeIO, "%v", err)
	}
	root, err := openRoot(loc.Root)
	if err != nil {
		if os.IsNotExist(err) && fn == abi.FnFSStat {
			return abi.FSStatResponse{Size: -1, RealPath: real}, nil
		}
		return nil, ioErr(err)
	}
	defer root.Close()
	rel := loc.Rel
	open := func(flag int, perm os.FileMode) (*os.File, error) {
		f, err := root.OpenFile(rel, flag, perm)
		if err != nil {
			return nil, err
		}
		if loc.Exact {
			if err := sameEntry(root, rel, f); err != nil {
				f.Close()
				return nil, err
			}
		}
		return f, nil
	}
	switch fn {
	case abi.FnFSStat:
		fi, err := root.Stat(rel)
		if err != nil {
			if os.IsNotExist(err) {
				return abi.FSStatResponse{Size: -1, RealPath: real}, nil
			}
			return nil, ioErr(err)
		}
		return abi.FSStatResponse{Exists: true, Dir: fi.IsDir(), Size: fi.Size(), RealPath: real}, nil
	case abi.FnFSRead:
		limit := min(r.Max, MaxFSRead)
		if limit <= 0 {
			limit = DefaultFSRead
		}
		f, err := open(os.O_RDONLY, 0)
		if err != nil {
			return nil, ioErr(err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return nil, ioErr(err)
		}
		if fi.IsDir() {
			return nil, abi.Errorf(abi.CodeBadRequest, "%s is a directory", r.Path)
		}
		if r.Offset != 0 { // с позиции; отрицательное — столько байт с конца
			off := r.Offset
			if off < 0 {
				off = max(fi.Size()+off, 0)
			}
			if _, err := f.Seek(min(off, fi.Size()), io.SeekStart); err != nil {
				return nil, ioErr(err)
			}
		}
		b, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return nil, ioErr(err)
		}
		out := abi.FSReadResponse{Size: fi.Size(), RealPath: real}
		if int64(len(b)) > limit {
			b, out.Truncated = b[:limit], true
		}
		out.Data = b
		return out, nil
	case abi.FnFSList:
		f, err := open(os.O_RDONLY, 0)
		if err != nil {
			return nil, ioErr(err)
		}
		defer f.Close()
		ents, err := f.ReadDir(-1)
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
		before := rootSize(root, rel)
		f, err := open(os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return nil, ioErr(err)
		}
		if _, err := f.Write(r.Data); err != nil {
			f.Close()
			return nil, ioErr(err)
		}
		after := int64(-1)
		if fi, err := f.Stat(); err == nil {
			after = fi.Size()
		}
		if err := f.Close(); err != nil {
			return nil, ioErr(err)
		}
		return abi.FSWriteResponse{SizeBefore: before, SizeAfter: after, RealPath: real}, nil
	case abi.FnFSWrite:
		before := rootSize(root, rel)
		// Временный файл в том же каталоге корня, затем rename внутри корня:
		// запись не уходит за его пределы, даже если путь подменят.
		tmpRel := filepath.Join(filepath.Dir(rel), ".gate4ai-"+strconv.FormatInt(time.Now().UnixNano(), 36))
		tmp, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return nil, ioErr(err)
		}
		_, werr := tmp.Write(r.Data)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = root.Rename(tmpRel, rel)
		}
		if werr != nil {
			_ = root.Remove(tmpRel)
			return nil, ioErr(werr)
		}
		return abi.FSWriteResponse{SizeBefore: before, SizeAfter: rootSize(root, rel), RealPath: real}, nil
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
		c.close()
	}
}
