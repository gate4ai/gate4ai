// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/miekg/dns"
	"go.uber.org/zap"

	"github.com/gate4ai/gate4ai/relay"
)

// relayProc — gate4ai-relay из ../relay на 127.0.0.1 с доменом localhost.
type relayProc struct {
	url  string // https://localhost:<port>
	addr string // 127.0.0.1:<port> — TLS (API, туннели, трафик устройств)
	dns  string // 127.0.0.1:<port> — авторитетный DNS зоны localhost
	ca   string // PEM сертификата relay
	pool *x509.CertPool
}

// freePort — свободный сейчас адрес 127.0.0.1:<порт>. Между закрытием и
// запуском relay порт может занять параллельный тест: startRelay тогда
// запускает relay заново на других портах.
func freePort(t *testing.T, network string) string {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().String()
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// relayAttempts — сколько раз запускать relay, если порт успели занять.
const relayAttempts = 5

func startRelay(t *testing.T) *relayProc {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e builds binaries")
	}
	bin := binary(t, "relay", "./cmd/gate4ai-relay")
	cert, leaf, err := relay.SelfSigned("localhost")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	for attempt := 1; ; attempt++ {
		tmp := t.TempDir()
		r := &relayProc{addr: freePort(t, "tcp"), dns: freePort(t, "udp"), ca: filepath.Join(tmp, "relay.pem"), pool: x509.NewCertPool()}
		r.url = "https://localhost:" + r.addr[strings.LastIndex(r.addr, ":")+1:]
		r.pool.AddCert(leaf)
		os.WriteFile(r.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0o600)
		os.WriteFile(filepath.Join(tmp, "relay.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600)
		if r.start(t, bin, tmp, attempt == relayAttempts) {
			return r
		}
		t.Logf("relay did not start on %s / %s (attempt %d): retrying on other ports", r.addr, r.dns, attempt)
	}
}

// start запускает relay; false — процесс сразу завершился (порт занят), last —
// последняя попытка: тогда ошибка теста.
func (r *relayProc) start(t *testing.T, bin, tmp string, last bool) bool {
	t.Helper()
	rlog := &lockedBuf{}
	cmd := exec.Command(bin, "--domain", "localhost", "--listen", r.addr, "--dns", r.dns, "--ipv4", "127.0.0.1",
		"--cert", r.ca, "--key", filepath.Join(tmp, "relay.key"), "--state", filepath.Join(tmp, "state.json"))
	cmd.Stdout, cmd.Stderr = rlog, rlog
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	c := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: r.pool}}}
	ready := func() bool {
		resp, err := c.Get(r.url + "/api/health")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	if !last {
		// Ранний выход (address already in use) — повтор на других портах.
		deadline := time.Now().Add(StartTimeout)
		for !ready() {
			select {
			case <-exited:
				return false
			case <-time.After(50 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				<-exited
				t.Fatalf("relay did not start in %s:\n%s", StartTimeout, rlog.String())
			}
		}
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		<-exited
		if t.Failed() {
			t.Log("relay log:\n" + rlog.String())
		}
	})
	if last {
		waitReady(t, "relay", exited, ready)
	}
	return true
}

// browser — «телефон»: ходит на https://<id>.localhost/… через relay (как браузер по
// DNS на адрес relay) и проверяет, что TLS завершает устройство, а не relay.
func (r *relayProc) browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", r.addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, // сертификат устройства самоподписанный (--self-signed)
			VerifyConnection: func(cs tls.ConnectionState) error {
				if cn := cs.PeerCertificates[0].Subject.CommonName; cn != cs.ServerName || cn == "localhost" {
					t.Errorf("TLS terminated by %q, not by the device %q", cn, cs.ServerName)
				}
				return nil
			}},
		DisableKeepAlives: true}}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// DNS relay годится для ACME DNS-01: certmagic находит зону relay (а не само имя
// _acme-challenge.<id>) и её NS. Иначе daemon не получает сертификат устройства,
// не подключается к relay, и ссылки на <id>.relay.gate4.ai висят до таймаута.
func TestRelayDNSForACME(t *testing.T) {
	t.Parallel()
	r := startRelay(t)
	fqdn := "_acme-challenge.abcdef0123456789.localhost."
	zone, err := certmagic.FindZoneByFQDN(context.Background(), zap.NewNop(), fqdn, []string{r.dns})
	if err != nil || zone != "localhost." {
		t.Fatalf("zone of %s: %q, %v", fqdn, zone, err)
	}
	m := new(dns.Msg)
	m.SetQuestion(zone, dns.TypeNS)
	in, err := dns.Exchange(m, r.dns)
	if err != nil || len(in.Answer) == 0 {
		t.Fatalf("no NS for %s: %v %v", zone, in, err)
	}
}

var remoteLinkRe = regexp.MustCompile(`<pre class="box" id="rlink">(https://[^<]+)</pre>`)

// Ссылка «Вход с телефона» (Настройки → Вход с телефона): daemon подключён к relay,
// ссылка открывает Web UI устройства через relay и второй раз не открывается.
func TestRemoteLinkThroughRelay(t *testing.T) {
	t.Parallel()
	r := startRelay(t)
	g := startGate(t, "--ui", "127.0.0.1:0")
	if res := g.run("", "", "relay", "register", "--url", r.url, "--ca-file", r.ca, "--self-signed"); res.code != 0 {
		t.Fatal(res.out)
	}
	for start := time.Now(); ; {
		st := g.run("", "", "relay", "status").json(t)
		if st["online"] == true {
			break
		}
		if time.Since(start) > StartTimeout {
			t.Fatalf("device did not connect to the relay: %v", st)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Владелец входит в локальный Web UI и берёт ссылку, как в браузере.
	res := g.run("", "", "ui", "--print")
	if res.code != 0 {
		t.Fatal(res.out)
	}
	jar, _ := cookiejar.New(nil)
	owner := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	if code, _ := get(t, owner, strings.TrimSpace(res.out)); code != 200 {
		t.Fatal("local login:", code)
	}
	ui := strings.TrimSuffix(strings.TrimSpace(res.out), "/login?t="+strings.SplitN(strings.TrimSpace(res.out), "t=", 2)[1])
	link := func() string {
		t.Helper()
		_, p := get(t, owner, ui+"/settings/internet?link=1")
		m := remoteLinkRe.FindStringSubmatch(p)
		if m == nil {
			t.Fatal("no remote link on the settings page:", p)
		}
		return m[1]
	}

	l := link()
	if !regexp.MustCompile(`^https://[0-9a-z]{16}\.localhost/ui/login\?t=[0-9a-f]+$`).MatchString(l) {
		t.Fatal("link:", l)
	}
	phone := r.browser(t)
	if code, p := get(t, phone, l); code != 200 || !strings.Contains(p, "Remote access") {
		t.Fatalf("link did not open the device UI: %d %s", code, p)
	}
	if code, _ := get(t, r.browser(t), l); code != 401 {
		t.Fatal("link opened twice:", code)
	}
	// Срок ссылки (2 минуты) проверяет TestLoginLinkExpires в gate4ai/internal/front/webui
	// со сдвигом часов: здесь его ожидание добавляло бы 2 минуты к каждому CI.
}
