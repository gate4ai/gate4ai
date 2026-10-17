// SPDX-License-Identifier: AGPL-3.0-only

// Package ipc — локальный канал трея и CLI к daemon: unix-сокет (macOS, Linux)
// или named pipe (Windows) за общим интерфейсом; доступ только текущему
// пользователю, проверка собеседника. TCP на localhost не используется.
package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Peer — проверенный собеседник.
type Peer struct {
	UID int
	PID int
}

type peerKey struct{}

// PeerFrom возвращает собеседника из контекста HTTP-запроса.
func PeerFrom(ctx context.Context) (Peer, bool) {
	p, ok := ctx.Value(peerKey{}).(Peer)
	return p, ok
}

// ErrPeerRejected — собеседник не прошёл проверку.
var ErrPeerRejected = errors.New("ipc: peer rejected")

// ErrInUse — сокет занят работающим daemon.
var ErrInUse = errors.New("gate4ai daemon is already running on this socket")

// CheckPath — путь сокета помещается в sun_path (у named pipe предела нет).
func CheckPath(path string) error {
	if max := MaxPath(); max > 0 && len(path) > max {
		return fmt.Errorf("socket path %s is too long (%d bytes, the limit is %d): set GATE4AI_SOCKET / GATE4AI_AGENT_SOCKET or a shorter GATE4AI_HOME", path, len(path), max)
	}
	return nil
}

// Alive — на сокете / pipe отвечает работающий daemon.
func Alive(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := Dial(ctx, path)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// peerConn — соединение с проверенным собеседником.
type peerConn struct {
	net.Conn
	peer Peer
}

// ConnContext кладёт собеседника в контекст запроса (для http.Server.ConnContext).
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if pc, ok := c.(*peerConn); ok {
		return context.WithValue(ctx, peerKey{}, pc.peer)
	}
	return ctx
}

// checkedListener отбрасывает соединения чужих пользователей.
type checkedListener struct {
	net.Listener
	check func(net.Conn) (Peer, error)
}

func (l *checkedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		p, err := l.check(c)
		if err != nil {
			c.Close()
			continue
		}
		return &peerConn{Conn: c, peer: p}, nil
	}
}

// HTTPClient — HTTP-клиент поверх сокета / pipe.
func HTTPClient(addr string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:  func(ctx context.Context, _, _ string) (net.Conn, error) { return Dial(ctx, addr) },
		MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second,
	}}
}
