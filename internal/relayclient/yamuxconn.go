// SPDX-License-Identifier: AGPL-3.0-only

package relayclient

import (
	"errors"
	"net"
	"os"

	"github.com/hashicorp/yamux"
)

// yamuxListener — потоки yamux для net/http поверх TLS. yamux.ErrTimeout не
// «временная» (Temporary() == false), а crypto/tls после невременной ошибки
// считает соединение сломанным навсегда. net/http при Hijack (websocket:
// просмотр сеанса браузера в Web UI) нарочно прерывает фоновое чтение
// дедлайном — через relay соединение умирало сразу после рукопожатия
// («Связь потеряна, подключаюсь снова…» по кругу). Таймаут подменяется
// os.ErrDeadlineExceeded — такой же net.Error, но временный, как у TCP.
type yamuxListener struct{ *yamux.Session }

func (l yamuxListener) Accept() (net.Conn, error) {
	c, err := l.Session.Accept()
	if err != nil {
		return nil, err
	}
	return deadlineConn{c}, nil
}

type deadlineConn struct{ net.Conn }

func (c deadlineConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, tcpTimeout(err)
}

func (c deadlineConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, tcpTimeout(err)
}

func tcpTimeout(err error) error {
	if errors.Is(err, yamux.ErrTimeout) {
		return os.ErrDeadlineExceeded
	}
	return err
}
