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
// SID текущего пользователя. Каждое принятое соединение проверяется ещё раз:
// процесс клиента (GetNamedPipeClientProcessId) должен работать от того же SID.
// Перехват имени pipe до запуска daemon закрывает Dial: он проверяет, что
// сервер — процесс этого пользователя.
func Listen(path string) (net.Listener, error) {
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	l, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: fmt.Sprintf("D:P(A;;GA;;;%s)", sid.String())})
	if err != nil {
		return nil, err
	}
	return &checkedListener{Listener: l, check: func(c net.Conn) (Peer, error) {
		pid, ok := pipePeer(c, windows.GetNamedPipeClientProcessId, sid)
		if !ok {
			return Peer{}, ErrPeerRejected
		}
		return Peer{PID: int(pid)}, nil
	}}, nil
}

// Dial подключается к named pipe и проверяет, что на том конце — процесс
// текущего пользователя: иначе другой пользователь, заранее занявший имя pipe,
// получил бы секреты из `vault set`, решения по подтверждениям и прочее.
func Dial(ctx context.Context, path string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	sid, err := currentSID()
	if err != nil {
		c.Close()
		return nil, err
	}
	if _, ok := pipePeer(c, windows.GetNamedPipeServerProcessId, sid); !ok {
		c.Close()
		return nil, fmt.Errorf("%w: %s is not served by a process of the current user", ErrPeerRejected, path)
	}
	return c, nil
}

// MaxPath — у named pipe предела длины пути, как у unix-сокета, нет.
func MaxPath() int { return 0 }

func currentSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}

// pipePeer — pid процесса на другом конце pipe (getPID — клиент или сервер),
// если он работает от SID want.
func pipePeer(c net.Conn, getPID func(windows.Handle, *uint32) error, want *windows.SID) (uint32, bool) {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return 0, false
	}
	h := windows.Handle(f.Fd())
	var pid uint32
	if getPID(h, &pid) != nil || !sameUser(h, pid, want) {
		return 0, false
	}
	return pid, true
}

// sameUser — процесс pid работает от SID want. Если токен процесса прочитать
// нельзя (например, он запущен с повышенными правами), решает владелец объекта
// pipe: им становится создатель pipe.
func sameUser(pipe windows.Handle, pid uint32, want *windows.SID) bool {
	if s, err := processSID(pid); err == nil {
		return s.Equals(want)
	}
	sd, err := windows.GetSecurityInfo(pipe, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	return err == nil && owner != nil && owner.Equals(want)
}

func processSID(pid uint32) (*windows.SID, error) {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(p)
	var tok windows.Token
	if err := windows.OpenProcessToken(p, windows.TOKEN_QUERY, &tok); err != nil {
		return nil, err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}
