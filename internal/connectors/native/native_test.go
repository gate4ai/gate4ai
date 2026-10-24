// SPDX-License-Identifier: AGPL-3.0-only

package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	sdknative "github.com/gate4ai/gate4ai/sdk-go/native"

	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
)

const testManifest = `connector: echo
version: "1"
runtime: native
platforms: [linux/amd64]
capabilities:
  files: ["${setting:root}/**"]
  reveal: ["@model.key"]
limits: { timeout_s: 2 }
actions:
  - { name: read, class: read, input_schema: { type: object, properties: { path: { type: string } } } }
  - { name: crash, class: read }
  - { name: slow, class: read }
  - { name: wait, class: read }
`

// echo — нативный коннектор теста: тот же бинарник теста, запущенный с GATE4AI_TEST_ECHO=1.
type echo struct{}

func (echo) Execute(ctx context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	switch r.Action {
	case "wait": // ждёт отмены и отмечает её в файле (процесс узнал об отмене)
		<-ctx.Done()
		os.WriteFile(os.Getenv("GATE4AI_TEST_MARK"), []byte(ctx.Err().Error()), 0o600)
		return abi.ExecResult{}, ctx.Err()
	case "crash":
		os.Exit(3)
	case "slow":
		time.Sleep(5 * time.Second)
	}
	p, _ := r.Args["path"].(string)
	f, err := h.FSRead(p, 100)
	if err != nil {
		return abi.ExecResult{}, err
	}
	h.Evidence("e.txt", "text/plain", []byte("ev"))
	return abi.ExecResult{Structured: map[string]any{"data": string(f.Data), "key": r.Revealed["@model.key"]}}, nil
}

func (echo) Verify(context.Context, *abi.Host, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified}, nil
}

// emitter — запросы владельца: echo отвечает и шлёт событие с теми же аргументами.
var emitter func(kind string, data any)

func (echo) SetEmit(f func(kind string, data any)) { emitter = f }

func (echo) Owner(_ context.Context, op string, args json.RawMessage) (any, error) {
	emitter(op, args)
	return map[string]string{"op": op}, nil
}

func TestMain(m *testing.M) {
	if os.Getenv("GATE4AI_TEST_ECHO") == "1" {
		sdknative.Serve([]byte(testManifest), echo{})
		os.Exit(0)
	}
	testEnvPrefix = "GATE4AI_TEST_"
	os.Exit(m.Run())
}

type vault map[string]string

func (v vault) Value(a string) (string, error) { return v[a], nil }

func setup(t *testing.T) (*Process, string) {
	t.Helper()
	t.Setenv("GATE4AI_TEST_ECHO", "1")
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "notes"), 0o755)
	os.WriteFile(filepath.Join(home, "notes", "a.md"), []byte("hello"), 0o644)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e, err := wasm.NewEngine("", wasm.Services{Home: home, Secrets: vault{"@model.key": "sk-1"}, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	m, err := manifest.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(os.Args[0], m, e, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, home
}

func TestExecuteWithHostChecks(t *testing.T) {
	p, _ := setup(t)
	if p.State() != StateStopped {
		t.Fatal(p.State())
	}
	set := map[string]any{"root": "~/notes"}
	res, err := p.Execute(context.Background(), abi.ExecRequest{Action: "read", Args: map[string]any{"path": "~/notes/a.md"}, Settings: set})
	if err != nil {
		t.Fatal(err)
	}
	st := res.Structured.(map[string]any)
	if st["data"] != "hello" || st["key"] != "sk-1" || len(res.Evidence) != 1 {
		t.Fatal(res)
	}
	_, err = p.Execute(context.Background(), abi.ExecRequest{Action: "read", Args: map[string]any{"path": "/etc/passwd"}, Settings: set})
	if err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
		t.Fatal("outside capabilities:", err)
	}
	if v, err := p.Verify(context.Background(), abi.VerifyRequest{Action: "read", Settings: set}); err != nil || v.Status != abi.Verified {
		t.Fatal(v, err)
	}
	_, err = p.Execute(context.Background(), abi.ExecRequest{Action: "slow", Settings: set})
	if err == nil || !strings.Contains(err.Error(), "time limit") {
		t.Fatal("timeout:", err)
	}
	p.EndCall("root") // процесс жив — уведомление уходит без ошибок
}

func TestCrashRestartAndDisable(t *testing.T) {
	p, _ := setup(t)
	set := map[string]any{"root": "~/notes"}
	read := func() error {
		_, err := p.Execute(context.Background(), abi.ExecRequest{Action: "read", Args: map[string]any{"path": "~/notes/a.md"}, Settings: set})
		return err
	}
	if _, err := p.Execute(context.Background(), abi.ExecRequest{Action: "crash", Settings: set}); err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Fatal("crash:", err)
	}
	// Следующий вызов запускает процесс заново.
	if err := read(); err != nil {
		t.Fatal("restart:", err)
	}
	if p.State() != StateRunning {
		t.Fatal(p.State())
	}
	for i := 0; i < MaxCrashes; i++ {
		p.Execute(context.Background(), abi.ExecRequest{Action: "crash", Settings: set})
		time.Sleep(50 * time.Millisecond) // процесс завершился и падение учтено
	}
	if err := read(); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal("crash loop must disable the connector:", err)
	}
	if p.State() != StateDisabled {
		t.Fatal(p.State())
	}
}

func TestBadBinaryAndManifest(t *testing.T) {
	p, _ := setup(t)
	p.bin = filepath.Join(t.TempDir(), "missing")
	if _, err := p.Execute(context.Background(), abi.ExecRequest{Action: "read", Settings: map[string]any{"root": "~"}}); err == nil {
		t.Fatal("missing binary")
	}
	q, _ := setup(t)
	q.man.Version = "2" // пакет говорит одно, процесс — другое
	if _, err := q.Execute(context.Background(), abi.ExecRequest{Action: "read", Settings: map[string]any{"root": "~"}}); err == nil || !strings.Contains(err.Error(), "another manifest") {
		t.Fatal("manifest mismatch:", err)
	}
}

// Вызов отменён (агент прервал CLI, закрылся HTTP-запрос): процесс узнаёт об
// этом и прерывает действие, а ответ — cancelled, не crashed.
func TestCancelReachesTheProcess(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "cancelled")
	t.Setenv("GATE4AI_TEST_MARK", mark)
	p, _ := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	_, err := p.Execute(ctx, abi.ExecRequest{Action: "wait", Settings: map[string]any{"root": "~/notes"}})
	if err == nil || !strings.Contains(err.Error(), abi.CodeCancelled) || strings.Contains(err.Error(), "crashed") {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if b, err := os.ReadFile(mark); err == nil {
			if !strings.Contains(string(b), "canceled") {
				t.Fatal(string(b))
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the process did not learn that the call was cancelled")
}

// Нативный процесс получает только системные переменные окружения, а не токены
// и ключи из окружения daemon.
func TestMinimalEnv(t *testing.T) {
	got := strings.Join(MinimalEnv([]string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "XDG_RUNTIME_DIR=/run/u", "DISPLAY=:0",
		"GATE4AI_TOKEN=t0k", "GATE4AI_HOME=/g", "GITHUB_TOKEN=gh", "OPENAI_API_KEY=sk", "AWS_SECRET_ACCESS_KEY=x", "LocalAppData=C:\\u"}), " ")
	if got != "PATH=/bin HOME=/h LC_ALL=C XDG_RUNTIME_DIR=/run/u DISPLAY=:0 GATE4AI_HOME=/g LocalAppData=C:\\u" {
		t.Fatal(got)
	}
}

// Строки stderr нативного процесса маскируются перед записью в лог daemon.
func TestStderrMasked(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	e, err := wasm.NewEngine("", wasm.Services{Log: log, Mask: func(s string) string { return strings.ReplaceAll(s, "fake-secret-123", "***") }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	m, _ := manifest.Parse([]byte(testManifest))
	p, err := New(os.Args[0], m, e, log)
	if err != nil {
		t.Fatal(err)
	}
	p.stderr(strings.NewReader("debug: password fake-secret-123\n"))
	if strings.Contains(buf.String(), "fake-secret-123") || !strings.Contains(buf.String(), "***") {
		t.Fatal(buf.String())
	}
}

func TestOwnerAndEvents(t *testing.T) {
	p, _ := setup(t)
	if err := p.Owner(context.Background(), "sessions", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatal("an owner request does not start the process:", err)
	}
	if _, err := p.Execute(context.Background(), abi.ExecRequest{Action: "read", Args: map[string]any{"path": "~/notes/a.md"}, Settings: map[string]any{"root": "~/notes"}}); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	p.SetEventSink(func(kind string, data json.RawMessage) { got <- kind + " " + string(data) })
	var out map[string]string
	if err := p.Owner(context.Background(), "view", map[string]string{"session": "s-1"}, &out); err != nil || out["op"] != "view" {
		t.Fatal(out, err)
	}
	select {
	case e := <-got:
		if e != `view {"session":"s-1"}` {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
}
