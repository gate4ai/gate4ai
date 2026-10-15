// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/paths"
)

// DaemonUsage — `gate4ai daemon --help`.
const DaemonUsage = `usage:
  gate4ai daemon [flags]           run the daemon in this terminal (Ctrl+C stops it)
  gate4ai daemon start [flags]     run it in the background (log: ` + "<data dir>/daemon.log" + `)
  gate4ai daemon stop              stop a running daemon
  gate4ai daemon status            is it running: socket, policy version, connectors, pending approvals
  gate4ai service install          start the daemon (and the tray) at every login

flags (gate4ai daemon and gate4ai daemon start):
  --home dir                 data directory
  --config file.yaml         initial configuration if the config store is empty
  --keystore auto|keychain|file
  --socket path              owner socket / named pipe
  --agent-socket path        agent socket / named pipe
  --connectors dir           installed connectors directory (default <home>/connectors)
  --ui addr                  web UI address, loopback only (default 127.0.0.1:7431; "" disables)
  --public addr --public-url https://host [--public-cert f --public-key f]   public address without relay
  --egress addr              egress proxy address for sandboxed agents
  --telegram-api url         Telegram Bot API base URL
  --version                  print version
`

// StartDaemon запускает `bin daemon args…` отдельным процессом, отвязанным от
// терминала, с выводом в logPath; exited получает результат, если процесс
// завершился (подменяется в тестах).
var StartDaemon = func(bin string, args []string, logPath string) (exited <-chan error, err error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cmd := exec.Command(bin, append([]string{"daemon"}, args...)...)
	cmd.Stdout, cmd.Stderr = f, f
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan error, 1)
	go func() { ch <- cmd.Wait() }()
	return ch, nil
}

// DaemonCtl — `gate4ai daemon start | stop | status`.
func DaemonCtl(ctx context.Context, e *Env, sub string, args []string) int {
	if sub != "start" { // флаги start уходят daemon как есть
		var j bool
		if args, j = stripJSONFlag(args); j {
			e.ForceJSON = true
		}
	}
	switch sub {
	case "start":
		return e.daemonStart(ctx, args)
	case "stop":
		return e.daemonStop(ctx)
	case "status":
		return e.daemonStatus(ctx)
	}
	fmt.Fprint(e.Stderr, DaemonUsage)
	return ExitUsage
}

// health — daemon отвечает на сокете владельца.
func (e *Env) health(ctx context.Context) (map[string]any, error) {
	var h map[string]any
	err := e.Connect("").Do(ctx, "GET", "/v1/health", nil, &h)
	return h, err
}

func (e *Env) daemonStart(ctx context.Context, args []string) int {
	if _, err := e.health(ctx); err == nil {
		e.out(map[string]any{"status": "ok", "running": true, "started": false}, nil)
		e.say("gate4ai daemon is already running. Open the Web UI: gate4ai ui")
		return ExitOK
	}
	if err := e.startDaemon(ctx, args); err != nil {
		return e.fail(err)
	}
	e.out(map[string]any{"status": "ok", "running": true, "started": true, "log": paths.DaemonLog()}, nil)
	return ExitOK
}

// startDaemon запускает daemon в фоне и ждёт, пока он ответит.
func (e *Env) startDaemon(ctx context.Context, args []string) error {
	if e.StartBinary == "" {
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		e.StartBinary = bin
	}
	logPath := paths.DaemonLog()
	if v := flagValue(args, "home"); v != "" && e.Getenv("GATE4AI_HOME") == "" {
		logPath = filepath.Join(v, "daemon.log")
	}
	e.say("Starting gate4ai daemon in the background (log: " + logPath + ")…")
	exited, err := StartDaemon(e.StartBinary, args, logPath)
	if err != nil {
		return fmt.Errorf("start gate4ai daemon: %w", err)
	}
	c := e.Connect("")
	if s := flagValue(args, "socket"); s != "" {
		c = NewSocketClient(s, "")
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var h map[string]any
		err := c.Do(ctx, "GET", "/v1/health", nil, &h)
		if err == nil {
			e.say("gate4ai daemon is running. Stop it: gate4ai daemon stop. Start it at every login: gate4ai service install")
			return nil
		}
		if !errors.Is(err, ErrUnavailable) {
			return err
		}
		select {
		case xerr := <-exited:
			return fmt.Errorf("gate4ai daemon exited (%v): %s", xerr, logTail(logPath))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gate4ai daemon did not answer in 20s; see %s", logPath)
		}
	}
}

// logTail — последние строки журнала daemon для сообщения об ошибке.
func logTail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "see " + path
	}
	lines := strings.Split(strings.TrimSpace(string(bytes.ToValidUTF8(b, nil))), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, " | ") + " (log: " + path + ")"
}

// flagValue — значение --name v / --name=v среди аргументов.
func flagValue(args []string, name string) string {
	for i, a := range args {
		a = "-" + strings.TrimLeft(a, "-")
		if a == "-"+name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "-"+name+"="); ok {
			return v
		}
	}
	return ""
}

func (e *Env) daemonStop(ctx context.Context) int {
	c := e.Connect("")
	if err := c.Do(ctx, "POST", "/v1/shutdown", nil, nil); err != nil {
		if errors.Is(err, ErrUnavailable) {
			e.out(map[string]any{"status": "ok", "running": false}, nil)
			e.say("gate4ai daemon is not running.")
			return ExitOK
		}
		return e.fail(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := e.health(ctx); errors.Is(err, ErrUnavailable) {
			e.out(map[string]any{"status": "ok", "running": false, "stopped": true}, nil)
			e.say("gate4ai daemon stopped. If it runs at login (gate4ai service install), it starts again at the next login.")
			return ExitOK
		}
		time.Sleep(100 * time.Millisecond)
	}
	return e.fail(errors.New("gate4ai daemon did not stop in 10s"))
}

// daemonStatus — `gate4ai daemon status` и `gate4ai status` без id.
func (e *Env) daemonStatus(ctx context.Context) int {
	c := e.Client()
	var h map[string]any
	if err := c.Do(ctx, "GET", "/v1/health", nil, &h); err != nil {
		return e.fail(err)
	}
	res := map[string]any{"status": "ok", "running": true}
	for k, v := range h {
		if k != "ok" {
			res[k] = v
		}
	}
	if c.Addr != nil {
		res["socket"] = c.Addr()
	}
	if e.Getenv("GATE4AI_TOKEN") == "" {
		var pending []map[string]any
		if c.Do(ctx, "GET", "/v1/approvals?status=pending", nil, &pending) == nil {
			res["pending_approvals"] = len(pending)
		}
		var relay map[string]any
		if c.Do(ctx, "GET", "/v1/relay/status", nil, &relay) == nil {
			res["relay"] = relay
		}
	}
	e.out(res, func(w io.Writer) {
		fmt.Fprintf(w, "gate4ai daemon is running\n")
		kvTable(res, "running")(w)
	})
	return ExitOK
}

// say — сообщение человеку (stderr), только если stderr — терминал.
func (e *Env) say(msg string) {
	if e.StderrTTY {
		fmt.Fprintln(e.Stderr, msg)
	}
}

// StartBackground запускает daemon в фоне и ждёт ответа (трей); bin — gate4ai.
func StartBackground(ctx context.Context, bin string) error {
	e := DefaultEnv()
	e.Stdout, e.Stderr, e.StderrTTY, e.StartBinary = discard{}, discard{}, false, bin
	return e.startDaemon(ctx, nil)
}
