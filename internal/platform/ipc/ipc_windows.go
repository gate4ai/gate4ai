// SPDX-License-Identifier: AGPL-3.0-only

//go:build windows

package ipc

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Listen создаёт named pipe с security descriptor, разрешающим доступ только
// SID текущего пользователя. go-winio создаёт первый экземпляр с
// FILE_FLAG_FIRST_PIPE_INSTANCE, поэтому перехватить имя pipe нельзя;
// собеседник проверяется по GetNamedPipeClientProcessId и владельцу процесса.
func Listen(path string) (net.Listener, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := u.User.Sid.String()
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: fmt.Sprintf("D:P(A;;GA;;;%s)", sid)})
	if err != nil {
		return nil, err
	}
	return &checkedListener{Listener: l, check: func(c net.Conn) (Peer, error) {
		return Peer{}, nil
	}}, nil
}

// Dial подключается к named pipe.
func Dial(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, path)
}

// MaxPath — у named pipe предела длины пути, как у unix-сокета, нет.
func MaxPath() int { return 0 }
