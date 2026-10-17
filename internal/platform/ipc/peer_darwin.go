// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package ipc

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerCred(c net.Conn) (Peer, error) {
	var p Peer
	var serr error
	err := rawFD(c, func(fd uintptr) {
		x, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			serr = err
			return
		}
		p = Peer{UID: int(x.Uid)}
		if pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			p.PID = pid
		}
	})
	if err != nil {
		return p, err
	}
	return p, serr
}
