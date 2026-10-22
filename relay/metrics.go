// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// RecentWindow — окно метрик «за последние 7 дней».
const RecentWindow = 7 * 24 * time.Hour

// Metrics — метрики Prometheus relay (docs/observability.md). Отдаются на
// отдельном адресе (--metrics, только localhost), не на порту 443.
type Metrics struct {
	reg *prometheus.Registry

	conns    *prometheus.CounterVec   // входящие TLS-соединения по виду
	active   *prometheus.GaugeVec     // открытые сейчас
	rejected *prometheus.CounterVec   // сброшенные по причине
	bytes    *prometheus.CounterVec   // байты passthrough по виду и направлению
	tunnels  prometheus.Counter       // подключения туннелей устройств
	wait     prometheus.Histogram     // ожидание туннеля офлайн-устройства
	requests *prometheus.CounterVec   // запросы API
	latency  *prometheus.HistogramVec // длительность запросов API (без /tunnel)
	dns      *prometheus.CounterVec   // запросы DNS по коду ответа
}

func newMetrics(s *Server) *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		conns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gate4ai_relay_connections_total",
			Help: "Incoming TLS connections by kind (api, device, route)."}, []string{"kind"}),
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "gate4ai_relay_active_connections",
			Help: "Open connections by kind (api, device, route)."}, []string{"kind"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gate4ai_relay_rejected_connections_total",
			Help: "Dropped connections by reason (conn_limit, bad_hello, unknown_host, unknown_device, device_offline, stream, backend)."}, []string{"reason"}),
		bytes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gate4ai_relay_passthrough_bytes_total",
			Help: "Bytes passed through without decryption; direction in = from client, out = to client."}, []string{"kind", "direction"}),
		tunnels: prometheus.NewCounter(prometheus.CounterOpts{Name: "gate4ai_relay_tunnel_connects_total",
			Help: "Device tunnel connections accepted."}),
		wait: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "gate4ai_relay_device_wait_seconds",
			Help: "Time an incoming connection waited for the device tunnel.", Buckets: []float64{.001, .01, .1, .5, 1, 5, 15, 30}}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gate4ai_relay_http_requests_total",
			Help: "Relay API requests by route pattern and status code."}, []string{"route", "code"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "gate4ai_relay_http_request_duration_seconds",
			Help: "Relay API request duration (without the long-lived /tunnel).", Buckets: prometheus.DefBuckets}, []string{"route"}),
		dns: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gate4ai_relay_dns_queries_total",
			Help: "Authoritative DNS queries by response code (RATELIMITED — UDP over the per-IP limit, not answered)."}, []string{"rcode"}),
	}
	snap := func() StatsSnapshot { return s.Store.Snapshot(RecentWindow) }
	m.reg.MustRegister(m.conns, m.active, m.rejected, m.bytes, m.tunnels, m.wait, m.requests, m.latency, m.dns,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gate4ai_relay_devices",
			Help: "Registered devices."}, func() float64 { return float64(snap().Devices) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gate4ai_relay_devices_online",
			Help: "Devices with an open tunnel."}, func() float64 { return float64(s.onlineCount()) }),
		// Накопленные счётчики берутся из store и не сбрасываются при перезапуске.
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "gate4ai_relay_device_registrations_total",
			Help: "Device registrations (POST /api/devices), persisted in the state file."}, func() float64 { return float64(snap().Registrations) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "gate4ai_relay_certificate_orders_total",
			Help: "Let's Encrypt certificate orders of devices (ACME DNS-01 TXT set; one order per device per 10 min), persisted."},
			func() float64 { return float64(snap().CertOrders) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gate4ai_relay_device_registrations_7d",
			Help: "Device registrations in the last 7 days (from the state file)."}, func() float64 { return float64(snap().RecentRegs) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gate4ai_relay_certificate_orders_7d",
			Help: "Let's Encrypt certificate orders of devices in the last 7 days (from the state file)."}, func() float64 { return float64(snap().RecentCertOrds) }),
	)
	return m
}

// Register добавляет сборщики (например, build_info из main).
func (m *Metrics) Register(c ...prometheus.Collector) { m.reg.MustRegister(c...) }

// Handler — страница /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// instrument считает запросы API по шаблону маршрута ServeMux.
func (m *Metrics) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		route := r.Pattern // ServeMux заполняет его у того же запроса
		if route == "" {
			route = "unmatched"
		}
		span := trace.SpanFromContext(r.Context())
		span.SetName(route)
		span.SetAttributes(semconv.HTTPRoute(route))
		m.requests.WithLabelValues(route, strconv.Itoa(sw.code)).Inc()
		if route != "GET /tunnel" {
			m.latency.WithLabelValues(route).Observe(time.Since(start).Seconds())
		}
	})
}

// statusWriter запоминает код ответа; Unwrap нужен websocket.Accept (Hijack).
type statusWriter struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.code, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
