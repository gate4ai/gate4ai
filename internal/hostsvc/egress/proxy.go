// SPDX-License-Identifier: AGPL-3.0-only

// Package egress — Egress Proxy: единственный сетевой выход изолированных
// агентов. Allowlist доменов (allow / ask / deny), подстановка секретов на
// выходе только для нужного домена (агент видит заглушку), журнал соединений.
package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Injection — подстановка секрета в заголовок запроса к домену.
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
	// Auth сопоставляет учётные данные прокси с агентом запуска.
	Auth func(user, pass string) (agent string, ok bool)
	// Decide — решение по домену (allow / deny с причиной; ask ждёт человека внутри).
	Decide func(ctx context.Context, agent, host string, port int) (bool, string)
	// Secrets — подстановки для агента и домена.
	Secrets func(agent, host string) []Injection
	CA      *CA
	// UpstreamTLS — проверка сертификатов настоящих серверов (nil — системные CA).
	UpstreamTLS *tls.Config
	Dial        func(ctx context.Context, network, addr string) (net.Conn, error)
	Log         func(Record)
}

func (p *Proxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, "tcp", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (p *Proxy) log(r Record) {
	if p.Log != nil {
		r.TS = time.Now().UTC()
		p.Log(r)
	}
}

func (p *Proxy) agent(r *http.Request) (string, bool) {
	h := r.Header.Get("Proxy-Authorization")
	enc, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	u, pw, ok := strings.Cut(string(b), ":")
	if !ok {
		return "", false
	}
	return p.Auth(u, pw)
}

func splitHostPort(hp string, def int) (string, int) {
	h, ps, err := net.SplitHostPort(hp)
	if err != nil {
		return strings.ToLower(hp), def
	}
	n, _ := strconv.Atoi(ps)
	return strings.ToLower(h), n
}

// ServeHTTP — CONNECT (HTTPS) и обычные HTTP-запросы с абсолютным URI.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agent, ok := p.agent(r)
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="gate4ai"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	r.Header.Del("Proxy-Authorization")
	if r.Method == http.MethodConnect {
		p.connect(w, r, agent)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "gate4ai egress proxy", 400)
		return
	}
	host, port := splitHostPort(r.URL.Host, map[string]int{"http": 80, "https": 443}[r.URL.Scheme])
	rec := Record{Agent: agent, Host: host, Port: port, Method: r.Method}
	allow, reason := p.Decide(r.Context(), agent, host, port)
	if !allow {
		rec.Decision, rec.Reason = "deny", reason
		p.log(rec)
		http.Error(w, "denied_by_policy: egress to "+host+" is not allowed ("+reason+")", http.StatusForbidden)
		return
	}
	rec.Decision = "allow"
	p.inject(r, agent, host)
	r.RequestURI = ""
	tr := &http.Transport{DialContext: p.Dial, TLSClientConfig: p.UpstreamTLS, Proxy: nil}
	resp, err := tr.RoundTrip(r)
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

func (p *Proxy) inject(r *http.Request, agent, host string) bool {
	if p.Secrets == nil {
		return false
	}
	did := false
	for _, inj := range p.Secrets(agent, host) {
		if v := r.Header.Get(inj.Header); v != "" && strings.Contains(v, inj.Placeholder) {
			r.Header.Set(inj.Header, strings.ReplaceAll(v, inj.Placeholder, inj.Value))
			did = true
		}
	}
	return did
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

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, agent string) {
	host, port := splitHostPort(r.Host, 443)
	rec := Record{Agent: agent, Host: host, Port: port, Method: "CONNECT"}
	allow, reason := p.Decide(r.Context(), agent, host, port)
	if !allow {
		rec.Decision, rec.Reason = "deny", reason
		p.log(rec)
		http.Error(w, "denied_by_policy: egress to "+host+" is not allowed ("+reason+")", http.StatusForbidden)
		return
	}
	rec.Decision = "allow"
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", 500)
		return
	}
	mitm := p.CA != nil && p.Secrets != nil && len(p.Secrets(agent, host)) > 0
	var up net.Conn
	if !mitm {
		var err error
		if up, err = p.dial(r.Context(), net.JoinHostPort(host, strconv.Itoa(port))); err != nil {
			rec.Reason = err.Error()
			p.log(rec)
			http.Error(w, err.Error(), http.StatusBadGateway)
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
		p.serveMITM(client, buf, agent, host, port, &rec)
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

// serveMITM расшифровывает HTTPS к домену с подстановкой секретов, подставляет
// реальный ключ и отправляет запрос настоящему серверу с проверкой его сертификата.
func (p *Proxy) serveMITM(client net.Conn, buf *bufio.ReadWriter, agent, host string, port int, rec *Record) {
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
	upCfg := &tls.Config{ServerName: host}
	if p.UpstreamTLS != nil {
		upCfg = p.UpstreamTLS.Clone()
		upCfg.ServerName = host
	}
	tr := &http.Transport{DialContext: p.Dial, TLSClientConfig: upCfg, ForceAttemptHTTP2: false}
	defer tr.CloseIdleConnections()
	br := bufio.NewReader(tc)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		req.URL.Scheme, req.URL.Host = "https", net.JoinHostPort(host, strconv.Itoa(port))
		req.RequestURI = ""
		p.inject(req, agent, host)
		resp, err := tr.RoundTrip(req)
		if err != nil {
			_, _ = tc.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
			rec.Reason = err.Error()
			return
		}
		b, err := httputil.DumpResponse(resp, true)
		resp.Body.Close()
		if err != nil {
			return
		}
		rec.Down += int64(len(b))
		if _, err := tc.Write(b); err != nil {
			return
		}
		if resp.Close || req.Close {
			return
		}
	}
}

type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c prefixConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// ErrDenied — соединение запрещено.
var ErrDenied = errors.New("egress denied")
