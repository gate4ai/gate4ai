// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Регрессионные тесты пределов relay (код-ревью: DNS-усилитель, DoS через
// store, лимит Let's Encrypt, таймауты и лимиты соединений, утечки).

func dnsQuery(t *testing.T, addr, net string, name string, typ uint16, edns uint16) (*dns.Msg, error) {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), typ)
	if edns > 0 {
		m.SetEdns0(edns, false)
	}
	c := &dns.Client{Net: net, Timeout: 500 * time.Millisecond, UDPSize: 65535}
	r, _, err := c.Exchange(m, addr)
	return r, err
}

// TestDNSNoAmplification: TXT ограничены по числу и длине, ответ по UDP не
// больше буфера клиента (TC — повторить по TCP).
func TestDNSNoAmplification(t *testing.T) {
	st, _ := OpenStore("")
	id, _, _ := st.Register()
	name := "_acme-challenge." + id + ".relay.test"
	if err := st.SetDeviceTXT(id, name, strings.Repeat("a", 256)); !errors.Is(err, ErrBadTXT) {
		t.Fatal("TXT longer than 255 bytes accepted:", err)
	}
	if err := st.SetDeviceTXT(id, name, "not base64url!"); !errors.Is(err, ErrBadTXT) {
		t.Fatal("non-ACME TXT accepted:", err)
	}
	for i := 0; i < 5; i++ {
		if err := st.SetDeviceTXT(id, name, strings.Repeat(string(rune('a'+i)), 43)); err != nil {
			t.Fatal(err)
		}
	}
	if v := st.TXT(name); len(v) != DefaultMaxTXT || v[1] != strings.Repeat("e", 43) {
		t.Fatal("TXT values not capped (newest kept):", v)
	}
	// Даже если значений много (MaxTXT больше), ответ UDP усечён.
	st.MaxTXT = 50
	for i := 0; i < 50; i++ {
		_ = st.SetTXT(name, strings.Repeat(string(rune('A'+i%26)), 200+i))
	}
	udp, tcp, err := ListenDNS("127.0.0.1:0", &DNS{Zone: "relay.test", Store: st})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Shutdown()
	defer tcp.Shutdown()
	ua, ta := udp.PacketConn.LocalAddr().String(), tcp.Listener.Addr().String()
	wire := func(r *dns.Msg) int { r.Compress = true; return r.Len() }
	r, err := dnsQuery(t, ua, "udp", name, dns.TypeTXT, 0)
	if err != nil || !r.Truncated || wire(r) > dns.MinMsgSize {
		t.Fatal("UDP answer without EDNS0 not truncated to 512:", err, r.Len(), r.Truncated)
	}
	r, err = dnsQuery(t, ua, "udp", name, dns.TypeTXT, 4096)
	if err != nil || !r.Truncated || wire(r) > maxUDPSize {
		t.Fatal("UDP answer with EDNS0 above 1232:", err, r.Len())
	}
	r, err = dnsQuery(t, ta, "tcp", name, dns.TypeTXT, 0)
	if err != nil || r.Truncated || len(r.Answer) != 50 {
		t.Fatal("TCP answer must be complete:", err)
	}
}

func TestDNSRateLimitAndCAA(t *testing.T) {
	st, _ := OpenStore("")
	udp, tcp, err := ListenDNS("127.0.0.1:0", &DNS{Zone: "relay.test", Store: st, UDPPerSec: 1, UDPBurst: 2,
		CAA: []string{"letsencrypt.org", "sectigo.com"}})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Shutdown()
	defer tcp.Shutdown()
	ua := udp.PacketConn.LocalAddr().String()
	r, err := dnsQuery(t, ua, "udp", "x.relay.test", dns.TypeCAA, 0)
	if err != nil || len(r.Answer) != 2 || r.Answer[1].(*dns.CAA).Value != "sectigo.com" {
		t.Fatal("CAA from settings:", r, err)
	}
	_, _ = dnsQuery(t, ua, "udp", "relay.test", dns.TypeSOA, 0)
	if _, err := dnsQuery(t, ua, "udp", "relay.test", dns.TypeSOA, 0); err == nil {
		t.Fatal("UDP queries above the per-IP limit answered")
	}
	if _, err := dnsQuery(t, tcp.Listener.Addr().String(), "tcp", "relay.test", dns.TypeSOA, 0); err != nil {
		t.Fatal("TCP is not rate limited:", err)
	}
}

func TestRegistrationLimits(t *testing.T) {
	e := start(t, func(s *Server) { s.RegPerIPDay = 2 })
	post := func() int {
		resp, err := e.client.Post("https://relay.test/api/devices", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if a, b := post(), post(); a != 200 || b != 200 {
		t.Fatal("registration:", a, b)
	}
	if c := post(); c != 429 {
		t.Fatal("registrations per IP not limited:", c)
	}
	e = start(t, func(s *Server) { s.RegPerIPDay, s.RegPerDay = 0, 1 })
	if post() != 200 || post() != 429 {
		t.Fatal("total registrations not limited")
	}
	e = start(t, func(s *Server) { s.Store.MaxDevices = 1 })
	if post() != 200 || post() != 503 {
		t.Fatal("device limit")
	}
}

// TestCertOrdersPerDevice: устройство не выберет лимит Let's Encrypt на домен
// relay — заказов (TXT с перерывом больше certOrderGap) не больше
// CertOrdersPerDay в сутки.
func TestCertOrdersPerDevice(t *testing.T) {
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return clock }
	defer func() { now = time.Now }()
	e := start(t)
	id, tok := e.register(t)
	name := "_acme-challenge." + id + ".relay.test"
	for i := 0; i < DefaultCertOrdersPerDay; i++ {
		if c := e.call(t, "POST", "/api/acme/txt", id, tok, map[string]string{"name": name, "value": "v" + string(rune('a'+i))}); c != 200 {
			t.Fatal(c)
		}
		clock = clock.Add(certOrderGap)
	}
	if c := e.call(t, "POST", "/api/acme/txt", id, tok, map[string]string{"name": name, "value": "vz"}); c != 429 {
		t.Fatal("cert orders per device not limited:", c)
	}
	if c := e.call(t, "POST", "/api/acme/txt", id, tok, map[string]string{"name": name, "value": "x/y"}); c != 400 {
		t.Fatal("bad TXT value:", c)
	}
	clock = clock.Add(24 * time.Hour)
	if c := e.call(t, "POST", "/api/acme/txt", id, tok, map[string]string{"name": name, "value": "vz"}); c != 200 {
		t.Fatal("next day:", c)
	}
}

// TestStoreSaveFailureRollsBack: если запись не удалась, память не расходится
// с диском, а клиент не видит внутреннюю ошибку.
func TestStoreSaveFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := st.Register()
	if err != nil {
		t.Fatal(err)
	}
	writeFile = func(string, []byte) error { return errors.New("disk full at /secret/path") }
	defer func() { writeFile = writeFileAtomic }()
	if _, _, err := st.Register(); err == nil {
		t.Fatal("register without saving")
	}
	if n := st.Snapshot(time.Hour); n.Devices != 1 || n.Registrations != 1 {
		t.Fatal("memory diverged from disk:", n)
	}
	if err := st.SetDeviceTXT(id, "_acme-challenge.x", "abc"); err == nil || len(st.TXT("_acme-challenge.x")) != 0 || st.Snapshot(time.Hour).CertOrders != 0 {
		t.Fatal("TXT kept after failed save")
	}
	e := start(t)
	e.srv.Store = st
	resp, err := e.client.Post("https://relay.test/api/devices", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 500 || strings.Contains(string(b), "secret") {
		t.Fatal("internal error leaked:", resp.StatusCode, string(b))
	}
}

func TestStoreFileFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	// null вместо карт не роняет relay.
	if err := os.WriteFile(path, []byte(`{"devices":null,"txt":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Register(); err != nil {
		t.Fatal(err)
	}
	// Чекпоинты — в файлах свидетеля, не в файле состояния.
	_ = os.WriteFile(path, []byte(`{"devices":{"dev1":"`+hashTok("t")+`"}}`), 0o600)
	if st, err = OpenStore(path); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 2; i++ {
		if err := st.AddCheckpoint("dev1", WitnessedCheckpoint{Seq: i, Head: "aa"}); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(path); bytes.Contains(b, []byte("checkpoints")) {
		t.Fatal("checkpoints in the state file")
	}
	// Хранятся последние MaxCheckpoints; переживают перезапуск.
	st.MaxCheckpoints = 3
	for i := int64(3); i <= 6; i++ {
		if err := st.AddCheckpoint("dev1", WitnessedCheckpoint{Seq: i, Head: "cc"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddCheckpoint("dev1", WitnessedCheckpoint{Seq: 6, Head: "dd"}); !errors.Is(err, ErrRewind) {
		t.Fatal("same seq, other head:", err)
	}
	if err := st.AddCheckpoint("nodev", WitnessedCheckpoint{Seq: 1}); err == nil {
		t.Fatal("checkpoint for an unknown device")
	}
	st, _ = OpenStore(path)
	if cps := st.Checkpoints("dev1"); len(cps) != 3 || cps[0].Seq != 4 || cps[2].Seq != 6 {
		t.Fatal("checkpoints not capped:", cps)
	}
	if err := st.AddCheckpoint("dev1", WitnessedCheckpoint{Seq: 5, Head: "cc"}); !errors.Is(err, ErrRewind) {
		t.Fatal("rewind after restart:", err)
	}
	// Временных файлов не остаётся.
	if m, _ := filepath.Glob(filepath.Join(dir, "*.tmp*")); len(m) != 0 {
		t.Fatal(m)
	}
}

// TestAuthNotBlockedByDisk: Auth и Exists (каждое входящее соединение) не ждут
// записи состояния на диск.
func TestAuthNotBlockedByDisk(t *testing.T) {
	st, _ := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	id, tok, _ := st.Register()
	block, entered := make(chan struct{}), make(chan struct{})
	writeFile = func(p string, b []byte) error { close(entered); <-block; return writeFileAtomic(p, b) }
	regDone := make(chan struct{})
	go func() { defer close(regDone); _, _, _ = st.Register() }()
	// Запись дописывается до конца теста: иначе она идёт в уже удаляемый TempDir.
	defer func() { close(block); <-regDone; writeFile = writeFileAtomic }()
	<-entered
	done := make(chan bool)
	go func() { done <- st.Auth(id, tok) && st.Exists(id) && !st.Auth(id, "dev_wrong") }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("auth")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Auth blocked by a disk write")
	}
}

// TestWaitersDropped: соединения к офлайн-устройству не копят каналы ожидания,
// и ждущих одно устройство не больше MaxWaiters.
func TestWaitersDropped(t *testing.T) {
	e := start(t, func(s *Server) { s.HoldFor = 300 * time.Millisecond; s.MaxWaiters = 1 })
	id, _ := e.register(t)
	dial := func() net.Conn {
		c, err := e.dial(context.Background(), "tcp", "")
		if err != nil {
			t.Fatal(err)
		}
		go tls.Client(c, &tls.Config{ServerName: id + ".relay.test", InsecureSkipVerify: true}).Handshake()
		return c
	}
	c1 := dial()
	defer c1.Close()
	for i := 0; i < 100 && e.srv.waiting(id) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if e.srv.waiting(id) != 1 {
		t.Fatal("no waiter")
	}
	c2 := dial()
	defer c2.Close()
	t0 := time.Now()
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.ReadAll(c2)
	if time.Since(t0) > 200*time.Millisecond {
		t.Fatal("waiters above MaxWaiters were held")
	}
	time.Sleep(400 * time.Millisecond)
	if n := e.srv.waiting(id); n != 0 {
		t.Fatal("waiter not removed after timeout:", n)
	}
}

func TestConnLimitPerIP(t *testing.T) {
	e := start(t, func(s *Server) { s.MaxConnsPerIP = 2 })
	var cs []net.Conn
	for i := 0; i < 2; i++ { // молчат: relay ждёт ClientHello
		c, err := e.dial(context.Background(), "tcp", "")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		cs = append(cs, c)
	}
	time.Sleep(50 * time.Millisecond)
	c, _ := e.dial(context.Background(), "tcp", "")
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("connection above the per-IP limit not dropped:", err)
	}
	cs[0].Close()
	time.Sleep(50 * time.Millisecond)
	if resp, err := e.client.Get("https://relay.test/api/health"); err != nil || resp.StatusCode != 200 {
		t.Fatal("slot not released:", err)
	}
}

// TestPipeIdleAndProxy: passthrough без данных закрывается через PipeIdle,
// поток в одну сторону — нет; маршрут ",proxy" получает PROXY protocol v2.
func TestPipeIdleAndProxy(t *testing.T) {
	e := start(t, func(s *Server) { s.PipeIdle = 300 * time.Millisecond })
	bl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bl.Close()
	got := make(chan []byte, 4)
	go func() {
		for {
			c, err := bl.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				hdr := make([]byte, 28)
				_, _ = io.ReadFull(c, hdr)
				got <- hdr
				// Поток сервера: 8 × 100 мс — дольше PipeIdle, клиент молчит.
				for i := 0; i < 8; i++ {
					if _, err := c.Write([]byte("x")); err != nil {
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				_, _ = io.Copy(io.Discard, c) // дальше молчат обе стороны
			}()
		}
	}()
	if err := e.srv.AddRoute("site.test", bl.Addr().String()+",proxy"); err != nil {
		t.Fatal(err)
	}
	c, _ := e.dial(context.Background(), "tcp", "")
	defer c.Close()
	_, _ = c.Write(clientHello(t, "site.test")) // только ClientHello: ответ читает тест
	hdr := <-got
	if !bytes.Equal(hdr[:12], []byte("\r\n\r\n\x00\r\nQUIT\n")) || hdr[12] != 0x21 || hdr[13] != 0x11 {
		t.Fatalf("no PROXY v2 header: %x", hdr)
	}
	if port := binary.BigEndian.Uint16(hdr[24:26]); int(port) != c.LocalAddr().(*net.TCPAddr).Port {
		t.Fatal("PROXY source port", port)
	}
	t0 := time.Now()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := io.Copy(io.Discard, c)
	if el := time.Since(t0); n < 8 || el > 3*time.Second {
		t.Fatalf("one-way stream cut or idle connection kept: %d bytes, %v", n, el)
	}
}

// clientHello — первая запись TLS (ClientHello) с SNI host.
func clientHello(t *testing.T, host string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go tls.Client(a, &tls.Config{ServerName: host, InsecureSkipVerify: true}).Handshake()
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(b, hdr); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(hdr[3:5]))
	if _, err := io.ReadFull(b, body); err != nil {
		t.Fatal(err)
	}
	return append(hdr, body...)
}

// waiting — сколько соединений ждут туннель устройства (тесты).
func (s *Server) waiting(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waiters[id])
}
