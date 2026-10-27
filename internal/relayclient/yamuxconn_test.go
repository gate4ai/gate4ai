// SPDX-License-Identifier: AGPL-3.0-only

package relayclient

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Websocket (Hijack) поверх TLS на потоке yamux: после рукопожатия сервер
// читает сообщения клиента, а не получает «i/o deadline reached» от
// прерванного net/http фонового чтения.
func TestWebsocketOverYamuxTLS(t *testing.T) {
	cert, _, err := SelfSigned("x")
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	srvSess, err := yamux.Server(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	cliSess, err := yamux.Client(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srvSess.Close()
	defer cliSess.Close()
	got := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			got <- "accept: " + err.Error()
			return
		}
		defer c.CloseNow()
		_, msg, err := c.Read(r.Context())
		if err != nil {
			got <- "read: " + err.Error()
			return
		}
		got <- string(msg)
		_ = c.Write(r.Context(), websocket.MessageText, msg)
	})}
	go srv.Serve(tls.NewListener(yamuxListener{srvSess}, &tls.Config{Certificates: []tls.Certificate{cert}}))
	defer srv.Close()
	hc := &http.Client{Transport: &http.Transport{
		DialContext:     func(context.Context, string, string) (net.Conn, error) { return cliSess.Open() },
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "wss://x/ws", &websocket.DialOptions{HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := c.Read(ctx); err != nil || string(msg) != "ping" {
		t.Fatalf("client read: %q, %v", msg, err)
	}
	if s := <-got; s != "ping" {
		t.Fatalf("server: %s", s)
	}
}
