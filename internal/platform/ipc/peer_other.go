// SPDX-License-Identifier: AGPL-3.0-only

//go:build unix && !linux && !darwin

package ipc

import (
	"errors"
	"net"
)

func peerCred(c net.Conn) (Peer, error) {
	return Peer{}, errors.New("ipc: peer credentials not supported on this OS")
}
