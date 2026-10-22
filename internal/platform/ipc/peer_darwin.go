// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerCred(c net.Conn) (Peer, error) {
	return fdPeer(c, func(fd int) (Peer, error) {
		x, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			return Peer{}, err
		}
		p := Peer{UID: int(x.Uid)}
		if pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			p.PID = pid
		}
		return p, nil
	})
}
