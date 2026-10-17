// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package ipc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestSocketPermsAndPeer(t *testing.T) {
	dir, _ := os.MkdirTemp("", "g4a")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "run", "gate4ai.sock")
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket perm %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm %v", di.Mode().Perm())
	}
	srv := &http.Server{ConnContext: ConnContext, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PeerFrom(r.Context())
		fmt.Fprintf(w, "%v %d", ok, p.UID)
	})}
	go srv.Serve(l)
	defer srv.Close()
	resp, err := HTTPClient(path).Get("http://gate4ai/x")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != fmt.Sprintf("true %d", os.Getuid()) {
		t.Fatalf("peer: %s", b)
	}
	// Второй daemon не может занять живой сокет.
	if _, err := Listen(path); err == nil {
		t.Fatal("must refuse live socket")
	}
	// Обычный файл не удаляется.
	f := filepath.Join(dir, "file")
	os.WriteFile(f, nil, 0o600)
	if _, err := Listen(f); err == nil {
		t.Fatal("must refuse regular file")
	}
	_ = context.Background
}

func TestRejectForeignPeer(t *testing.T) {
	ln, _ := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	cl := &checkedListener{Listener: ln, check: func(c net.Conn) (Peer, error) {
		p, err := peerCred(c)
		if err != nil {
			return p, err
		}
		if p.UID != os.Getuid()+1 { // эмулируем чужого пользователя
			return Peer{}, ErrPeerRejected
		}
		return p, nil
	}}
	done := make(chan error, 1)
	go func() { _, err := cl.Accept(); done <- err }()
	c, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("foreign peer connection must be closed")
	}
	ln.Close()
	<-done
}
