// SPDX-License-Identifier: FSL-1.1-ALv2

package wasm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/hosttest/mailtest"
	"github.com/gate4ai/sdk-go/manifest"
)

// buildConnector собирает коннектор из исходников монорепозитория
// (connectors/<имя>); после разделения репозиториев тест пропускается.
func buildConnector(t *testing.T, name string) ([]byte, manifest.Manifest) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a WASM module")
	}
	src := filepath.Join("..", "..", "..", "connectors", name)
	if _, err := os.Stat(filepath.Join(src, "manifest.yaml")); err != nil {
		t.Skip("connector sources not found:", src)
	}
	out := filepath.Join(t.TempDir(), name+".wasm")
	if err := BuildGo(context.Background(), src, out); err != nil {
		t.Fatal(err)
	}
	bin, _ := os.ReadFile(out)
	y, _ := os.ReadFile(filepath.Join(src, "manifest.yaml"))
	m, err := manifest.Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	return bin, m
}

func TestMailConnectorInWasm(t *testing.T) {
	bin, man := buildConnector(t, "mail")
	srv, err := mailtest.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	e, _ := NewEngine(engineCache, Services{Secrets: vault(srv.Secrets())})
	c, err := e.Load(context.Background(), bin, man)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	set := srv.Settings("me@example.com")
	ctx := context.Background()
	args := map[string]any{"to": []any{"landlord@example.com"}, "cc": []any{}, "subject": "Аренда", "body": "Оплата отправлена.", "in_reply_to": ""}
	res, err := c.Execute(ctx, abi.ExecRequest{Action: "send", Args: args, Settings: set})
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.Received) != 1 || !strings.Contains(string(srv.Received[0].Data), res.ResultRef) {
		t.Fatal("not sent", srv.Received)
	}
	v, err := c.Verify(ctx, abi.VerifyRequest{Action: "send", Args: args, Settings: set, ResultRef: res.ResultRef})
	if err != nil || v.Status != abi.Verified {
		t.Fatal(v, err)
	}
	l, err := c.Execute(ctx, abi.ExecRequest{Action: "list", Args: map[string]any{"folder": "Sent", "limit": 5.0}, Settings: set})
	if err != nil || len(l.Result.(map[string]any)["messages"].([]any)) != 1 {
		t.Fatal(l, err)
	}
}

func TestFilesConnectorInWasm(t *testing.T) {
	bin, man := buildConnector(t, "files")
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "notes"), 0o755)
	e, _ := NewEngine(engineCache, Services{Home: home})
	c, err := e.Load(context.Background(), bin, man)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	set := map[string]any{"roots": []any{"~/notes"}}
	args := map[string]any{"path": "~/notes/log.md", "text": "- done"}
	res, err := c.Execute(context.Background(), abi.ExecRequest{Action: "append", Args: args, Settings: set})
	if err != nil || len(res.Evidence) != 1 {
		t.Fatal(res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "notes", "log.md")); string(b) != "- done" {
		t.Fatal(string(b))
	}
	if _, err := c.Execute(context.Background(), abi.ExecRequest{Action: "read", Args: map[string]any{"path": "/etc/passwd"}, Settings: set}); err == nil || !strings.Contains(err.Error(), abi.CodeCapabilityDenied) {
		t.Fatal(err)
	}
}
