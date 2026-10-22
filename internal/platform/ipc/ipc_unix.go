// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
)

// Listen создаёт unix-сокет с правами 0600 в каталоге 0700 и проверкой
// собеседника (SO_PEERCRED / getpeereid): принимаются только процессы того же пользователя.
func Listen(path string) (net.Listener, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("ipc: %s exists and is not a socket", path)
		}
		// Живой daemon? Тогда не отбираем у него сокет.
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return nil, fmt.Errorf("%w: %s", ErrInUse, path)
		}
		_ = os.Remove(path)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	uid := os.Getuid()
	return &checkedListener{Listener: l, check: func(c net.Conn) (Peer, error) {
		p, err := peerCred(c)
		if err != nil {
			return Peer{}, err
		}
		if p.UID != uid {
			return Peer{}, ErrPeerRejected
		}
		return p, nil
	}}, nil
}

// Dial подключается к сокету.
func Dial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// fdPeer — собеседник по дескриптору unix-соединения (read — getsockopt ОС).
func fdPeer(c net.Conn, read func(fd int) (Peer, error)) (Peer, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Peer{}, errors.New("ipc: not a unix connection")
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var p Peer
	var rerr error
	if err := rc.Control(func(fd uintptr) { p, rerr = read(int(fd)) }); err != nil {
		return Peer{}, err
	}
	return p, rerr
}

// MaxPath — предел длины пути сокета (sun_path без завершающего нуля).
func MaxPath() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}
