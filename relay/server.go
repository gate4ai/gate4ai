// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Server — relay.
type Server struct {
	Domain   string      // relay.gate4.ai: устройства — <id>.relay.gate4.ai
	TLS      *tls.Config // сертификат самого relay (для туннеля и API)
	Store    *Store
	Log      *slog.Logger
	HoldFor  time.Duration // сколько держать входящее при офлайн-устройстве (~30 с)
	AllowReg bool          // открытая регистрация устройств
	// Routes — сторонние имена на том же порту (сайт gate4.ai и т. п.): SNI →
	// TCP-адрес бэкенда. Трафик пробрасывается без расшифровки, TLS (и ACME
	// TLS-ALPN-01) завершает бэкенд.
	Routes map[string]string

	// Metrics — метрики Prometheus (страница — Metrics.Handler).
	Metrics *Metrics

	mu       sync.Mutex
	sessions map[string]*yamux.Session
	waiters  map[string][]chan struct{}
}

// NewServer создаёт relay.
func NewServer(domain string, tlsCfg *tls.Config, st *Store) *Server {
	s := &Server{Domain: strings.ToLower(domain), TLS: tlsCfg, Store: st, Log: slog.Default(), HoldFor: 30 * time.Second,
		AllowReg: true, Routes: map[string]string{}, sessions: map[string]*yamux.Session{}, waiters: map[string][]chan struct{}{}}
	s.Metrics = newMetrics(s)
	return s
}

// DeviceHost — публичное имя устройства.
func (s *Server) DeviceHost(id string) string { return id + "." + s.Domain }

// AddRoute пробрасывает соединения с SNI host на TCP-адрес addr. Имена relay
// и его устройств занять нельзя.
func (s *Server) AddRoute(host, addr string) error {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || addr == "" {
		return errors.New("route: need host=addr")
	}
	if host == s.Domain || strings.HasSuffix(host, "."+s.Domain) {
		return fmt.Errorf("route %s: names under %s belong to the relay and its devices", host, s.Domain)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("route %s: %w", host, err)
	}
	s.Routes[host] = addr
	return nil
}

// Serve принимает TCP-соединения (порт 443).
func (s *Server) Serve(l net.Listener) error {
	api := &http.Server{Handler: s.apiHandler(), ReadHeaderTimeout: 10 * time.Second, ConnState: s.apiConnState}
	apiConns := newChanListener(l.Addr())
	go api.Serve(tls.NewListener(apiConns, s.TLS))
	defer api.Close()
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handle(c, apiConns)
	}
}

func (s *Server) handle(c net.Conn, api *chanListener) {
	m := s.Metrics
	sni, prefix, err := PeekSNI(c)
	if err != nil {
		m.rejected.WithLabelValues("bad_hello").Inc()
		c.Close()
		return
	}
	sni = strings.ToLower(sni)
	if sni == s.Domain {
		m.conns.WithLabelValues("api").Inc()
		api.push(WithPrefix(c, prefix))
		return
	}
	if addr, ok := s.Routes[sni]; ok {
		s.forward(c, prefix, sni, addr)
		return
	}
	id, ok := strings.CutSuffix(sni, "."+s.Domain)
	if !ok || id == "" || strings.Contains(id, ".") {
		m.rejected.WithLabelValues("unknown_host").Inc()
		c.Close()
		return
	}
	if !s.Store.Exists(id) {
		m.rejected.WithLabelValues("unknown_device").Inc()
		c.Close()
		return
	}
	m.conns.WithLabelValues("device").Inc()
	_, span := tracer.Start(context.Background(), "relay.passthrough device", trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("relay.kind", "device"), attribute.String("relay.device", id)))
	defer span.End()
	t0 := time.Now()
	sess := s.waitSession(id)
	m.wait.Observe(time.Since(t0).Seconds())
	if sess == nil {
		m.rejected.WithLabelValues("device_offline").Inc()
		span.SetStatus(codes.Error, "device offline")
		c.Close() // устройство офлайн: очереди нет, агент повторит позже
		return
	}
	span.AddEvent("tunnel ready")
	st, err := sess.Open()
	if err != nil {
		m.rejected.WithLabelValues("stream").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, "open stream")
		c.Close()
		return
	}
	// Passthrough: relay не расшифровывает трафик.
	s.pipe(span, "device", c, st, prefix, func() { st.Close() })
}

// pipe пробрасывает c ↔ up (сначала prefix в up) и учитывает байты; closeUp
// вызывается, когда клиент закончил писать.
func (s *Server) pipe(span trace.Span, kind string, c net.Conn, up io.ReadWriteCloser, prefix []byte, closeUp func()) {
	m := s.Metrics
	m.active.WithLabelValues(kind).Inc()
	defer m.active.WithLabelValues(kind).Dec()
	in := make(chan int64, 1)
	go func() {
		_, _ = up.Write(prefix)
		n, _ := io.Copy(up, c)
		closeUp()
		in <- int64(len(prefix)) + n
	}()
	out, _ := io.Copy(c, up)
	c.Close()
	up.Close()
	nin := <-in
	m.bytes.WithLabelValues(kind, "in").Add(float64(nin))
	m.bytes.WithLabelValues(kind, "out").Add(float64(out))
	span.SetAttributes(attribute.Int64("relay.bytes_in", nin), attribute.Int64("relay.bytes_out", out))
}

// forward пробрасывает соединение на бэкенд маршрута, не расшифровывая его.
func (s *Server) forward(c net.Conn, prefix []byte, host, addr string) {
	m := s.Metrics
	m.conns.WithLabelValues("route").Inc()
	_, span := tracer.Start(context.Background(), "relay.passthrough route", trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("relay.kind", "route"), attribute.String("relay.host", host), attribute.String("relay.backend", addr)))
	defer span.End()
	b, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		m.rejected.WithLabelValues("backend").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, "backend unreachable")
		s.Log.Warn("route backend unreachable", "host", host, "backend", addr, "err", err)
		c.Close()
		return
	}
	s.pipe(span, "route", c, b, prefix, func() {
		if tc, ok := b.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	})
}

// SetHoldFor меняет время удержания входящих соединений офлайн-устройства.
func (s *Server) SetHoldFor(d time.Duration) {
	s.mu.Lock()
	s.HoldFor = d
	s.mu.Unlock()
}

func (s *Server) waitSession(id string) *yamux.Session {
	s.mu.Lock()
	deadline := time.Now().Add(s.HoldFor)
	s.mu.Unlock()
	for {
		s.mu.Lock()
		sess := s.sessions[id]
		if sess != nil && sess.IsClosed() {
			delete(s.sessions, id)
			sess = nil
		}
		if sess != nil {
			s.mu.Unlock()
			return sess
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			s.mu.Unlock()
			return nil
		}
		ch := make(chan struct{})
		s.waiters[id] = append(s.waiters[id], ch)
		s.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(wait):
		}
	}
}

// apiConnState считает открытые соединения API; туннели после Hijack
// учитываются в gate4ai_relay_devices_online.
func (s *Server) apiConnState(_ net.Conn, st http.ConnState) {
	switch st {
	case http.StateNew:
		s.Metrics.active.WithLabelValues("api").Inc()
	case http.StateClosed, http.StateHijacked:
		s.Metrics.active.WithLabelValues("api").Dec()
	}
}

// onlineCount — число устройств с открытым туннелем.
func (s *Server) onlineCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sess := range s.sessions {
		if !sess.IsClosed() {
			n++
		}
	}
	return n
}

// Online — подключено ли устройство.
func (s *Server) Online(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	return sess != nil && !sess.IsClosed()
}

// Disconnect обрывает туннель устройства (администрирование и тесты);
// устройство переподключится само.
func (s *Server) Disconnect(id string) {
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
}

func deviceAuth(r *http.Request) (string, string) {
	id := r.Header.Get("X-Gate4ai-Device")
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return id, tok
}

func (s *Server) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		if !s.AllowReg {
			http.Error(w, "registration closed", 403)
			return
		}
		id, tok, err := s.Store.Register()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]string{"device_id": id, "token": tok, "host": s.DeviceHost(id)})
	})
	mux.HandleFunc("GET /tunnel", func(w http.ResponseWriter, r *http.Request) {
		id, tok := deviceAuth(r)
		if !s.Store.Auth(id, tok) {
			http.Error(w, "unauthorized", 401)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(-1)
		conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		cfg := yamux.DefaultConfig()
		cfg.LogOutput = io.Discard
		cfg.KeepAliveInterval = 15 * time.Second
		sess, err := yamux.Client(conn, cfg)
		if err != nil {
			conn.Close()
			return
		}
		s.mu.Lock()
		if old := s.sessions[id]; old != nil {
			old.Close()
		}
		s.sessions[id] = sess
		for _, ch := range s.waiters[id] {
			close(ch)
		}
		delete(s.waiters, id)
		s.mu.Unlock()
		s.Metrics.tunnels.Inc()
		s.Log.Info("device connected", "device", id)
		<-sess.CloseChan()
		s.Log.Info("device disconnected", "device", id)
	})
	// ACME DNS-01: устройство ставит TXT только для своего имени.
	mux.HandleFunc("POST /api/acme/txt", func(w http.ResponseWriter, r *http.Request) { s.txt(w, r, true) })
	mux.HandleFunc("DELETE /api/acme/txt", func(w http.ResponseWriter, r *http.Request) { s.txt(w, r, false) })
	mux.HandleFunc("POST /api/witness", func(w http.ResponseWriter, r *http.Request) {
		id, tok := deviceAuth(r)
		if !s.Store.Auth(id, tok) {
			http.Error(w, "unauthorized", 401)
			return
		}
		var cp WitnessedCheckpoint
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&cp); err != nil || len(cp.Head) != 64 {
			http.Error(w, "bad checkpoint", 400)
			return
		}
		cp.ReceivedAt = time.Now().UTC()
		if err := s.Store.AddCheckpoint(id, cp); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/witness/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.Store.Checkpoints(r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"ok": true}) })
	return traceAPI(s.Metrics.instrument(mux))
}

func (s *Server) txt(w http.ResponseWriter, r *http.Request, set bool) {
	id, tok := deviceAuth(r)
	if !s.Store.Auth(id, tok) {
		http.Error(w, "unauthorized", 401)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	want := "_acme-challenge." + s.DeviceHost(id)
	if strings.ToLower(strings.TrimSuffix(req.Name, ".")) != want {
		http.Error(w, "a device may only set "+want, 403)
		return
	}
	var err error
	if set {
		err = s.Store.SetDeviceTXT(id, want, req.Value)
	} else {
		err = s.Store.DelTXT(want, req.Value)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// chanListener — net.Listener из канала соединений.
type chanListener struct {
	ch     chan net.Conn
	addr   net.Addr
	once   sync.Once
	closed chan struct{}
}

func newChanListener(a net.Addr) *chanListener {
	return &chanListener{ch: make(chan net.Conn), addr: a, closed: make(chan struct{})}
}

func (l *chanListener) push(c net.Conn) {
	select {
	case l.ch <- c:
	case <-l.closed:
		c.Close()
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, errors.New("listener closed")
	}
}

func (l *chanListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }
