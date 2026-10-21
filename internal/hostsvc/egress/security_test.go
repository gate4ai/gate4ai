// SPDX-License-Identifier: AGPL-3.0-only

package egress

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testPH  = "GATE4AI_PLACEHOLDER_0123"
	testKey = "sk-real-secret-value"
)

// mitmFixture — прокси с подстановкой для api.example.test и «настоящий»
// HTTPS-сервер h за ним; Dial ведёт все имена на этот сервер.
func mitmFixture(t *testing.T, h http.HandlerFunc) (client *http.Client, proxyURL string, ca *CA, srvAddr string) {
	t.Helper()
	upstream := httptest.NewUnstartedServer(h)
	realCA, _ := NewCA("public CA")
	upstream.TLS = &tls.Config{GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) { return realCA.Leaf(h.ServerName) }}
	upstream.StartTLS()
	t.Cleanup(upstream.Close)
	ca, _ = NewCA("run CA")
	p := &Proxy{
		Auth:   func(u, pw string) (string, string, bool) { return "agent", "1", u == "run-1" && pw == "pw" },
		Decide: func(context.Context, string, string, int) (bool, string) { return true, "" },
		Secrets: func(run, host string, port int) []Injection {
			if host == "api.example.test" && port == 443 {
				return []Injection{{Header: "Authorization", Placeholder: testPH, Value: testKey}}
			}
			return nil
		},
		CA:          ca,
		UpstreamTLS: &tls.Config{RootCAs: realCA.Pool()},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", upstream.Listener.Addr().String())
		},
	}
	ps := httptest.NewServer(p)
	t.Cleanup(ps.Close)
	t.Cleanup(p.Close)
	pu, _ := url.Parse(ps.URL)
	pu.User = url.UserPassword("run-1", "pw")
	roots := ca.Pool()
	tr := &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: roots}, DisableCompression: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}, pu.String(), ca, upstream.Listener.Addr().String()
}

// H1: Host внутреннего запроса задаёт агент — ключ не уходит на чужой хост
// за тем же фронтом (domain fronting): 421, сервер запрос не получает.
func TestMITMHostMismatch(t *testing.T) {
	var hits atomic.Int32
	c, _, _, _ := mitmFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "host="+r.Host+" auth="+r.Header.Get("Authorization"))
	})
	req, _ := http.NewRequest("GET", "https://api.example.test/v1", nil)
	req.Host = "attacker.example"
	req.Header.Set("Authorization", "Bearer "+testPH)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMisdirectedRequest || hits.Load() != 0 || strings.Contains(string(b), testKey) {
		t.Fatal(resp.StatusCode, hits.Load(), string(b))
	}
	// Тот же хост — подстановка работает, наверх уходит правильный Host.
	req, _ = http.NewRequest("GET", "https://api.example.test/v1", nil)
	req.Header.Set("Authorization", "Bearer "+testPH)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "host=api.example.test") || strings.Contains(string(b), testKey) {
		t.Fatal(resp.StatusCode, string(b))
	}
}

// H2: в обычный HTTP секрет не подставляется никогда; заглушка там — отказ.
func TestPlainHTTPNoInjection(t *testing.T) {
	var got atomic.Value
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization"))
	}))
	defer plain.Close()
	p := &Proxy{
		Auth:   func(u, pw string) (string, string, bool) { return "agent", "1", true },
		Decide: func(context.Context, string, string, int) (bool, string) { return true, "" },
		Secrets: func(run, host string, port int) []Injection {
			return []Injection{{Header: "Authorization", Placeholder: testPH, Value: testKey}}
		},
		CA: mustCA(t),
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", plain.Listener.Addr().String())
		},
	}
	ps := httptest.NewServer(p)
	defer ps.Close()
	pu, _ := url.Parse(ps.URL)
	pu.User = url.UserPassword("u", "p")
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	req, _ := http.NewRequest("GET", "http://api.example.test/", nil)
	req.Header.Set("Authorization", "Bearer "+testPH)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 403 || !strings.Contains(string(b), "secret_denied") || got.Load() != nil {
		t.Fatal(resp.StatusCode, string(b), got.Load())
	}
}

func mustCA(t *testing.T) *CA {
	ca, err := NewCA("t")
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// M3: заглушка не в заголовке привязки — отказ; эхо ключа в ответе
// (в том числе в производной форме и в заголовках) замаскировано.
func TestMITMPlaceholderElsewhereAndEcho(t *testing.T) {
	c, _, _, _ := mitmFixture(t, func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		w.Header().Set("X-Echo", a)
		io.WriteString(w, "auth="+a+" url="+url.QueryEscape(a))
	})
	for _, mod := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("X-Other", testPH) },
		func(r *http.Request) { r.URL.RawQuery = "k=" + testPH },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer x"+testPH) },
	} {
		req, _ := http.NewRequest("GET", "https://api.example.test/", nil)
		req.Header.Set("Authorization", "Bearer "+testPH)
		mod(req)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 403 || !strings.Contains(string(b), "secret_denied") {
			t.Fatal(resp.StatusCode, string(b))
		}
	}
	req, _ := http.NewRequest("GET", "https://api.example.test/", nil)
	req.Header.Set("Authorization", "Bearer "+testPH)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), "sk-real") || strings.Contains(resp.Header.Get("X-Echo"), "sk-real") || !strings.Contains(string(b), "auth=Bearer ****") {
		t.Fatal(string(b), resp.Header)
	}
}

// Сжатый ответ распаковывается до маскирования; потоковый ответ (SSE)
// доходит до агента до конца ответа сервера.
func TestMITMCompressedAndStreaming(t *testing.T) {
	release := make(chan struct{})
	var gotAE atomic.Value
	c, _, _, _ := mitmFixture(t, func(w http.ResponseWriter, r *http.Request) {
		gotAE.Store(r.Header.Get("Accept-Encoding"))
		a := r.Header.Get("Authorization")
		if r.URL.Path == "/gzip" {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			io.WriteString(gz, "echo "+a)
			gz.Close()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: "+a+"\n\n")
	})
	req, _ := http.NewRequest("GET", "https://api.example.test/gzip", nil)
	req.Header.Set("Authorization", "Bearer "+testPH)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if gotAE.Load() != "identity" || resp.Header.Get("Content-Encoding") != "" || string(b) != "echo Bearer "+strings.Repeat("*", len(testKey)) {
		t.Fatalf("%v %q %v", gotAE.Load(), b, resp.Header)
	}
	req, _ = http.NewRequest("GET", "https://api.example.test/sse", nil)
	req.Header.Set("Authorization", "Bearer "+testPH)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatal(line, err)
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(rest), "sk-real") {
		t.Fatal(string(rest))
	}
}

// B.M4: имя, которое резолвится в адрес этой машины или частной сети, не
// открывает туннель к локальным службам, если такой адрес не разрешён явно.
func TestLocalAddressBlocked(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "local service") }))
	defer local.Close()
	_, port, _ := net.SplitHostPort(local.Listener.Addr().String())
	allowLocal := false
	p := &Proxy{
		Auth:       func(u, pw string) (string, string, bool) { return "agent", "1", true },
		Decide:     func(context.Context, string, string, int) (bool, string) { return true, "" },
		AllowLocal: func(agent, host string) bool { return allowLocal },
	}
	ps := httptest.NewServer(p)
	defer ps.Close()
	defer p.Close()
	pu, _ := url.Parse(ps.URL)
	pu.User = url.UserPassword("u", "p")
	tr := &http.Transport{Proxy: http.ProxyURL(pu)}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	for _, u := range []string{"http://localhost:" + port + "/", "http://127.0.0.1:" + port + "/"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 200 || strings.Contains(string(b), "local service") {
			t.Fatal(u, resp.StatusCode, string(b))
		}
	}
	// CONNECT на loopback тоже закрыт.
	conn, _ := net.Dial("tcp", ps.Listener.Addr().String())
	defer conn.Close()
	io.WriteString(conn, "CONNECT localhost:"+port+" HTTP/1.1\r\nHost: localhost:"+port+"\r\nProxy-Authorization: Basic dTpw\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 403 {
		t.Fatal(resp, err)
	}
	// Явно разрешённый адрес — работает.
	allowLocal = true
	resp, err = c.Get("http://127.0.0.1:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "local service" {
		t.Fatal(string(b))
	}
}

// L4: leaf, у которого истекает срок, перевыпускается.
func TestLeafRenewal(t *testing.T) {
	ca := mustCA(t)
	l1, err := ca.Leaf("a.test")
	if err != nil {
		t.Fatal(err)
	}
	if l2, _ := ca.Leaf("a.test"); l2 != l1 {
		t.Fatal("fresh leaf must be cached")
	}
	old := *l1
	old.Leaf = &x509.Certificate{NotAfter: time.Now().Add(time.Hour)}
	ca.leaf["a.test"] = &old
	if l3, _ := ca.Leaf("a.test"); l3 == &old || time.Until(l3.Leaf.NotAfter) < 6*24*time.Hour {
		t.Fatal("expiring leaf not renewed")
	}
	if time.Until(ca.cert.NotAfter) < 365*24*time.Hour {
		t.Fatal("CA lifetime")
	}
}
