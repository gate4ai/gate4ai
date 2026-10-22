// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package ipc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Сервер и клиент одного пользователя: Dial проходит проверку сервера, а
// сервер видит PID клиента (проверка клиента по SID, а не заглушка Peer{}).
func TestPipePeerChecks(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\gate4ai-test-%d`, os.Getpid())
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan Peer, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		got <- c.(*peerConn).peer
		c.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case p := <-got:
		if p.PID != os.Getpid() {
			t.Fatalf("peer pid %d, want %d", p.PID, os.Getpid())
		}
	case <-ctx.Done():
		t.Fatal("no accepted connection")
	}
}
