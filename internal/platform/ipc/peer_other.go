// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build unix && !linux && !darwin

package ipc

import (
	"errors"
	"net"
)

func peerCred(c net.Conn) (Peer, error) {
	return Peer{}, errors.New("ipc: peer credentials not supported on this OS")
}
