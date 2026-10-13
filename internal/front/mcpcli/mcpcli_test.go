// SPDX-License-Identifier: FSL-1.1-ALv2

package mcpcli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/mcp"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/sdk-go/manifest"
)

type files struct{}

func (files) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "files", Actions: []manifest.Action{
		{Name: "read", Class: "read", Description: "Read a file", Args: []manifest.Arg{{Name: "path", Type: "string", Required: true}}}}}
}
func (files) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Result: map[string]any{"content": "hello from " + r.Args["path"].(string)}}, nil
}
func (files) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func TestStdioMCPAgainstDaemon(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4amcp")
	defer os.RemoveAll(home)
	c, _ := connectors.NewInProcess(files{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {cc: {profile: p}}\nprofiles: {p: {connectors: [files], rules: [{id: no-etc, action: files.read, when: 'glob(path, \"/etc/**\")', effect: deny, reason: system files}]}}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	os1, as := filepath.Join(home, "o"), filepath.Join(home, "a")
	go d.Serve(ctx, os1, as)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(as); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tok, _ := d.State.IssueToken("cc", "", "")
	_ = auth.Owner

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := mcp.NewServer(ClientBackend{C: cli.NewSocketClient(as, tok)})
	go srv.ServeStdio(ctx, inR, outW)
	sc := bufio.NewScanner(outR)
	send := func(id int, method string, params any) map[string]any {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		inW.Write(append(b, '\n'))
		if !sc.Scan() {
			t.Fatal("no response")
		}
		var m map[string]any
		json.Unmarshal(sc.Bytes(), &m)
		return m["result"].(map[string]any)
	}
	send(1, "initialize", map[string]any{"protocolVersion": mcp.ProtocolVersion})
	tools := send(2, "tools/list", map[string]any{})["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "files_read" {
		t.Fatal(tools)
	}
	r := send(3, "tools/call", map[string]any{"name": "files_read", "arguments": map[string]any{"path": "/home/x/a.md"}})
	if r["isError"] != false || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), "hello from /home/x/a.md") {
		t.Fatal(r)
	}
	r = send(4, "tools/call", map[string]any{"name": "files_read", "arguments": map[string]any{"path": "/etc/shadow"}})
	if r["isError"] != true || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), "denied_by_policy: system files") {
		t.Fatal(r)
	}
}
