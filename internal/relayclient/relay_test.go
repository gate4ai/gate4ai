// SPDX-License-Identifier: AGPL-3.0-only

package relayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"
	"github.com/miekg/dns"

	"github.com/gate4ai/gate4ai/relay"

	"github.com/gate4ai/gate4ai/internal/audit"
)

type env struct {
	srv      *relay.Server
	addr     string
	relayCA  *x509.CertPool
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	relayURL string
}

func startRelay(t *testing.T) *env {
	t.Helper()
	cert, leaf, err := relay.SelfSigned("relay.test")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := relay.OpenStore("")
	s := relay.NewServer("relay.test", &tls.Config{Certificates: []tls.Certificate{cert}}, st)
	s.HoldFor = 300 * time.Millisecond
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go s.Serve(l)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	addr := l.Addr().String()
	return &env{srv: s, addr: addr, relayCA: pool, relayURL: "https://relay.test",
		dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}}
}

func (e *env) device(t *testing.T, id, token string, h http.Handler) (*Device, *x509.CertPool) {
	cert, leaf, _ := SelfSigned(id + ".relay.test")
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &Device{RelayURL: e.relayURL, ID: id, Token: token, RelayTLS: &tls.Config{RootCAs: e.relayCA, ServerName: "relay.test"},
		ServerTLS: &tls.Config{Certificates: []tls.Certificate{cert}}, Handler: h, Dial: e.dial,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}, pool
}

// agentGet — облачный агент обращается к https://<id>.relay.test/.
func (e *env) agentGet(id string, pool *x509.CertPool) (string, *x509.Certificate, error) {
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: id + ".relay.test"},
		DialContext:     e.dial, DisableKeepAlives: true}}
	resp, err := c.Get("https://" + id + ".relay.test/hello")
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.TLS.PeerCertificates[0], nil
}

func waitOnline(t *testing.T, d *Device) {
	for i := 0; i < 200 && !d.Online(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !d.Online() {
		t.Fatal("device did not connect")
	}
}

func TestTunnelPassthroughReconnect(t *testing.T) {
	e := startRelay(t)
	id, tok, host, err := Register(context.Background(), e.relayURL, &tls.Config{RootCAs: e.relayCA, ServerName: "relay.test"}, e.dial)
	if err != nil || host != id+".relay.test" || len(id) != 16 {
		t.Fatal(id, host, err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello from device "+r.Host) })
	dev, devCA := e.device(t, id, tok, h)
	ctx, cancel := context.WithCancel(context.Background())
	go dev.Run(ctx)
	waitOnline(t, dev)

	body, peer, err := e.agentGet(id, devCA)
	if err != nil || !strings.HasPrefix(body, "hello from device") {
		t.Fatal(body, err)
	}
	// Сертификат, который видит агент, — сертификат устройства: relay не терминирует TLS.
	if peer.Subject.CommonName != id+".relay.test" {
		t.Fatal(peer.Subject)
	}
	// Relay обрывает сессию → устройство переподключается само.
	e.srv.Disconnect(id)
	time.Sleep(50 * time.Millisecond)
	waitOnline(t, dev)
	if body, _, err := e.agentGet(id, devCA); err != nil || body == "" {
		t.Fatal("after reconnect:", err)
	}
	// Устройство офлайн: relay держит соединение недолго и сбрасывает (очереди нет).
	cancel()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if _, _, err := e.agentGet(id, devCA); err == nil {
		t.Fatal("offline device answered")
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("relay did not hold the connection")
	}
	// Устройство возвращается в пределах ожидания — запрос проходит.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	e.srv.SetHoldFor(3 * time.Second)
	go func() { time.Sleep(100 * time.Millisecond); dev.Run(ctx2) }()
	if body, _, err := e.agentGet(id, devCA); err != nil || body == "" {
		t.Fatal("held connection not delivered:", err)
	}
	// Неизвестное устройство и чужие токены.
	if _, _, err := e.agentGet("unknown0000000000", devCA); err == nil {
		t.Fatal("unknown device")
	}
	bad, _ := e.device(t, id, "dev_wrong", h)
	if err := bad.once(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatal(err)
	}
}

func TestWitnessAndACMEDNS(t *testing.T) {
	e := startRelay(t)
	id, tok, _, _ := Register(context.Background(), e.relayURL, &tls.Config{RootCAs: e.relayCA, ServerName: "relay.test"}, e.dial)
	dev, _ := e.device(t, id, tok, http.NotFoundHandler())
	head := strings.Repeat("ab", 32)
	if err := dev.Witness(audit.Checkpoint{Seq: 10, Head: head, TS: time.Now(), Sig: "00"}); err != nil {
		t.Fatal(err)
	}
	if err := dev.Witness(audit.Checkpoint{Seq: 5, Head: head, TS: time.Now(), Sig: "00"}); err == nil {
		t.Fatal("rewound log accepted by witness")
	}
	if cps := e.srv.Store.Checkpoints(id); len(cps) != 1 || cps[0].Head != head {
		t.Fatal(cps)
	}
	other, _ := e.device(t, id, "dev_x", nil)
	if err := other.Witness(audit.Checkpoint{Seq: 11, Head: head}); err == nil {
		t.Fatal("unauthenticated witness")
	}

	// DNS-01 через API relay + авторитетный DNS.
	p := DNSProvider{D: dev}
	ctx := context.Background()
	if _, err := p.AppendRecords(ctx, "relay.test.", []libdns.Record{libdns.TXT{Name: "_acme-challenge." + id, Text: "token-123"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AppendRecords(ctx, "relay.test.", []libdns.Record{libdns.TXT{Name: "_acme-challenge.victim", Text: "x"}}); err == nil {
		t.Fatal("device set a TXT record for another name")
	}
	udp, tcp, err := relay.ListenDNS("127.0.0.1:0", &relay.DNS{Zone: "relay.test", IPv4: net.ParseIP("203.0.113.7"), Store: e.srv.Store})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Shutdown()
	defer tcp.Shutdown()
	q := func(name string, typ uint16) *dns.Msg {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), typ)
		r, _, err := new(dns.Client).Exchange(m, udp.PacketConn.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := q("_acme-challenge."+id+".relay.test", dns.TypeTXT)
	if len(r.Answer) != 1 || r.Answer[0].(*dns.TXT).Txt[0] != "token-123" || !r.Authoritative {
		t.Fatal(r)
	}
	if r := q(id+".relay.test", dns.TypeA); len(r.Answer) != 1 || r.Answer[0].(*dns.A).A.String() != "203.0.113.7" {
		t.Fatal(r)
	}
	if r := q("example.com", dns.TypeA); r.Rcode != dns.RcodeRefused {
		t.Fatal("open resolver")
	}
	p.DeleteRecords(ctx, "relay.test.", []libdns.Record{libdns.TXT{Name: "_acme-challenge." + id, Text: "token-123"}})
	if r := q("_acme-challenge."+id+".relay.test", dns.TypeTXT); len(r.Answer) != 0 {
		t.Fatal("TXT not deleted")
	}
}
