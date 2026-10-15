// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// envAt — окружение CLI владельца на сокете sock (как у человека в терминале, если tty).
func envAt(sock string, tty bool) (*Env, *bytes.Buffer, *bytes.Buffer) {
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	return &Env{Stdin: strings.NewReader(""), Stdout: out, Stderr: errb, Getenv: func(string) string { return "" },
		Interactive: tty, StderrTTY: tty, StartBinary: "gate4ai-test",
		Connect: func(string) *Client { return NewSocketClient(sock, "") }}, out, errb
}

func shortDir(t *testing.T) string {
	d, err := os.MkdirTemp("", "g4ux")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestExplain(t *testing.T) {
	sock := "/run/user/1000/gate4ai/gate4ai.sock"
	cases := []struct {
		err          error
		agent        bool
		msg, hint, d string
	}{
		{&UnavailableError{Addr: sock, Err: os.ErrNotExist}, false, "gate4ai daemon is not running", "gate4ai daemon start", "no socket at " + sock},
		{&UnavailableError{Addr: sock, Err: syscall.ECONNREFUSED}, false, "stopped or crashed", "gate4ai service install", "daemon.log"},
		{&UnavailableError{Addr: sock, Err: os.ErrNotExist}, true, "gate4ai daemon is not running", "ask the owner", ""},
		{&UnavailableError{Addr: "/" + strings.Repeat("x", 200), Err: syscall.EINVAL}, false, "too long", "GATE4AI_SOCKET", "the limit is"},
		{&UnavailableError{Addr: sock, Err: os.ErrPermission}, false, "no access", "same user", ""},
		{&UnavailableError{Addr: "https://dev.relay.gate4.ai", Remote: true, Err: errors.New("timeout")}, true, "offline", "retry later", "dev.relay"},
		{&APIError{Status: 401, Code: "unauthorized", Message: "invalid or revoked token"}, true, "invalid or revoked token", "gate4ai agents token", ""},
	}
	for i, c := range cases {
		p := Explain(c.err, c.agent)
		if !strings.Contains(p.Message, c.msg) || !strings.Contains(p.Hint, c.hint) || !strings.Contains(p.Detail, c.d) {
			t.Errorf("%d: %+v", i, p)
		}
		if c.agent && strings.Contains(p.Hint, "service install") {
			t.Errorf("%d: owner command suggested to an agent: %s", i, p.Hint)
		}
	}
	// Код ошибки не повторяется в сообщении.
	if p := Explain(&APIError{Status: 404, Code: "not_found", Message: `agent "x" is not in the configuration`}, false); strings.Contains(p.Message, "not_found") {
		t.Fatal(p.Message)
	}
}

// Сценарий из запроса: `gate4ai ui` без daemon — понятная ошибка, а в терминале daemon запускается сам.
func TestUIWithoutDaemon(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "o.sock")
	started := 0
	old := StartDaemon
	t.Cleanup(func() { StartDaemon = old })
	StartDaemon = func(bin string, args []string, logPath string) (<-chan error, error) {
		started++
		c, _ := connectors.NewInProcess(mailFake{})
		d, err := daemon.New(daemon.Options{Home: filepath.Join(dir, "d"), InitialConfig: []byte(policy), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}})
		if err != nil {
			return nil, err
		}
		d.AddRoutes(fakeUIRoutes)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() { cancel(); d.Close() })
		ch := make(chan error, 1)
		go func() { ch <- d.Serve(ctx, sock, filepath.Join(dir, "a.sock")) }()
		return ch, nil
	}
	oldOpen := OpenURL
	t.Cleanup(func() { OpenURL = oldOpen })
	OpenURL = func(string) error { return nil }

	// Скрипт (не терминал): daemon не запускается, JSON с подсказкой, код 6.
	e, out, _ := envAt(sock, false)
	if code := Run(context.Background(), e, []string{"ui"}); code != ExitUnavailable || started != 0 {
		t.Fatalf("code %d started %d", code, started)
	}
	m := jsonOf(t, out.String())
	if m["code"] != "unavailable" || !strings.Contains(m["hint"].(string), "gate4ai daemon start") || strings.Contains(m["message"].(string), "dial unix") {
		t.Fatal(out)
	}
	// --no-start в терминале: тоже без запуска, и человеку — подсказка в stderr.
	e, _, errb := envAt(sock, true)
	if code := Run(context.Background(), e, []string{"ui", "--no-start"}); code != ExitUnavailable || started != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(errb.String(), "Start it: gate4ai daemon start") {
		t.Fatal(errb)
	}
	// В терминале: daemon запускается в фоне, затем выдаётся ссылка.
	e, out, _ = envAt(sock, true)
	if code := Run(context.Background(), e, []string{"ui"}); code != ExitOK || started != 1 || !strings.Contains(out.String(), "/login?t=") {
		t.Fatalf("code %d started %d: %s", code, started, out)
	}
	// daemon status / status без id.
	e, out, _ = envAt(sock, false)
	if code := Run(context.Background(), e, []string{"status"}); code != ExitOK || jsonOf(t, out.String())["running"] != true {
		t.Fatal(out)
	}
	// Уже запущен: daemon start ничего не запускает.
	e, out, _ = envAt(sock, false)
	if code := DaemonCtl(context.Background(), e, "start", nil); code != ExitOK || jsonOf(t, out.String())["started"] != false || started != 1 {
		t.Fatal(out)
	}
	// Остановка.
	e, out, _ = envAt(sock, false)
	if code := DaemonCtl(context.Background(), e, "stop", nil); code != ExitOK || jsonOf(t, out.String())["stopped"] != true {
		t.Fatal(out)
	}
	e, _, _ = envAt(sock, false)
	if code := Run(context.Background(), e, []string{"status"}); code != ExitUnavailable {
		t.Fatal(code)
	}
}

// fakeUIRoutes — маршрут ссылки входа без настоящего Web UI.
func fakeUIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ui/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"url":"http://127.0.0.1:7431/login?t=abc"}`)
	})
}

// Daemon упал при запуске: ошибка с концом журнала, а не ожидание 20 секунд.
func TestStartDaemonExited(t *testing.T) {
	dir := shortDir(t)
	old := StartDaemon
	t.Cleanup(func() { StartDaemon = old })
	StartDaemon = func(bin string, args []string, logPath string) (<-chan error, error) {
		os.WriteFile(logPath, []byte("gate4ai daemon: unknown keystore \"x\"\n"), 0o600)
		ch := make(chan error, 1)
		ch <- errors.New("exit status 1")
		return ch, nil
	}
	t.Setenv("GATE4AI_HOME", dir)
	e, out, _ := envAt(filepath.Join(dir, "o.sock"), false)
	begin := time.Now()
	if code := DaemonCtl(context.Background(), e, "start", []string{"--keystore", "x"}); code != ExitError || time.Since(begin) > 5*time.Second {
		t.Fatal(code)
	}
	if !strings.Contains(out.String(), "unknown keystore") {
		t.Fatal(out)
	}
}

func TestTyposAndHelp(t *testing.T) {
	dir := shortDir(t)
	// Без daemon: опечатка во встроенной команде видна сразу.
	e, out, _ := envAt(filepath.Join(dir, "none.sock"), false)
	if code := Run(context.Background(), e, []string{"aprovals"}); code != ExitUsage || !strings.Contains(out.String(), "did you mean gate4ai approvals?") {
		t.Fatal(code, out)
	}
	e, out, _ = envAt(filepath.Join(dir, "none.sock"), false)
	if code := Run(context.Background(), e, []string{"files", "read"}); code != ExitUnavailable || !strings.Contains(out.String(), "not a built-in command") {
		t.Fatal(code, out)
	}
	// С daemon: подсказка среди коннекторов и действий.
	h := start(t)
	if code, out := h.run(t, "", "emial", "send"); code != ExitUsage || !strings.Contains(out, "did you mean gate4ai email?") {
		t.Fatal(code, out)
	}
	if code, out := h.run(t, "", "email", "sned"); code != ExitUsage || !strings.Contains(out, "did you mean gate4ai email send?") {
		t.Fatal(code, out)
	}
	// --help у команд — код 0 и строки справки этой команды.
	for _, args := range [][]string{{"ui", "--help"}, {"agents", "-h"}, {"log", "--help"}, {"help", "policy"}, {"connectors", "install", "--help"}} {
		e, out, _ := envAt(filepath.Join(dir, "none.sock"), false)
		if code := Run(context.Background(), e, args); code != ExitOK || !strings.Contains(out.String(), "gate4ai "+strings.TrimPrefix(args[0], "help")) {
			t.Fatal(args, code, out)
		}
	}
	e, out, _ = envAt(filepath.Join(dir, "none.sock"), false)
	if Run(context.Background(), e, []string{"daemon", "--help"}); !strings.Contains(out.String(), "gate4ai daemon start") {
		t.Fatal(out)
	}
	// Пустой журнал — подсказка.
	if code, out := h.run(t, "", "log"); code != ExitOK || !strings.Contains(out, "journal is empty") {
		t.Fatal(code, out)
	}
	if code, out := h.run(t, "", "approvals", "--table"); code != ExitOK || !strings.Contains(out, "no approvals are waiting") {
		t.Fatal(code, out)
	}
}

func TestSuggest(t *testing.T) {
	for in, want := range map[string]string{"aprovals": "approvals", "polcy": "policy", "stauts": "status", "xyz": "", "ag": ""} {
		if got := suggest(in, commands); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
