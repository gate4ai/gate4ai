// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

// Сеть контейнера (уровень 2). Контейнер запускается с `--network none`: у
// него есть только loopback, прямого выхода наружу нет. Egress Proxy доступен
// так: на хосте `gate4ai run` слушает unix-сокет и пересылает соединения на
// TCP-адрес прокси (StartBridge); сокет примонтирован в контейнер, а внутри
// бинарник gate4ai — точка входа контейнера — слушает 127.0.0.1:ShimPort и
// пересылает соединения в этот сокет (Shim), затем запускает агента с
// HTTP(S)_PROXY=http://…@127.0.0.1:ShimPort. Аутентификация запуска на
// прокси — та же (пароль в адресе прокси).

// ShimCommand — скрытая команда gate4ai внутри контейнера (точка входа).
const ShimCommand = "_egress-shim"

// ShimPort — порт прокси внутри контейнера (loopback контейнера).
const ShimPort = 3128

// Пути внутри контейнера.
const (
	containerProxySock = "/run/gate4ai-proxy.sock"
	containerGateBin   = "/usr/local/bin/gate4ai"
)

// Bridge — пересылка с unix-сокета на хосте на TCP-адрес Egress Proxy.
type Bridge struct {
	Socket string `json:"socket"` // unix-сокет на хосте (монтируется в контейнер)
	Target string `json:"target"` // host:port прокси
}

// StartBridge слушает b.Socket и пересылает каждое соединение на b.Target до
// отмены ctx или вызова stop.
func StartBridge(ctx context.Context, b Bridge) (stop func(), err error) {
	_ = os.Remove(b.Socket)
	l, err := net.Listen("unix", b.Socket)
	if err != nil {
		return nil, err
	}
	// Каталог запуска — 0700 (другие пользователи хоста сокет не видят);
	// пользователь внутри контейнера может быть не тем же uid, что на хосте.
	_ = os.Chmod(b.Socket, 0o666)
	ctx, cancel := context.WithCancel(ctx)
	go func() { <-ctx.Done(); l.Close() }()
	go forward(l, func() (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", b.Target)
	})
	return func() { cancel(); _ = os.Remove(b.Socket) }, nil
}

// forward принимает соединения l и соединяет каждое с dial().
func forward(l net.Listener, dial func() (net.Conn, error)) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			up, err := dial()
			if err != nil {
				return
			}
			defer up.Close()
			pipe(c, up)
		}()
	}
}

// pipe копирует данные в обе стороны до конца любой из них.
func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// ShimListen — слушатель прокси внутри контейнера: соединения с listen
// пересылаются в unix-сокет sock.
func ShimListen(listen, sock string) (net.Listener, error) {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	go forward(l, func() (net.Conn, error) { return net.Dial("unix", sock) })
	return l, nil
}

// Shim — точка входа контейнера (`gate4ai _egress-shim --listen … --socket …
// -- команда …`): поднимает пересылку прокси и запускает агента; код выхода —
// код агента.
func Shim(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(ShimCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", fmt.Sprintf("127.0.0.1:%d", ShimPort), "proxy address inside the container")
	sock := fs.String("socket", containerProxySock, "unix socket of the Egress Proxy bridge")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fmt.Fprintln(stderr, "gate4ai "+ShimCommand+": command required after --")
		return 2
	}
	l, err := ShimListen(*listen, *sock)
	if err != nil {
		fmt.Fprintln(stderr, "gate4ai "+ShimCommand+":", err)
		return 1
	}
	defer l.Close()
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "gate4ai "+ShimCommand+":", err)
		return 127
	}
	// Точка входа — PID 1 контейнера: сигналы остановки передаются агенту.
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop := ctx.Done()
	for {
		select {
		case s := <-sig:
			_ = cmd.Process.Signal(s)
		case <-stop:
			_ = cmd.Process.Kill()
			stop = nil
		case err := <-done:
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode()
			}
			if err != nil {
				return 1
			}
			return 0
		}
	}
}
