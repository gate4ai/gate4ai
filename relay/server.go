// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	// proxyRoutes — маршруты, бэкенду которых relay первым отправляет заголовок
	// PROXY protocol v2 с настоящим адресом клиента (--route host=addr,proxy).
	proxyRoutes map[string]bool

	// Metrics — метрики Prometheus (страница — Metrics.Handler).
	Metrics *Metrics

	// Пределы (0 — без предела). Меняются до Serve.
	MaxConns      int           // одновременных TCP-соединений всего
	MaxConnsPerIP int           // одновременных TCP-соединений с одного адреса (IPv6 — /64)
	MaxWaiters    int           // соединений, ждущих туннель одного офлайн-устройства
	PipeIdle      time.Duration // passthrough без данных в обе стороны дольше — закрывается
	// RegPerIPDay и RegPerDay — регистраций устройств в сутки с одного адреса и всего.
	RegPerIPDay, RegPerDay int

	mu       sync.Mutex
	sessions map[string]*yamux.Session
	waiters  map[string][]chan struct{}

	limOnce             sync.Once
	conns               *connLimiter
	regIP, regAll, wits *rateLimiter
}

// NewServer создаёт relay.
func NewServer(domain string, tlsCfg *tls.Config, st *Store) *Server {
	s := &Server{Domain: strings.ToLower(domain), TLS: tlsCfg, Store: st, Log: slog.Default(), HoldFor: 30 * time.Second,
		AllowReg: true, Routes: map[string]string{}, proxyRoutes: map[string]bool{},
		MaxConns: 20000, MaxConnsPerIP: 256, MaxWaiters: 32, PipeIdle: 10 * time.Minute, RegPerIPDay: 5, RegPerDay: 500,
		sessions: map[string]*yamux.Session{}, waiters: map[string][]chan struct{}{}}
	s.Metrics = newMetrics(s)
	return s
}

// DeviceHost — публичное имя устройства.
func (s *Server) DeviceHost(id string) string { return id + "." + s.Domain }

// AddRoute пробрасывает соединения с SNI host на TCP-адрес addr. Имена relay
// и его устройств занять нельзя. addr с суффиксом ",proxy" — бэкенд получает
// заголовок PROXY protocol v2 (настоящий адрес клиента вместо 127.0.0.1).
func (s *Server) AddRoute(host, addr string) error {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	addr, proxy := strings.CutSuffix(addr, ",proxy")
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
	if proxy {
		s.proxyRoutes[host] = true
	}
	return nil
}

// limits создаёт лимитеры по значениям полей (один раз, при первом использовании).
func (s *Server) limits() {
	s.limOnce.Do(func() {
		s.conns = &connLimiter{max: s.MaxConns, maxIP: s.MaxConnsPerIP}
		if s.RegPerIPDay > 0 {
			s.regIP = newRateLimiter(24*time.Hour/time.Duration(s.RegPerIPDay), s.RegPerIPDay)
		}
		if s.RegPerDay > 0 {
			s.regAll = newRateLimiter(24*time.Hour/time.Duration(s.RegPerDay), s.RegPerDay)
		}
		// Чекпоинты: журнал шлёт их раз в 100 записей или раз в сутки; запас — 60
		// сразу (повтор неотправленных), дальше один в 10 с.
		s.wits = newRateLimiter(10*time.Second, 60)
	})
}

// Serve принимает TCP-соединения (порт 443).
// Соединения сверх MaxConns / MaxConnsPerIP сбрасываются сразу.
func (s *Server) Serve(l net.Listener) error {
	s.limits()
	// IdleTimeout: keep-alive соединение API не висит вечно; чтение тела и
	// ответ ограничивает apiDeadlines (кроме /tunnel — у него keepalive yamux).
	api := &http.Server{Handler: s.apiHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		ConnState: s.apiConnState}
	apiConns := newChanListener(l.Addr())
	go api.Serve(tls.NewListener(apiConns, s.TLS))
	defer api.Close()
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		release, ok := s.conns.acquire(ipKey(c.RemoteAddr()))
		if !ok {
			s.Metrics.rejected.WithLabelValues("conn_limit").Inc()
			c.Close()
			continue
		}
		go s.handle(&releaseConn{Conn: c, release: release}, apiConns)
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
		s.forward(c, prefix, sni, addr, s.proxyRoutes[sni])
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
// вызывается, когда клиент закончил писать. Соединение без данных в обе
// стороны дольше PipeIdle закрывается.
func (s *Server) pipe(span trace.Span, kind string, c net.Conn, up io.ReadWriteCloser, prefix []byte, closeUp func()) {
	m := s.Metrics
	m.active.WithLabelValues(kind).Inc()
	defer m.active.WithLabelValues(kind).Dec()
	var act atomic.Int64
	act.Store(time.Now().UnixNano())
	in := make(chan int64, 1)
	go func() {
		_, _ = up.Write(prefix)
		n, _ := idleCopy(up, c, &act, s.PipeIdle)
		closeUp()
		in <- int64(len(prefix)) + n
	}()
	out, _ := idleCopy(c, up, &act, s.PipeIdle)
	c.Close()
	up.Close()
	nin := <-in
	m.bytes.WithLabelValues(kind, "in").Add(float64(nin))
	m.bytes.WithLabelValues(kind, "out").Add(float64(out))
	span.SetAttributes(attribute.Int64("relay.bytes_in", nin), attribute.Int64("relay.bytes_out", out))
}

// idleCopy копирует src → dst. Чтение ждёт данных не дольше idle с последней
// активности в любую из сторон (act — время в UnixNano): односторонний поток
// (ответ сервера идёт, клиент молчит) не обрывается.
func idleCopy(dst io.Writer, src io.Reader, act *atomic.Int64, idle time.Duration) (int64, error) {
	ds, _ := src.(interface{ SetReadDeadline(time.Time) error })
	if idle <= 0 || ds == nil {
		return io.Copy(dst, src)
	}
	defer ds.SetReadDeadline(time.Time{})
	buf := make([]byte, 32<<10)
	var n int64
	for {
		_ = ds.SetReadDeadline(time.Unix(0, act.Load()).Add(idle))
		k, err := src.Read(buf)
		if k > 0 {
			act.Store(time.Now().UnixNano())
			w, werr := dst.Write(buf[:k])
			n += int64(w)
			if werr != nil {
				return n, werr
			}
		}
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && time.Since(time.Unix(0, act.Load())) < idle {
				continue // другая сторона была активна — ждём дальше
			}
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
	}
}

// proxyHeader — заголовок PROXY protocol v2 (команда PROXY, TCP) с адресами
// соединения c; для не-TCP адресов — команда LOCAL.
func proxyHeader(c net.Conn) []byte {
	h := []byte("\r\n\r\n\x00\r\nQUIT\n")
	src, ok1 := c.RemoteAddr().(*net.TCPAddr)
	dst, ok2 := c.LocalAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return append(h, 0x20, 0x00, 0, 0)
	}
	var body []byte
	fam := byte(0x11) // TCP over IPv4
	if s4, d4 := src.IP.To4(), dst.IP.To4(); s4 != nil && d4 != nil {
		body = append(append(body, s4...), d4...)
	} else {
		fam = 0x21 // TCP over IPv6
		body = append(append(body, src.IP.To16()...), dst.IP.To16()...)
	}
	body = binary.BigEndian.AppendUint16(body, uint16(src.Port))
	body = binary.BigEndian.AppendUint16(body, uint16(dst.Port))
	h = append(h, 0x21, fam)
	h = binary.BigEndian.AppendUint16(h, uint16(len(body)))
	return append(h, body...)
}

// forward пробрасывает соединение на бэкенд маршрута, не расшифровывая его;
// proxy — сначала заголовок PROXY protocol v2.
func (s *Server) forward(c net.Conn, prefix []byte, host, addr string, proxy bool) {
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
	if proxy {
		prefix = append(proxyHeader(c), prefix...)
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

// waitSession ждёт туннель устройства до HoldFor; nil — офлайн или уже ждут
// MaxWaiters соединений.
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
		if s.MaxWaiters > 0 && len(s.waiters[id]) >= s.MaxWaiters {
			s.mu.Unlock()
			return nil
		}
		ch := make(chan struct{})
		s.waiters[id] = append(s.waiters[id], ch)
		s.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(wait):
			s.dropWaiter(id, ch)
		}
	}
}

// dropWaiter убирает канал ожидания, не дождавшийся туннеля.
func (s *Server) dropWaiter(id string, ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.waiters[id]
	for i, c := range l {
		if c == ch {
			l = append(l[:i:i], l[i+1:]...)
			break
		}
	}
	if len(l) == 0 {
		delete(s.waiters, id)
	} else {
		s.waiters[id] = l
	}
}

// waiting — сколько соединений ждут туннель устройства (тесты).
func (s *Server) waiting(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waiters[id])
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

// authDevice — id устройства запроса по его токену; false — ответ 401 уже отправлен.
func (s *Server) authDevice(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.Header.Get("X-Gate4ai-Device")
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.Store.Auth(id, tok) {
		http.Error(w, "unauthorized", 401)
		return "", false
	}
	return id, true
}

func (s *Server) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		if !s.AllowReg {
			http.Error(w, "registration closed", 403)
			return
		}
		// Регистрация открыта, но не бесплатна для атакующего: лимит на адрес
		// (IPv6 — /64) и общий в сутки, общий предел устройств в store.
		s.limits()
		if !s.regIP.Allow(ipKey(remoteAddr(r))) || !s.regAll.Allow("") {
			http.Error(w, "too many registrations, try later", 429)
			return
		}
		id, tok, err := s.Store.Register()
		if errors.Is(err, ErrFull) {
			http.Error(w, "registration closed: device limit reached", 503)
			return
		}
		if err != nil {
			s.internalError(w, "register", err)
			return
		}
		writeJSON(w, map[string]string{"device_id": id, "token": tok, "host": s.DeviceHost(id)})
	})
	mux.HandleFunc("GET /tunnel", func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.authDevice(w, r)
		if !ok {
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
		id, ok := s.authDevice(w, r)
		if !ok {
			return
		}
		s.limits()
		if !s.wits.Allow(id) {
			http.Error(w, "too many checkpoints, try later", 429)
			return
		}
		var cp WitnessedCheckpoint
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&cp); err != nil || !validCheckpoint(cp) {
			http.Error(w, "bad checkpoint", 400)
			return
		}
		cp.Head, cp.Sig = strings.ToLower(cp.Head), strings.ToLower(cp.Sig)
		cp.ReceivedAt = time.Now().UTC()
		switch err := s.Store.AddCheckpoint(id, cp); {
		case errors.Is(err, ErrRewind):
			http.Error(w, err.Error(), 409)
			return
		case err != nil:
			s.internalError(w, "witness", err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	// Чекпоинты видит только само устройство: id публичен (CT-логи), а seq и
	// времена выдают, сколько и когда пользователь действует через шлюз.
	mux.HandleFunc("GET /api/witness/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.authDevice(w, r)
		if !ok {
			return
		}
		if id != r.PathValue("id") {
			http.Error(w, "unauthorized", 401)
			return
		}
		writeJSON(w, s.Store.Checkpoints(id))
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]bool{"ok": true}) })
	return apiDeadlines(traceAPI(s.Metrics.instrument(mux)))
}

// apiDeadlines — дедлайны чтения тела и ответа у запросов API: медленный
// клиент не держит соединение и горутину. /tunnel после Hijack живёт долго,
// там свои keepalive yamux.
func apiDeadlines(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tunnel" {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(15 * time.Second))
			_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		}
		h.ServeHTTP(w, r)
	})
}

// remoteAddr — адрес клиента запроса API (r.RemoteAddr — host:port соединения).
func remoteAddr(r *http.Request) net.Addr {
	if a, err := net.ResolveTCPAddr("tcp", r.RemoteAddr); err == nil {
		return a
	}
	return nil
}

// internalError — 500 с общим текстом; подробности (пути, ошибки диска) — в лог.
func (s *Server) internalError(w http.ResponseWriter, op string, err error) {
	s.Log.Error("relay api", "op", op, "err", err)
	http.Error(w, "internal error", 500)
}

// validCheckpoint — формат чекпоинта по протоколу: seq ≥ 1, head — 32 байта
// hex, sig — подпись Ed25519 (64 байта hex). Саму подпись relay не проверяет:
// открытого ключа журнала устройства у него нет.
func validCheckpoint(cp WitnessedCheckpoint) bool {
	h, err1 := hex.DecodeString(cp.Head)
	sig, err2 := hex.DecodeString(cp.Sig)
	return cp.Seq >= 1 && err1 == nil && len(h) == 32 && err2 == nil && len(sig) == 64
}

func (s *Server) txt(w http.ResponseWriter, r *http.Request, set bool) {
	id, ok := s.authDevice(w, r)
	if !ok {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
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
	switch {
	case errors.Is(err, ErrBadTXT):
		http.Error(w, "bad TXT value: ACME key authorization digest (base64url, up to 255 bytes) expected", 400)
		return
	case errors.Is(err, ErrOrderLimit):
		http.Error(w, "too many certificate orders for this device today, try tomorrow", 429)
		return
	case err != nil:
		s.internalError(w, "acme txt", err)
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
