// SPDX-License-Identifier: FSL-1.1-ALv2

package wasm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/hostfuncs"
	"github.com/gate4ai/sdk-go/manifest"
)

var (
	probeOnce sync.Once
	probeWasm []byte
	probeMan  manifest.Manifest
	probeErr  error
)

func probe(t *testing.T) ([]byte, manifest.Manifest) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a WASM module")
	}
	probeOnce.Do(func() {
		dir, _ := os.MkdirTemp("", "g4aprobe")
		out := filepath.Join(dir, "probe.wasm")
		if probeErr = BuildGo(context.Background(), "testdata/probe", out); probeErr != nil {
			return
		}
		probeWasm, probeErr = os.ReadFile(out)
		y, _ := os.ReadFile("testdata/probe/manifest.yaml")
		probeMan, _ = manifest.Parse(y)
		os.RemoveAll(dir)
	})
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	return probeWasm, probeMan
}

type vault map[string]string

func (v vault) Value(a string) (string, error) {
	if s, ok := v[a]; ok {
		return s, nil
	}
	return "", errors.New("no such secret")
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

var engineCache = filepath.Join(os.TempDir(), "gate4ai-test-wasm-cache")

func load(t *testing.T, logs io.Writer) *Connector {
	t.Helper()
	bin, man := probe(t)
	e, err := NewEngine(engineCache, Services{Secrets: vault{"@probe": "s3cr3t-value"}, KV: func(n string) hostfuncs.KV { return KVDir(t.TempDir())(n) },
		Log: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.Load(context.Background(), bin, man)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func run(t *testing.T, c *Connector, action string, args map[string]any) (abi.ExecResult, error) {
	t.Helper()
	return c.Execute(context.Background(), abi.ExecRequest{Action: action, Args: args, Settings: map[string]any{"token": "@probe"}})
}

func TestCapabilityDeniedForEveryFunction(t *testing.T) {
	c := load(t, io.Discard)
	res, err := run(t, c, "deny_all", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Structured.(map[string]any)
	if len(got) != 10 {
		t.Fatal(got)
	}
	for fn, code := range got {
		if code != abi.CodeCapabilityDenied {
			t.Errorf("%s: %v", fn, code)
		}
	}
}

func TestSecretNeverReachesModule(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		io.WriteString(w, "you sent "+seen)
	}))
	defer srv.Close()
	logs := &logBuf{}
	c := load(t, logs)
	res, err := run(t, c, "http", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer s3cr3t-value" {
		t.Fatal("host did not substitute the secret:", seen)
	}
	body := res.Structured.(map[string]any)["body"].(string)
	if strings.Contains(body, "s3cr3t") || !strings.Contains(body, "[secret]") {
		t.Fatal("secret reached the module:", body)
	}
	if strings.Contains(logs.String(), "s3cr3t") || !strings.Contains(logs.String(), "connector=probe") {
		t.Fatal("log:", logs.String())
	}
	res, _ = run(t, c, "settings", nil)
	if res.Structured.(map[string]any)["token"] != "@probe" {
		t.Fatal("module must see only the alias", res.Structured)
	}
}

// stubIMAP — минимальный IMAP: приветствие, LOGIN (запоминает строку), ответ OK на любую команду.
func stubIMAP(t *testing.T) (port int, login <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ch := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		io.WriteString(c, "* OK stub ready\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag, cmd, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
			if strings.HasPrefix(cmd, "LOGIN ") {
				ch <- cmd
			}
			io.WriteString(c, tag+" OK "+strings.Fields(cmd)[0]+" done\r\n")
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, ch
}

func TestDialAuthenticatedByHost(t *testing.T) {
	port, login := stubIMAP(t)
	c := load(t, io.Discard)
	res, err := run(t, c, "imap", map[string]any{"port": float64(port)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-login:
		if got != `LOGIN "probe" "s3cr3t-value"` {
			t.Fatal("host login:", got)
		}
	default:
		t.Fatal("host did not log in")
	}
	out := res.Structured.(map[string]any)
	if !strings.HasPrefix(out["greeting"].(string), "* PREAUTH") || out["reply"] != "a1 OK NOOP done\r\n" {
		t.Fatal("module side:", out)
	}
	if strings.Contains(fmt.Sprint(out), "s3cr3t") {
		t.Fatal("secret reached the module:", out)
	}
}

func TestKVEvidenceAndVerify(t *testing.T) {
	c := load(t, io.Discard)
	res, err := run(t, c, "kv", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Structured.(map[string]any)
	if r["value"] != "1" || r["quota"] != abi.CodeLimit || len(res.Evidence) != 1 || string(res.Evidence[0].Data) != "1" {
		t.Fatal(res)
	}
	v, err := c.Verify(context.Background(), abi.VerifyRequest{Action: "kv", Settings: map[string]any{"token": "@probe"}})
	if err != nil || v.Status != abi.Verified {
		t.Fatal(v, err)
	}
}

func TestLimits(t *testing.T) {
	c := load(t, io.Discard)
	_, err := run(t, c, "spin", nil)
	var e *abi.Error
	if !errors.As(err, &e) || e.Code != abi.CodeLimit {
		t.Fatal("time limit:", err)
	}
	if _, err := run(t, c, "hog", nil); err == nil || !strings.Contains(err.Error(), "crashed") {
		t.Fatal("memory limit:", err)
	}
	if _, err := run(t, c, "crash", nil); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatal("panic:", err)
	}
	// После сбоев коннектор продолжает работать: каждый вызов — новый экземпляр.
	if _, err := run(t, c, "settings", nil); err != nil {
		t.Fatal(err)
	}
}

func TestManifestMustMatchPackage(t *testing.T) {
	bin, man := probe(t)
	e, _ := NewEngine("", Services{})
	man.Capabilities.Network = append(man.Capabilities.Network, manifest.NetworkCap{Host: "evil.example", Scheme: "https"})
	if _, err := e.Load(context.Background(), bin, man); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatal("package manifest with wider capabilities accepted:", err)
	}
	man.Runtime = manifest.RuntimeNative
	if _, err := e.Load(context.Background(), bin, man); err == nil {
		t.Fatal("non-wasm runtime accepted")
	}
	if _, err := e.Load(context.Background(), []byte("not wasm"), probeMan); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestLazyLoad(t *testing.T) {
	bin, man := probe(t)
	e, _ := NewEngine(engineCache, Services{Secrets: vault{"@probe": "x"}})
	l := e.LoadLazy(bin, man)
	defer l.Close()
	if l.Manifest().Connector != "probe" {
		t.Fatal("package manifest must be available at once")
	}
	res, err := l.Execute(context.Background(), abi.ExecRequest{Action: "settings", Settings: map[string]any{"token": "@probe"}})
	if err != nil || res.Structured.(map[string]any)["token"] != "@probe" {
		t.Fatal(res, err)
	}
	bad := man
	bad.Version = "9"
	lb := e.LoadLazy(bin, bad)
	if _, err := lb.Execute(context.Background(), abi.ExecRequest{Action: "settings"}); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatal("mismatched manifest must fail on call:", err)
	}
	lb.Close()
}
