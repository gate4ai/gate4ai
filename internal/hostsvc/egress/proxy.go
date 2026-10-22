// SPDX-License-Identifier: AGPL-3.0-only

// Package egress — Egress Proxy: единственный сетевой выход изолированных
// агентов. Allowlist доменов (allow / ask / deny), подстановка секретов на
// выходе только для origin привязки и только в HTTPS (агент видит заглушку),
// маскирование ответов, журнал соединений.
package egress

import (
	"bufio"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs/mask"
)

// MinPlaceholder — минимальная длина заглушки: короткая совпала бы с чем
// угодно внутри заголовка. Подстановки с более короткой заглушкой не действуют.
const MinPlaceholder = 16

// Injection — подстановка секрета в заголовок запроса к origin привязки.
type Injection struct {
	Header      string
	Placeholder string
	Value       string
}

// Record — запись журнала соединений.
type Record struct {
	TS       time.Time `json:"ts"`
	Agent    string    `json:"agent"`
	Host     string    `json:"host"`
	Port     int       `json:"port"`
	Method   string    `json:"method"` // CONNECT | GET | …
	Decision string    `json:"decision"`
	Reason   string    `json:"reason,omitempty"`
	Up       int64     `json:"bytes_up"`
	Down     int64     `json:"bytes_down"`
	MITM     bool      `json:"secret_injection,omitempty"`
}

// Proxy — HTTP(S)-прокси с аутентификацией запуска (Proxy-Authorization).
type Proxy struct {
	// Auth сопоставляет учётные данные прокси с агентом и запуском.
	Auth func(user, pass string) (agent, run string, ok bool)
	// Decide — решение по домену (allow / deny с причиной; ask ждёт человека внутри).
	Decide func(ctx context.Context, agent, host string, port int) (bool, string)
	// Secrets — подстановки для запуска run и origin https://host:port (заглушки
	// этого запуска). Подставляются только в HTTPS (CONNECT с расшифровкой), в
	// обычный HTTP — никогда.
	Secrets func(run, host string, port int) []Injection
	// AllowLocal — агенту явно разрешён адрес этой машины или частной сети
	// host (имя или IP). Иначе соединения на loopback, частные, link-local,
	// multicast и unspecified адреса (после резолва имени) запрещены.
	AllowLocal func(agent, host string) bool
	CA         *CA
	// UpstreamTLS — проверка сертификатов настоящих серверов (nil — системные CA).
	UpstreamTLS *tls.Config
	// Dial — соединение с сервером (тесты). По умолчанию — net.Dialer, который
	// проверяет каждый IP, с которым соединяется (AllowLocal).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	Log  func(Record)

	once sync.Once
	tr   *http.Transport
}

type ctxKey struct{}

// target — агент и хост запроса (для проверки адреса при соединении).
type target struct{ agent, host string }

// ErrLocal — адрес этой машины или частной сети не разрешён явно.
var ErrLocal = errors.New("egress to a local or private address is not allowed")

// blockedIP — адрес этой машины, частной сети, link-local, multicast или unspecified.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

func (p *Proxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, "tcp", addr)
	}
	t, _ := ctx.Value(ctxKey{}).(target)
	allowLocal := p.AllowLocal != nil && t.host != "" && p.AllowLocal(t.agent, t.host)
	// Проверка — в Control: на IP, с которым действительно соединяемся (после
	// резолва), а не на имени (DNS-rebinding, имя на 127.0.0.1 / 169.254.169.254).
	d := net.Dialer{Timeout: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(h); ip == nil || (blockedIP(ip) && !allowLocal) {
			return fmt.Errorf("%w (%s)", ErrLocal, h)
		}
		return nil
	}}
	return d.DialContext(ctx, "tcp", addr)
}

// withTarget — контекст с агентом и хостом для проверки адреса при соединении.
func withTarget(ctx context.Context, agent, host string) context.Context {
	return context.WithValue(ctx, ctxKey{}, target{agent, host})
}

// transport — общий Transport прокси (обычный HTTP и расшифрованный HTTPS).
func (p *Proxy) transport() *http.Transport {
	p.once.Do(func() {
		var cfg *tls.Config
		if p.UpstreamTLS != nil {
			cfg = p.UpstreamTLS.Clone()
			cfg.ServerName = "" // по хосту запроса
		}
		p.tr = &http.Transport{
			DialContext:         func(ctx context.Context, _, addr string) (net.Conn, error) { return p.dial(ctx, addr) },
			TLSClientConfig:     cfg,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 30 * time.Second,
		}
	})
	return p.tr
}

// Close закрывает простаивающие соединения к серверам.
func (p *Proxy) Close() {
	if p.tr != nil {
		p.tr.CloseIdleConnections()
	}
}

func (p *Proxy) log(r Record) {
	if p.Log != nil {
		r.TS = time.Now().UTC()
		p.Log(r)
	}
}

func (p *Proxy) agent(r *http.Request) (agent, run string, ok bool) {
	h := r.Header.Get("Proxy-Authorization")
	enc, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return "", "", false
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", "", false
	}
	u, pw, ok := strings.Cut(string(b), ":")
	if !ok {
		return "", "", false
	}
	return p.Auth(u, pw)
}

func splitHostPort(hp string, def int) (string, int) {
	h, ps, err := net.SplitHostPort(hp)
	if err != nil {
		return strings.TrimSuffix(strings.ToLower(hp), "."), def
	}
	n, _ := strconv.Atoi(ps)
	return strings.TrimSuffix(strings.ToLower(h), "."), n
}

// injections — подстановки запуска для origin https://host:port (с допустимой заглушкой).
func (p *Proxy) injections(run, host string, port int) []Injection {
	if p.Secrets == nil {
		return nil
	}
	var out []Injection
	for _, inj := range p.Secrets(run, host, port) {
		if len(inj.Placeholder) >= MinPlaceholder && inj.Header != "" && inj.Value != "" {
			out = append(out, inj)
		}
	}
	return out
}

// ServeHTTP — CONNECT (HTTPS) и обычные HTTP-запросы с абсолютным URI.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent, run, ok := p.agent(r)
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="gate4ai"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	r.Header.Del("Proxy-Authorization")
	if r.Method == http.MethodConnect {
		p.connect(w, r, agent, run)
		return
	}
	if !r.URL.IsAbs() || r.URL.Scheme != "http" {
		http.Error(w, "gate4ai egress proxy", 400)
		return
	}
	host, port := splitHostPort(r.URL.Host, 80)
	rec := Record{Agent: agent, Host: host, Port: port, Method: r.Method}
	if !p.allowed(w, r, &rec) {
		return
	}
	// Обычный HTTP идёт по сети открытым текстом: секрет сюда не подставляется
	// никогда, а заглушка домена с подстановкой — отказ (агент ошибся схемой).
	if ph := placeholderIn(r, p.injections(run, host, 443)); ph != "" {
		rec.Decision, rec.Reason = "deny", "secret_denied: placeholder over plain http"
		p.log(rec)
		http.Error(w, "secret_denied: secrets are injected only over https (placeholder in a plain http request)", http.StatusForbidden)
		return
	}
	r.RequestURI = ""
	r.Host = ""
	resp, err := p.transport().RoundTrip(r.WithContext(withTarget(r.Context(), agent, host)))
	if err != nil {
		rec.Reason = err.Error()
		p.log(rec)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	rec.Down = n
	p.log(rec)
}

// allowed — решение по домену записи rec; при отказе — запись в журнал и 403.
func (p *Proxy) allowed(w http.ResponseWriter, r *http.Request, rec *Record) bool {
	allow, reason := p.Decide(r.Context(), rec.Agent, rec.Host, rec.Port)
	if !allow {
		rec.Decision, rec.Reason = "deny", reason
		p.log(*rec)
		http.Error(w, "denied_by_policy: egress to "+rec.Host+" is not allowed ("+reason+")", http.StatusForbidden)
		return false
	}
	rec.Decision = "allow"
	return true
}

// placeholderIn — заглушка из injs где-либо в URL или заголовках запроса.
func placeholderIn(r *http.Request, injs []Injection) string {
	for _, inj := range injs {
		if strings.Contains(r.URL.String(), inj.Placeholder) || strings.Contains(r.Host, inj.Placeholder) {
			return inj.Placeholder
		}
		for _, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(v, inj.Placeholder) {
					return inj.Placeholder
				}
			}
		}
	}
	return ""
}

// inject ставит секрет в заголовок привязки: значение заголовка — заглушка
// целиком (можно со схемой: "Bearer <заглушка>"). Заглушка в другом заголовке
// или в URL — отказ secret_denied (возвращается текст ошибки).
func inject(r *http.Request, injs []Injection) string {
	for _, inj := range injs {
		h := http.CanonicalHeaderKey(inj.Header)
		v := r.Header.Get(h)
		if v == "" || len(r.Header.Values(h)) != 1 {
			continue
		}
		if v == inj.Placeholder {
			r.Header.Set(h, inj.Value)
		} else if scheme, rest, ok := strings.Cut(v, " "); ok && rest == inj.Placeholder && !strings.ContainsAny(scheme, " \t") {
			r.Header.Set(h, scheme+" "+inj.Value)
		}
	}
	// После подстановки заглушек остаться не должно.
	if ph := placeholderIn(r, injs); ph != "" {
		return "secret_denied: the placeholder may only be the whole value of its header"
	}
	return ""
}

type countConn struct {
	net.Conn
	n *atomic.Int64
}

func (c countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.n.Add(int64(n))
	return n, err
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, agent, run string) {
	host, port := splitHostPort(r.Host, 443)
	rec := Record{Agent: agent, Host: host, Port: port, Method: "CONNECT"}
	if !p.allowed(w, r, &rec) {
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", 500)
		return
	}
	injs := p.injections(run, host, port)
	mitm := p.CA != nil && len(injs) > 0
	var up net.Conn
	if !mitm {
		var err error
		if up, err = p.dial(withTarget(r.Context(), agent, host), net.JoinHostPort(host, strconv.Itoa(port))); err != nil {
			rec.Reason = err.Error()
			p.log(rec)
			code := http.StatusBadGateway
			if errors.Is(err, ErrLocal) {
				code = http.StatusForbidden
			}
			http.Error(w, err.Error(), code)
			return
		}
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		if up != nil {
			up.Close()
		}
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if mitm {
		rec.MITM = true
		p.serveMITM(client, buf, agent, run, host, port, &rec)
		p.log(rec)
		return
	}
	var upN, downN atomic.Int64
	done := make(chan struct{})
	go func() {
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Reader.Peek(n)
			m, _ := up.Write(b)
			upN.Add(int64(m))
		}
		_, _ = io.Copy(up, countConn{client, &upN})
		if tc, ok := up.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(client, countConn{up, &downN})
	client.Close()
	<-done
	up.Close()
	rec.Up, rec.Down = upN.Load(), downN.Load()
	p.log(rec)
}

// countWriter считает отданные агенту байты.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

func writeStatus(w io.Writer, code int, msg string) {
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(msg), msg)
}

// serveMITM расшифровывает HTTPS к origin с подстановкой секретов, подставляет
// реальный ключ и отправляет запрос настоящему серверу с проверкой его
// сертификата. Host запроса должен совпадать с хостом CONNECT (иначе 421: ключ
// не уйдёт на чужой хост за тем же фронтом). Ответ маскируется (значения и
// производные формы) и отдаётся потоком.
func (p *Proxy) serveMITM(client net.Conn, buf *bufio.ReadWriter, agent, run, host string, port int, rec *Record) {
	defer client.Close()
	leaf, err := p.CA.Leaf(host)
	if err != nil {
		rec.Reason = err.Error()
		return
	}
	var inner net.Conn = client
	if n := buf.Reader.Buffered(); n > 0 {
		b, _ := buf.Reader.Peek(n)
		inner = prefixConn{Conn: client, r: io.MultiReader(strings.NewReader(string(b)), client)}
	}
	tc := tls.Server(inner, &tls.Config{Certificates: []tls.Certificate{*leaf}, NextProtos: []string{"http/1.1"}})
	if err := tc.Handshake(); err != nil {
		rec.Reason = "client TLS: " + err.Error()
		return
	}
	out := &countWriter{w: tc}
	defer func() { rec.Down += out.n }()
	br := bufio.NewReader(tc)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if req.Host != "" {
			if h, hp := splitHostPort(req.Host, port); h != host || hp != port {
				rec.Reason = "misdirected request: Host " + req.Host
				writeStatus(out, http.StatusMisdirectedRequest, "gate4ai: Host does not match the CONNECT target\n")
				return
			}
		}
		// Подстановки перечитываются на каждый запрос (конфигурация могла
		// измениться); их значения — набор маскирования ответа.
		injs := p.injections(run, host, port)
		if msg := inject(req, injs); msg != "" {
			rec.Reason = msg
			writeStatus(out, http.StatusForbidden, msg+"\n")
			return
		}
		req.Host = ""
		req.URL.Scheme, req.URL.Host = "https", net.JoinHostPort(host, strconv.Itoa(port))
		req.RequestURI = ""
		// Сжатый или частичный ответ маскирование не видит целиком.
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Del("Te")
		req.Header.Del("Range")
		req.Header.Del("If-Range")
		resp, err := p.transport().RoundTrip(req.WithContext(withTarget(context.Background(), agent, host)))
		if err != nil {
			rec.Reason = err.Error()
			writeStatus(out, http.StatusBadGateway, "gate4ai: upstream error\n")
			return
		}
		vals := make([]string, 0, len(injs))
		for _, inj := range injs {
			vals = append(vals, inj.Value)
		}
		werr := writeMasked(out, resp, mask.New(vals...))
		resp.Body.Close()
		if werr != nil || resp.Close || req.Close {
			if werr != nil {
				rec.Reason = werr.Error()
			}
			return
		}
	}
}

// writeMasked отдаёт ответ агенту потоком: заголовки и тело маскируются, сжатое
// тело (gzip, deflate) распаковывается до маскирования, неизвестное сжатие — 502.
func writeMasked(w io.Writer, resp *http.Response, ms *mask.Set) error {
	for k, vs := range resp.Header {
		for i, v := range vs {
			vs[i] = ms.String(v, "[secret]")
		}
		resp.Header[k] = vs
	}
	switch ce := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); ce {
	case "", "identity":
	case "gzip", "x-gzip", "deflate":
		var body io.ReadCloser
		var err error
		if ce == "deflate" {
			body, err = zlib.NewReader(resp.Body)
		} else {
			body, err = gzip.NewReader(resp.Body)
		}
		if err != nil {
			writeStatus(w, http.StatusBadGateway, "gate4ai: bad compressed response\n")
			return err
		}
		resp.Body = body
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
	default:
		writeStatus(w, http.StatusBadGateway, "gate4ai: unsupported Content-Encoding "+ce+"\n")
		return fmt.Errorf("unsupported Content-Encoding %q", ce)
	}
	if resp.ContentLength < 0 && len(resp.TransferEncoding) == 0 && resp.Request != nil && resp.Request.Method != http.MethodHead {
		resp.TransferEncoding = []string{"chunked"}
	}
	resp.Body = io.NopCloser(ms.Reader(resp.Body)) // длина не меняется: Content-Length верен
	return resp.Write(w)
}

type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c prefixConn) Read(b []byte) (int, error) { return c.r.Read(b) }
