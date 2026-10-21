// SPDX-License-Identifier: Apache-2.0

package netauth

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
	"errors"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

func cert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// serve — фейковый сервер: h обслуживает одно соединение; got — строки клиента.
func serve(t *testing.T, h func(c net.Conn, r *bufio.Reader, got chan<- string)) (port int, got chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	got = make(chan string, 32)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		h(c, bufio.NewReader(c), got)
	}()
	return l.Addr().(*net.TCPAddr).Port, got
}

func dialLocal(port int) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(port))
	}
}

func code(err error) string {
	var e *abi.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func readLine(r *bufio.Reader) string {
	l, _ := r.ReadString('\n')
	return strings.TrimRight(l, "\r\n")
}

// STARTTLS: данные, присланные сервером вместе с ответом на STARTTLS (до
// рукопожатия), — отказ (защита от внедрения команд открытым текстом).
func TestIMAPStartTLSInjection(t *testing.T) {
	port, _ := serve(t, func(c net.Conn, r *bufio.Reader, got chan<- string) {
		io.WriteString(c, "* OK ready\r\n")
		tag, _, _ := strings.Cut(readLine(r), " ")
		io.WriteString(c, tag+" OK begin\r\n* PREAUTH injected\r\n")
		time.Sleep(200 * time.Millisecond)
	})
	_, pool := cert(t)
	_, err := Dial(context.Background(), Config{DialRequest: abi.DialRequest{Host: "localhost", Port: port, TLS: abi.TLSStartTLS, Protocol: abi.ProtoIMAP,
		Auth: &abi.DialAuth{Scheme: abi.AuthIMAPLogin, Username: "me", Secret: "@x"}}, Password: "fake-pass", Dial: dialLocal(port), RootCAs: pool, Timeout: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "unexpected data") {
		t.Fatal(err)
	}
}

// IMAP LOGIN: ASCII — quoted со экранированием, не-ASCII и управляющие
// символы — литералом (CRLF в пароле не превращается в новую команду).
func TestIMAPLoginQuoting(t *testing.T) {
	cases := map[string]string{
		`pa"ss\word`:       `g1 LOGIN "me" "pa\"ss\\word"`,
		"pässword":         `g1 LOGIN "me" {9}`,
		"x\r\ng2 DELETE a": `g1 LOGIN "me" {14}`,
	}
	for pass, wantCmd := range cases {
		port, got := serve(t, func(c net.Conn, r *bufio.Reader, got chan<- string) {
			io.WriteString(c, "* OK ready\r\n")
			l := readLine(r)
			got <- l
			if strings.HasSuffix(l, "}") {
				n, _ := strconv.Atoi(l[strings.LastIndexByte(l, '{')+1 : len(l)-1])
				io.WriteString(c, "+ go\r\n")
				b := make([]byte, n+2)
				io.ReadFull(r, b)
				got <- string(b[:n])
			}
			io.WriteString(c, "g1 OK logged in\r\n")
		})
		conn, err := Dial(context.Background(), Config{DialRequest: abi.DialRequest{Host: "127.0.0.1", Port: port, TLS: abi.TLSNone, Protocol: abi.ProtoIMAP,
			Auth: &abi.DialAuth{Scheme: abi.AuthIMAPLogin, Username: "me", Secret: "@x"}}, Password: pass, Dial: dialLocal(port), Timeout: 2 * time.Second})
		if err != nil {
			t.Fatal(pass, err)
		}
		if !strings.HasPrefix(string(conn.Greeting), "* PREAUTH") {
			t.Fatal(string(conn.Greeting))
		}
		conn.Close()
		if l := <-got; l != wantCmd {
			t.Errorf("%q: %q", pass, l)
		}
		if strings.HasSuffix(wantCmd, "}") {
			if lit := <-got; lit != pass {
				t.Errorf("literal %q", lit)
			}
		}
	}
}

// Неверный пароль: ошибка auth без текста сервера (а значит, и без эха значения).
func TestAuthFailureMessages(t *testing.T) {
	const pass = "fake-secret-pass"
	port, _ := serve(t, func(c net.Conn, r *bufio.Reader, got chan<- string) {
		io.WriteString(c, "220 fake\r\n")
		for {
			l := readLine(r)
			switch {
			case l == "":
				return
			case strings.HasPrefix(l, "EHLO"):
				io.WriteString(c, "250 AUTH PLAIN\r\n")
			case strings.HasPrefix(l, "AUTH PLAIN "):
				b, _ := base64.StdEncoding.DecodeString(l[11:])
				io.WriteString(c, "535 bad credentials for "+string(b)+" "+l[11:]+"\r\n")
			}
		}
	})
	_, err := Dial(context.Background(), Config{DialRequest: abi.DialRequest{Host: "127.0.0.1", Port: port, TLS: abi.TLSNone, Protocol: abi.ProtoSMTP,
		Auth: &abi.DialAuth{Scheme: abi.AuthSMTPAuthPlain, Username: "me", Secret: "@x"}}, Password: pass, Dial: dialLocal(port), Timeout: 2 * time.Second})
	if code(err) != abi.CodeAuth || strings.Contains(err.Error(), pass) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte("\x00me\x00"+pass))) {
		t.Fatal(err)
	}
	port, _ = serve(t, func(c net.Conn, r *bufio.Reader, got chan<- string) {
		io.WriteString(c, "* OK ready\r\n")
		readLine(r)
		io.WriteString(c, "g1 NO wrong password "+pass+"\r\n")
	})
	_, err = Dial(context.Background(), Config{DialRequest: abi.DialRequest{Host: "127.0.0.1", Port: port, TLS: abi.TLSNone, Protocol: abi.ProtoIMAP,
		Auth: &abi.DialAuth{Scheme: abi.AuthIMAPLogin, Username: "me", Secret: "@x"}}, Password: pass, Dial: dialLocal(port), Timeout: 2 * time.Second})
	if code(err) != abi.CodeAuth || strings.Contains(err.Error(), pass) {
		t.Fatal(err)
	}
}

// SMTP через STARTTLS с проверкой сертификата; сертификат чужого имени — отказ.
func TestSMTPStartTLS(t *testing.T) {
	crt, pool := cert(t)
	srv := func(c net.Conn, r *bufio.Reader, got chan<- string) {
		io.WriteString(c, "220 fake\r\n")
		var conn net.Conn = c
		for {
			l := readLine(r)
			if l == "" {
				return
			}
			got <- l
			switch {
			case strings.HasPrefix(l, "EHLO"):
				io.WriteString(conn, "250-fake\r\n250 STARTTLS\r\n")
			case l == "STARTTLS":
				io.WriteString(conn, "220 go\r\n")
				tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{crt}})
				conn, r = tc, bufio.NewReader(tc)
			case strings.HasPrefix(l, "AUTH PLAIN "):
				io.WriteString(conn, "235 ok\r\n")
			}
		}
	}
	port, got := serve(t, srv)
	conn, err := Dial(context.Background(), Config{DialRequest: abi.DialRequest{Host: "localhost", Port: port, TLS: abi.TLSStartTLS, Protocol: abi.ProtoSMTP,
		Auth: &abi.DialAuth{Scheme: abi.AuthSMTPAuthPlain, Username: "me", Secret: "@x"}}, Password: "fake-pass", Dial: dialLocal(port), RootCAs: pool, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	var lines []string
	for len(got) > 0 {
		lines = append(lines, <-got)
	}
	if len(lines) != 4 || lines[1] != "STARTTLS" || !strings.HasPrefix(lines[3], "AUTH PLAIN") {
		t.Fatal(lines)
	}
	// Сервер с сертификатом не того имени — пароль не отправляется.
	port, got = serve(t, srv)
	_, err = Dial(context.Background(), Config{DialRequest: abi.DialRequest{Host: "mail.example.com", Port: port, TLS: abi.TLSStartTLS, Protocol: abi.ProtoSMTP,
		Auth: &abi.DialAuth{Scheme: abi.AuthSMTPAuthPlain, Username: "me", Secret: "@x"}}, Password: "fake-pass", Dial: dialLocal(port), RootCAs: pool, Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("certificate for another name accepted")
	}
	time.Sleep(50 * time.Millisecond)
	for len(got) > 0 {
		if l := <-got; strings.HasPrefix(l, "AUTH") {
			t.Fatal("password sent without a valid TLS")
		}
	}
}

func TestBadConfig(t *testing.T) {
	for _, c := range []Config{
		{DialRequest: abi.DialRequest{Host: "h", TLS: abi.TLSNone}},              // нет порта
		{DialRequest: abi.DialRequest{Host: "h", Port: 1, TLS: "weird"}},         // режим TLS
		{DialRequest: abi.DialRequest{Host: "h", Port: 1, TLS: abi.TLSStartTLS}}, // STARTTLS без протокола
		{DialRequest: abi.DialRequest{Host: "h", Port: 1, TLS: abi.TLSNone, Protocol: abi.ProtoSMTP, // auth не того протокола
			Auth: &abi.DialAuth{Scheme: abi.AuthIMAPLogin}}},
	} {
		if _, err := Dial(context.Background(), c); code(err) != abi.CodeBadRequest {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if DefaultPort(abi.ProtoIMAP, abi.TLSDirect) != 993 || DefaultPort(abi.ProtoSMTP, abi.TLSStartTLS) != 587 || DefaultPort(abi.ProtoSMTP, abi.TLSNone) != 25 {
		t.Fatal("default ports")
	}
}
