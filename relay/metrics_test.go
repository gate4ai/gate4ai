// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.Metrics.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	b, _ := io.ReadAll(w.Body)
	return string(b)
}

func wantMetric(t *testing.T, page, line string) {
	t.Helper()
	for _, l := range strings.Split(page, "\n") {
		if l == line {
			return
		}
	}
	t.Errorf("no %q in /metrics", line)
}

func TestMetricsRegistrationsAndCertOrders(t *testing.T) {
	e := start(t)
	id, tok := e.register(t)
	e.register(t)
	name := "_acme-challenge." + id + ".relay.test"
	// Две авторизации одного заказа — один заказ сертификата.
	for _, v := range []string{"a", "b"} {
		if code := e.call(t, "POST", "/api/acme/txt", id, tok, map[string]string{"name": name, "value": v}); code != 200 {
			t.Fatal(code)
		}
	}
	e.call(t, "POST", "/api/acme/txt", id, tok, map[string]string{"name": "_acme-challenge.victim.relay.test", "value": "x"})
	page := scrape(t, e.srv)
	wantMetric(t, page, "gate4ai_relay_devices 2")
	wantMetric(t, page, "gate4ai_relay_device_registrations_total 2")
	wantMetric(t, page, "gate4ai_relay_device_registrations_7d 2")
	wantMetric(t, page, "gate4ai_relay_certificate_orders_total 1")
	wantMetric(t, page, "gate4ai_relay_certificate_orders_7d 1")
	wantMetric(t, page, `gate4ai_relay_http_requests_total{code="200",route="POST /api/devices"} 2`)
	wantMetric(t, page, `gate4ai_relay_http_requests_total{code="403",route="POST /api/acme/txt"} 1`)
	if !strings.Contains(page, `gate4ai_relay_connections_total{kind="api"} `) {
		t.Error("no api connections")
	}
}

func TestMetricsPassthrough(t *testing.T) {
	e := start(t)
	id, tok := e.register(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := e.device(t, ctx, id, tok)
	if _, _, err := e.agentGet(id, pool); err != nil {
		t.Fatal(err)
	}
	_, _, _ = e.agentGet("unknown000000000", pool)
	time.Sleep(50 * time.Millisecond) // pipe дописывает счётчики после закрытия
	page := scrape(t, e.srv)
	wantMetric(t, page, "gate4ai_relay_devices_online 1")
	wantMetric(t, page, `gate4ai_relay_connections_total{kind="device"} 1`)
	wantMetric(t, page, `gate4ai_relay_rejected_connections_total{reason="unknown_device"} 1`)
	wantMetric(t, page, "gate4ai_relay_tunnel_connects_total 1")
	if !strings.Contains(page, `gate4ai_relay_passthrough_bytes_total{direction="out",kind="device"} `) {
		t.Error("no passthrough bytes")
	}
}

// Окно 7 дней и сохранение счётчиков между перезапусками relay.
func TestStatsWindowPersisted(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := t0
	now = func() time.Time { return clock }
	defer func() { now = time.Now }()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _, _ := st.Register() // 8 дней назад — вне окна
	_ = st.SetDeviceTXT(id, "_acme-challenge.x", "1")
	clock = t0.Add(8 * 24 * time.Hour)
	_, _, _ = st.Register()
	_ = st.SetDeviceTXT(id, "_acme-challenge.x", "2")
	clock = clock.Add(5 * time.Minute) // тот же заказ
	_ = st.SetDeviceTXT(id, "_acme-challenge.x", "3")
	clock = clock.Add(11 * time.Minute) // продление — новый заказ
	_ = st.SetDeviceTXT(id, "_acme-challenge.x", "4")

	st, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got := st.Snapshot(RecentWindow)
	want := StatsSnapshot{Devices: 2, Registrations: 2, CertOrders: 3, RecentRegs: 1, RecentCertOrds: 2}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	clock = clock.Add(30 * 24 * time.Hour)
	_, _, _ = st.Register()
	if n := len(st.data.Stats.RegisteredAt); n != 1 {
		t.Fatal("old registration times not pruned:", n)
	}
}
