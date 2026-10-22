// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerCred(c net.Conn) (Peer, error) {
	return fdPeer(c, func(fd int) (Peer, error) {
		u, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			return Peer{}, err
		}
		return Peer{UID: int(u.Uid), PID: int(u.Pid)}, nil
	})
}
