// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/autostart"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
)

const serviceUsage = `usage:
  gate4ai service install [--no-tray] [-- daemon flags]   start the daemon (and the tray) at every login, and the daemon now
  gate4ai service uninstall                                remove the autostart
  gate4ai service status                                   what is installed and whether the daemon runs`

func (e *Env) service(ctx context.Context, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return e.fail(err)
	}
	switch sub {
	case "install":
		return e.serviceInstall(ctx, home, args)
	case "uninstall":
		files, notes, err := autostart.Uninstall(home, false)
		if err != nil {
			return e.fail(err)
		}
		for _, n := range notes {
			e.say(n)
		}
		e.out(map[string]any{"status": "ok", "removed": files, "notes": notes}, nil)
		e.say("gate4ai autostart removed")
		return ExitOK
	case "status":
		res := autostart.State(home)
		res["status"] = "ok"
		// running здесь — daemon отвечает (как раньше); работает ли он
		// службой автозапуска — service_running.
		if v, ok := res["running"]; ok {
			res["service_running"] = v
		}
		_, herr := e.health(ctx)
		res["running"] = herr == nil
		e.out(res, kvTable(res))
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, serviceUsage)
	return ExitUsage
}

func (e *Env) serviceInstall(ctx context.Context, home string, args []string) int {
	var daemonArgs []string
	for i, a := range args {
		if a == "--" {
			daemonArgs, args = args[i+1:], args[:i]
			break
		}
	}
	noTray := false
	for _, a := range args {
		switch a {
		case "--no-tray", "-no-tray":
			noTray = true
		default:
			fmt.Fprintln(e.Stderr, serviceUsage)
			return ExitUsage
		}
	}
	bin := e.StartBinary
	if bin == "" {
		var err error
		if bin, err = os.Executable(); err != nil {
			return e.fail(err)
		}
		if r, err := filepath.EvalSymlinks(bin); err == nil {
			bin = r
		}
	}
	var notes []string
	if kept := withoutOneShot(daemonArgs); len(kept) != len(daemonArgs) {
		daemonArgs = kept
		notes = append(notes, "--reset-ui-login is a one-time flag: it is not saved in the autostart (run it once: gate4ai daemon stop && gate4ai daemon --reset-ui-login)")
	}
	spec := autostart.Spec{Bin: bin, Args: daemonArgs, Log: paths.DaemonLog(), Home: home}
	if !noTray {
		if spec.Tray = autostart.FindTray(bin); spec.Tray == "" {
			notes = append(notes, "gate4ai-tray not found next to gate4ai: only the daemon starts at login")
		}
	}
	// Daemon, запущенный руками, уступает автозапуску: иначе второй экземпляр
	// упирается в занятый сокет и перезапускается по кругу.
	if _, err := e.health(ctx); err == nil {
		if code := e.quiet().daemonStop(ctx); code != ExitOK {
			return e.fail(errors.New("could not stop the running daemon; stop it (gate4ai daemon stop) and repeat"))
		}
		notes = append(notes, "the daemon you started by hand was stopped; it now runs from the autostart")
	}
	res, err := autostart.Install(spec)
	if err != nil {
		return e.fail(err)
	}
	notes = append(notes, res.Notes...)
	if res.Started {
		if err := e.waitStarted(ctx, spec.Log); err != nil {
			return e.fail(err)
		}
	} else if err := e.startDaemon(ctx, daemonArgs); err != nil {
		return e.fail(err)
	}
	if spec.Tray != "" {
		notes = append(notes, "the tray icon starts at the next login; now: "+trayNowHint(spec.Tray))
	}
	for _, n := range notes {
		e.say(n)
	}
	e.say("gate4ai daemon is running and starts at every login. Open the Web UI: gate4ai ui")
	e.out(map[string]any{"status": "ok", "installed": res.Files, "running": true, "log": spec.Log, "notes": notes}, nil)
	return ExitOK
}

// waitStarted ждёт daemon, запущенный механизмом автозапуска.
func (e *Env) waitStarted(ctx context.Context, logPath string) error {
	for i := 0; i < 200; i++ {
		if _, err := e.health(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("the autostarted daemon did not answer in 20s: %s", logTail(logPath))
}

// quiet — то же окружение без сообщений человеку и JSON-вывода.
func (e *Env) quiet() *Env {
	q := *e
	q.Stdout, q.StderrTTY, q.ForceJSON = discard{}, false, true
	return &q
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func trayNowHint(tray string) string {
	if runtime.GOOS == "windows" {
		return "start " + tray
	}
	return tray + " &"
}
