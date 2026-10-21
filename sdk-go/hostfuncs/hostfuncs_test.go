// SPDX-License-Identifier: Apache-2.0

package hostfuncs

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/capability"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

type secrets map[string]string

func (s secrets) Value(a string) (string, error) {
	if v, ok := s[a]; ok {
		return v, nil
	}
	return "", errors.New("missing")
}

func dispatcher(t *testing.T, yaml string, settings map[string]any) *Dispatcher {
	t.Helper()
	m, err := manifest.Parse([]byte("connector: x\n" + yaml))
	if err != nil {
		t.Fatal(err)
	}
	c, err := capability.New(m, settings, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{Check: c, Secrets: secrets{"@api": "tok-SECRET-123", "@mail": "pässword \"q\""}, Timeout: 5 * time.Second}
	t.Cleanup(d.Close)
	return d
}

func call(t *testing.T, d *Dispatcher, fn string, req, out any) error {
	t.Helper()
	b, _ := json.Marshal(req)
	return abi.Decode(d.Call(fn, b), out)
}

func code(err error) string {
	var e *abi.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestHTTPSecretsBindAndMask(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo", gotAuth)
		w.Write([]byte("echo: " + gotAuth + " " + string(b)))
	}))
	defer srv.Close()
	d := dispatcher(t, `capabilities:
  network: [ { host: 127.0.0.1, scheme: http } ]
  secrets: [ { alias: "@api", bind: [127.0.0.1] } ]
`, nil)
	var resp abi.HTTPResponse
	err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{Method: "POST", URL: srv.URL + "/x",
		Headers: map[string]string{"Authorization": "Bearer {{secret:@api}}"}, Body: []byte("k={{secret:@api}}")}, &resp)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-SECRET-123" {
		t.Fatal("secret not substituted:", gotAuth)
	}
	if strings.Contains(string(resp.Body), "tok-SECRET") || strings.Contains(resp.Headers["X-Echo"], "tok-SECRET") {
		t.Fatal("secret reflected back to the connector:", string(resp.Body))
	}
	// Редирект не выполняется.
	if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL + "/redirect"}, &resp); err != nil || resp.Status != 302 {
		t.Fatal(resp.Status, err)
	}
	// Секрет в URL, неизвестный алиас, чужой адрес.
	if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL + "/?k={{secret:@api}}"}, &resp); code(err) != abi.CodeBadRequest {
		t.Fatal("secret in URL:", err)
	}
	if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL, Headers: map[string]string{"X": "{{secret:@mail}}"}}, &resp); code(err) != abi.CodeCapabilityDenied {
		t.Fatal("undeclared secret:", err)
	}
	d2 := dispatcher(t, `capabilities:
  network: [ { host: 127.0.0.1, scheme: http }, { host: localhost, scheme: http } ]
  secrets: [ { alias: "@api", bind: [localhost] } ]
`, nil)
	if err := call(t, d2, abi.FnHTTPRequest, abi.HTTPRequest{URL: srv.URL, Headers: map[string]string{"X": "{{secret:@api}}"}}, &resp); code(err) != abi.CodeCapabilityDenied {
		t.Fatal("secret sent to an unbound address:", err)
	}
	if err := call(t, d, abi.FnHTTPRequest, abi.HTTPRequest{URL: "https://example.com/"}, &resp); code(err) != abi.CodeCapabilityDenied {
		t.Fatal("undeclared host:", err)
	}
}

func TestFSAndKV(t *testing.T) {
	root := t.TempDir()
	d := dispatcher(t, `capabilities: { files: ["${setting:root}/**"], storage: { quota_mb: 1 } }`, map[string]any{"root": root})
	p := filepath.Join(root, "a.md")
	var w abi.FSWriteResponse
	if err := call(t, d, abi.FnFSAppend, abi.FSRequest{Path: p, Data: []byte("one\n")}, &w); err != nil || w.SizeBefore != -1 || w.SizeAfter != 4 {
		t.Fatal(w, err)
	}
	if err := call(t, d, abi.FnFSWrite, abi.FSRequest{Path: p, Data: []byte("two")}, &w); err != nil || w.SizeAfter != 3 {
		t.Fatal(w, err)
	}
	var r abi.FSReadResponse
	if err := call(t, d, abi.FnFSRead, abi.FSRequest{Path: p, Max: 2}, &r); err != nil || string(r.Data) != "tw" || !r.Truncated {
		t.Fatal(r, err)
	}
	var l abi.FSListResponse
	if err := call(t, d, abi.FnFSList, abi.FSRequest{Path: root}, &l); err != nil || len(l.Entries) != 1 || l.Entries[0].Name != "a.md" {
		t.Fatal(l, err)
	}
	var st abi.FSStatResponse
	if err := call(t, d, abi.FnFSStat, abi.FSRequest{Path: root + "/none"}, &st); err != nil || st.Exists {
		t.Fatal(st, err)
	}
	if err := call(t, d, abi.FnFSRead, abi.FSRequest{Path: root + "/../x"}, &r); code(err) != abi.CodeCapabilityDenied {
		t.Fatal("escape:", err)
	}
	if err := call(t, d, abi.FnKVSet, abi.KVRequest{Key: "k", Value: []byte("v")}, nil); err == nil {
		t.Fatal("kv without store")
	}
	d.KV = &memKV{m: map[string][]byte{}}
	if err := call(t, d, abi.FnKVSet, abi.KVRequest{Key: "k", Value: []byte("v")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(t, d, abi.FnKVSet, abi.KVRequest{Key: "big", Value: make([]byte, 2<<20)}, nil); code(err) != abi.CodeLimit {
		t.Fatal("quota:", err)
	}
	var kv abi.KVResponse
	if err := call(t, d, abi.FnKVGet, abi.KVRequest{Key: "k"}, &kv); err != nil || string(kv.Value) != "v" {
		t.Fatal(kv, err)
	}
	// Вызовы других коннекторов и сеть не объявлены.
	if err := call(t, d, abi.FnServiceCall, abi.ServiceCallRequest{Target: "browser", Action: "open"}, nil); code(err) != abi.CodeCapabilityDenied {
		t.Fatal(err)
	}
	if err := call(t, d, abi.FnTLSDial, abi.DialRequest{Host: "127.0.0.1", Port: 1, TLS: abi.TLSNone}, nil); code(err) != abi.CodeCapabilityDenied {
		t.Fatal(err)
	}
	if err := call(t, d, "rm_rf", struct{}{}, nil); code(err) != abi.CodeNotSupported {
		t.Fatal(err)
	}
}

type memKV struct{ m map[string][]byte }

func (k *memKV) Get(key string) ([]byte, bool, error) { v, ok := k.m[key]; return v, ok, nil }
func (k *memKV) Set(key string, v []byte, q int64) error {
	if int64(len(v)) > q {
		return abi.Errorf(abi.CodeLimit, "quota")
	}
	k.m[key] = v
	return nil
}
func (k *memKV) Delete(key string) error { delete(k.m, key); return nil }

func selfSigned(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// fakeIMAP — минимальный IMAP: приветствие, STARTTLS, LOGIN (quoted и литерал), NOOP.
func fakeIMAP(t *testing.T, cert tls.Certificate, wantUser, wantPass string) string {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var conn net.Conn = c
				r := bufio.NewReader(conn)
				io.WriteString(conn, "* OK fake ready\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					tag, rest, _ := strings.Cut(line, " ")
					switch {
					case rest == "STARTTLS":
						io.WriteString(conn, tag+" OK begin\r\n")
						tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}})
						conn, r = tc, bufio.NewReader(tc)
					case strings.HasPrefix(rest, "LOGIN "):
						args := rest[6:]
						// литерал пароля: {n} → "+", затем n байт и CRLF
						if i := strings.Index(args, "{"); i >= 0 && strings.HasSuffix(args, "}") {
							io.WriteString(conn, "+ go\r\n")
							more, _ := r.ReadString('\n')
							args = args[:i] + `"` + strings.TrimRight(more, "\r\n") + `"`
						}
						if args == `"`+wantUser+`" "`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(wantPass)+`"` ||
							args == `"`+wantUser+`" "`+wantPass+`"` {
							io.WriteString(conn, tag+" OK [CAPABILITY IMAP4rev1] logged in\r\n")
						} else {
							io.WriteString(conn, tag+" NO bad credentials\r\n")
						}
					default:
						io.WriteString(conn, tag+" OK "+rest+" done\r\n")
					}
				}
			}(c)
		}
	}()
	return l.Addr().String()
}

func readAll(t *testing.T, d *Dispatcher, id int, until string) string {
	t.Helper()
	var got string
	for i := 0; i < 100 && !strings.Contains(got, until); i++ {
		var r abi.ConnReadResponse
		if err := call(t, d, abi.FnConnRead, abi.ConnRead{Conn: id, WaitMS: 50}, &r); err != nil {
			t.Fatal(err)
		}
		got += string(r.Data)
	}
	return got
}

func TestDialIMAPLoginByHost(t *testing.T) {
	cert, pool := selfSigned(t, "localhost")
	addr := fakeIMAP(t, cert, "me@example.com", "pässword \"q\"")
	_, port, _ := net.SplitHostPort(addr)
	d := dispatcher(t, `capabilities:
  network: [ { host: localhost, port: "`+port+`", scheme: tls } ]
  secrets: [ { alias: "@mail", bind: [localhost] } ]
`, nil)
	d.RootCAs = pool
	var dr abi.DialResponse
	p, _ := strconv.Atoi(port)
	req := abi.DialRequest{Host: "localhost", Port: p, TLS: abi.TLSStartTLS, Protocol: abi.ProtoIMAP,
		Auth: &abi.DialAuth{Scheme: abi.AuthIMAPLogin, Username: "me@example.com", Secret: "@mail"}}
	if err := call(t, d, abi.FnTLSDial, req, &dr); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, d, dr.Conn, "\r\n")
	if !strings.HasPrefix(got, "* PREAUTH") {
		t.Fatal("module did not get PREAUTH greeting:", got)
	}
	if err := call(t, d, abi.FnConnWrite, abi.ConnWrite{Conn: dr.Conn, Data: []byte("a1 NOOP\r\n")}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, d, dr.Conn, "a1 OK"); !strings.Contains(got, "a1 OK NOOP done") {
		t.Fatal(got)
	}
	if err := call(t, d, abi.FnConnClose, abi.ConnClose{Conn: dr.Conn}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(t, d, abi.FnConnRead, abi.ConnRead{Conn: dr.Conn}, nil); code(err) != abi.CodeNotFound {
		t.Fatal("closed conn:", err)
	}
	// Неверный пароль — auth_failed, а значение в ошибку не попадает.
	d.Secrets = secrets{"@mail": "wrong-password-123"}
	err := call(t, d, abi.FnTLSDial, req, &dr)
	if code(err) != abi.CodeAuth || strings.Contains(err.Error(), "wrong-password") {
		t.Fatal(err)
	}
	// Секрет не привязан к этому хосту.
	req.Host = "127.0.0.1"
	if err := call(t, d, abi.FnTLSDial, req, &dr); code(err) != abi.CodeCapabilityDenied {
		t.Fatal(err)
	}
}

func TestDialSMTPAuthPlain(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	got := make(chan string, 8)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		io.WriteString(c, "220 fake ESMTP\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			got <- line
			switch {
			case strings.HasPrefix(line, "EHLO"):
				io.WriteString(c, "250-fake\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN "):
				b, _ := base64.StdEncoding.DecodeString(line[11:])
				if string(b) == "\x00me\x00tok-SECRET-123" {
					io.WriteString(c, "235 ok\r\n")
				} else {
					io.WriteString(c, "535 no\r\n")
				}
			default:
				io.WriteString(c, "250 ok\r\n")
			}
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	d := dispatcher(t, `capabilities:
  network: [ { host: 127.0.0.1, scheme: tcp } ]
  secrets: [ { alias: "@api", bind: [127.0.0.1] } ]
`, nil)
	p, _ := strconv.Atoi(port)
	var dr abi.DialResponse
	if err := call(t, d, abi.FnTLSDial, abi.DialRequest{Host: "127.0.0.1", Port: p, TLS: abi.TLSNone, Protocol: abi.ProtoSMTP,
		Auth: &abi.DialAuth{Scheme: abi.AuthSMTPAuthPlain, Username: "me", Secret: "@api"}}, &dr); err != nil {
		t.Fatal(err)
	}
	call(t, d, abi.FnConnWrite, abi.ConnWrite{Conn: dr.Conn, Data: []byte("MAIL FROM:<me@example.com>\r\n")}, nil)
	if out := readAll(t, d, dr.Conn, "250"); !strings.HasPrefix(out, "250") {
		t.Fatal(out)
	}
	if <-got != "EHLO gate4ai.local" || !strings.HasPrefix(<-got, "AUTH PLAIN") || <-got != "MAIL FROM:<me@example.com>" {
		t.Fatal("unexpected SMTP dialogue")
	}
}

type fakeRouter struct{ got []abi.ServiceCallRequest }

func (f *fakeRouter) ServiceCall(_ context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	f.got = append(f.got, r)
	return abi.ExecResult{Structured: map[string]any{"sites": rule.Params["domains"], "contract": float64(rule.Contract)}}, nil
}

func TestServiceCallAndLog(t *testing.T) {
	d := dispatcher(t, `capabilities: { services: [ { target: browser, contract: 1, actions: [open, observe], params: { domains: ["${setting:allowed}"] } } ] }`,
		map[string]any{"allowed": []any{"lk.example", "b.example"}})
	var logs []string
	d.Log = func(l, m string) { logs = append(logs, m) }
	if err := call(t, d, abi.FnServiceCall, abi.ServiceCallRequest{Target: "browser", Action: "open"}, nil); code(err) != abi.CodeNotSupported {
		t.Fatal("no router:", err)
	}
	fr := &fakeRouter{}
	d.Services = fr
	for _, bad := range []abi.ServiceCallRequest{{Target: "browser", Action: "act"}, {Target: "mail", Action: "send"}} {
		if err := call(t, d, abi.FnServiceCall, bad, nil); code(err) != abi.CodeCapabilityDenied {
			t.Fatal(bad, err)
		}
	}
	var res abi.ExecResult
	if err := call(t, d, abi.FnServiceCall, abi.ServiceCallRequest{Target: "browser", Action: "open", Args: map[string]any{"url": "https://lk.example/"}}, &res); err != nil {
		t.Fatal(err)
	}
	st := res.Structured.(map[string]any)
	if fmt.Sprint(st["sites"]) != "[lk.example b.example]" || st["contract"] != 1.0 || len(fr.got) != 1 || fr.got[0].Args["url"] != "https://lk.example/" {
		t.Fatal(res, fr.got)
	}
	d.used = []string{"tok-SECRET-123"}
	call(t, d, abi.FnLog, abi.LogRequest{Level: "info", Msg: "token tok-SECRET-123 used"}, nil)
	if len(logs) != 1 || strings.Contains(logs[0], "tok-SECRET") {
		t.Fatal(logs)
	}
}
