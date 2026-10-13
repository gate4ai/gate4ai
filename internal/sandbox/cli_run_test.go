// SPDX-License-Identifier: FSL-1.1-ALv2

package sandbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/sandbox"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/sdk-go/manifest"
)

type files struct{}

func (files) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "files", Actions: []manifest.Action{{Name: "read", Class: "read", Description: "Read"}}}
}
func (files) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{}, nil
}
func (files) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func TestCLIRun(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4arun")
	defer os.RemoveAll(home)
	c, _ := connectors.NewInProcess(files{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {claude-code: {profile: dev}}\nprofiles: {dev: {connectors: [files]}}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := sandbox.Attach(ctx, d, filepath.Join(home, "run"), "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	sock, asock := filepath.Join(home, "o.sock"), filepath.Join(home, "a.sock")
	go d.Serve(ctx, sock, asock)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(asock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ws := filepath.Join(home, "ws")
	os.MkdirAll(ws, 0o755)
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" },
			Connect: func(string) *cli.Client { return cli.NewSocketClient(sock, "") }}
		return cli.Run(ctx, env, args), out.String()
	}
	code, out := run("run", "claude-code", "--workspace", ws, "--dry-run")
	if code != 0 || !strings.Contains(out, `"srt"`) || strings.Contains(out, "g4a_") {
		t.Fatal(code, out)
	}
	var captured []string
	var capturedEnv []string
	cli.Exec = func(ctx context.Context, e *cli.Env, argv, env []string) (int, error) {
		captured, capturedEnv = argv, env
		return 7, nil
	}
	os.Setenv("SOME_API_KEY", "leak")
	defer os.Unsetenv("SOME_API_KEY")
	code, out = run("run", "claude-code", "--workspace", ws, "--container", "--", "bash", "-l")
	if code != 7 || captured[0] != "docker" || captured[len(captured)-1] != "-l" {
		t.Fatal(code, out, captured)
	}
	for _, kv := range capturedEnv {
		if strings.HasPrefix(kv, "SOME_API_KEY=") {
			t.Fatal("host secrets passed to the agent")
		}
	}
	if b, err := os.ReadFile(filepath.Join(ws, ".claude", "skills", "gate4ai", "SKILL.md")); err != nil || !strings.Contains(string(b), "files.read") {
		t.Fatal("skill not written", err)
	}
	var m map[string]any
	b, _ := os.ReadFile(filepath.Join(ws, ".mcp.json"))
	if json.Unmarshal(b, &m) != nil {
		t.Fatal("mcp config")
	}
	// После завершения запуска токен отозван.
	toks, _ := d.State.Tokens()
	for _, tk := range toks {
		if strings.HasPrefix(tk.Label, "run:") && !tk.Revoked {
			t.Fatal("run token not revoked", tk)
		}
	}
	// doctor работает и видит daemon.
	code, out = run("doctor")
	if !strings.Contains(out, `"daemon"`) || !strings.Contains(out, "daemon reachable") {
		t.Fatal(code, out)
	}
}
