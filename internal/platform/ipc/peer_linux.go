// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerCred(c net.Conn) (Peer, error) {
	var p Peer
	var serr error
	err := rawFD(c, func(fd uintptr) {
		u, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			serr = err
			return
		}
		p = Peer{UID: int(u.Uid), PID: int(u.Pid)}
	})
	if err != nil {
		return p, err
	}
	return p, serr
}
