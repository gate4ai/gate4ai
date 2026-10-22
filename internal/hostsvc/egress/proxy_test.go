// SPDX-License-Identifier: AGPL-3.0-only

package egress

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxy(t *testing.T) {
	// «Настоящие» серверы: API модели (HTTPS) и прочие.
	var gotKey atomic.Value
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey.Store(r.Header.Get("X-Api-Key"))
		io.WriteString(w, "host="+r.Host+" auth="+r.Header.Get("X-Api-Key"))
	}))
	realCA, _ := NewCA("public CA")
	upstream.TLS = &tls.Config{GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) { return realCA.Leaf(h.ServerName) }}
	upstream.StartTLS()
	defer upstream.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "plain ok") }))
	defer plain.Close()
	upPool := realCA.Pool()

	ca, err := NewCA("gate4ai run test")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var log []Record
	p := &Proxy{
		Auth: func(u, pw string) (string, string, bool) { return "claude-code", "1", u == "run-1" && pw == "pw" },
		Decide: func(_ context.Context, agent, host string, port int) (bool, string) {
			switch host {
			case "api.anthropic.test", "registry.npmjs.test", "127.0.0.1":
				return true, ""
			}
			return false, "not in allowlist"
		},
		Secrets: func(run, host string, port int) []Injection {
			if host == "api.anthropic.test" && port == 443 {
				return []Injection{{Header: "X-Api-Key", Placeholder: "GATE4AI_PLACEHOLDER", Value: "sk-real-secret"}}
			}
			return nil
		},
		CA:          ca,
		UpstreamTLS: &tls.Config{RootCAs: upPool},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			if strings.HasPrefix(addr, "api.anthropic.test:") || strings.HasPrefix(addr, "registry.npmjs.test:") {
				return d.DialContext(ctx, "tcp", upstream.Listener.Addr().String())
			}
			return d.DialContext(ctx, network, addr)
		},
		Log: func(r Record) { mu.Lock(); log = append(log, r); mu.Unlock() },
	}
	ps := httptest.NewServer(p)
	defer ps.Close()
	client := func(user string) *http.Client {
		pu, _ := url.Parse(ps.URL)
		pu.User = url.UserPassword(user, "pw")
		roots := ca.Pool()
		roots.AddCert(realCA.cert)
		return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: roots}}}
	}
	c := client("run-1")

	// Подстановка секрета: агент шлёт заглушку, сервер получает реальный ключ.
	req, _ := http.NewRequest("GET", "https://api.anthropic.test/v1/messages", nil)
	req.Header.Set("X-Api-Key", "GATE4AI_PLACEHOLDER")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	// Сервер получил реальный ключ, а его эхо в ответе агенту замаскировано.
	if gotKey.Load() != "sk-real-secret" || strings.Contains(string(b), "sk-real-secret") || !strings.Contains(string(b), "auth=*****") {
		t.Fatal(gotKey.Load(), string(b))
	}
	// Агент не видит реальный ключ, только ответ сервера; сертификат подписан CA запуска.
	if resp.TLS.PeerCertificates[0].Issuer.CommonName != "gate4ai run test" {
		t.Fatal("not intercepted")
	}
	// Другой разрешённый домен — слепой туннель (без расшифровки), заглушка не заменяется.
	req, _ = http.NewRequest("GET", "https://registry.npmjs.test/", nil)
	req.Header.Set("X-Api-Key", "GATE4AI_PLACEHOLDER")
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "auth=GATE4AI_PLACEHOLDER") || resp.TLS.PeerCertificates[0].Issuer.CommonName == "gate4ai run test" {
		t.Fatal("secret leaked to another domain or tunnel intercepted:", string(b))
	}
	// Запрещённый домен.
	if _, err := c.Get("https://evil.example/"); err == nil {
		t.Fatal("denied domain reachable")
	}
	resp, _ = c.Get("http://evil.example/")
	if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
	// Обычный HTTP к разрешённому хосту.
	resp, err = c.Get(plain.URL)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "plain ok" {
		t.Fatal(string(b))
	}
	// Без учётных данных прокси — 407.
	pu, _ := url.Parse(ps.URL)
	nc := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	if r, _ := nc.Get("http://127.0.0.1/"); r.StatusCode != http.StatusProxyAuthRequired {
		t.Fatal("anonymous proxy use")
	}
	// Журнал соединений (туннель пишется при закрытии).
	c.Transport.(*http.Transport).CloseIdleConnections()
	var denied, mitm, tunnel bool
	for i := 0; i < 100 && !(mitm && tunnel); i++ {
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		for _, r := range log {
			mitm = mitm || (r.Host == "api.anthropic.test" && r.MITM)
			tunnel = tunnel || (r.Host == "registry.npmjs.test" && !r.MITM && r.Down > 0 && r.Up > 0)
		}
		mu.Unlock()
	}
	mu.Lock()
	defer mu.Unlock()
	for _, r := range log {
		denied = denied || (r.Host == "evil.example" && r.Decision == "deny")
		mitm = mitm || (r.Host == "api.anthropic.test" && r.MITM)
		tunnel = tunnel || (r.Host == "registry.npmjs.test" && !r.MITM && r.Down > 0 && r.Up > 0)
		if r.Agent != "claude-code" {
			t.Fatal(r)
		}
	}
	if !denied || !mitm || !tunnel {
		t.Fatalf("log: %+v", log)
	}
}
