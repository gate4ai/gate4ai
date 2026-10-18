// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

func TestConnectorLogsHandler(t *testing.T) {
	var out bytes.Buffer
	logs := &ConnectorLogs{}
	log := slog.New(logs.Handler(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})))
	log.Debug("parsed bill", "connector", "site", "rows", 3)
	log.With("connector", "site").Warn("no table")
	log.Info("daemon started")
	lines := logs.Lines("site", time.Time{}, 0)
	if len(lines) != 2 || lines[0].Level != "debug" || lines[0].Msg != "parsed bill rows=3" || lines[1].Msg != "no table" {
		t.Fatalf("%+v", lines)
	}
	// Отладочная строка коннектора хранится, но в лог daemon (уровень info) не пишется.
	if strings.Contains(out.String(), "parsed bill") || !strings.Contains(out.String(), "no table") || !strings.Contains(out.String(), "daemon started") {
		t.Fatal(out.String())
	}
	if l := logs.Lines("site", time.Now().Add(time.Hour), 0); len(l) != 0 {
		t.Fatal("since", l)
	}
	for i := 0; i < MaxConnectorLog+10; i++ {
		logs.Add("x", LogLine{Time: time.Now(), Msg: "m"})
	}
	if n := len(logs.Lines("x", time.Time{}, 0)); n != MaxConnectorLog {
		t.Fatal(n)
	}
	if n := len(logs.Lines("x", time.Time{}, 5)); n != 5 {
		t.Fatal(n)
	}
}

// chatty — коннектор, который пишет в лог daemon, как WASM-хост пишет log() коннектора.
type chatty struct {
	name string
	log  func() *slog.Logger
}

func (c chatty) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: c.name, Actions: []manifest.Action{{Name: "read", Class: "read"}}}
}
func (c chatty) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	c.log().Info("selector #debt not found, using the table", "connector", c.name)
	return abi.ExecResult{}, abi.Errorf(abi.CodeNotFound, "no debt on the page")
}
func (chatty) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func TestConnectorLogInResultAndAPI(t *testing.T) {
	home := t.TempDir()
	var d *Daemon
	logger := func() *slog.Logger { return d.Log }
	dev, _ := connectors.NewInProcess(chatty{name: "site", log: logger})
	shop, _ := connectors.NewInProcess(chatty{name: "shop", log: logger})
	var err error
	d, err = New(Options{Home: filepath.Join(home, "data"), Keys: keys.NewMemory(), Connectors: []connectors.Client{dev, shop},
		InitialConfig: []byte("agents:\n  bot: { profile: p }\nprofiles:\n  p:\n    connectors: [site, shop]\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// site — своя сборка (--dev), shop — из магазина.
	for name, trust := range map[string]string{"site": store.TrustLocal, "shop": "verified"} {
		os.MkdirAll(filepath.Join(d.ConnectorsDir, name), 0o700)
		b, _ := json.Marshal(store.Info{Connector: name, Trust: trust})
		os.WriteFile(filepath.Join(d.ConnectorsDir, name, store.InfoFile), b, 0o600)
	}
	res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "site.read"})
	if res.Status != pipeline.StatusError || len(res.Log) != 1 || res.Log[0] != "info: selector #debt not found, using the table" {
		t.Fatalf("own build: the agent sees the connector's log: %+v", res)
	}
	if res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "shop.read"}); len(res.Log) != 0 {
		t.Fatalf("store connector: the log is the owner's diagnostics: %+v", res)
	}

	ownerSock, agentSock := filepath.Join(home, "run", "o.sock"), filepath.Join(home, "run", "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, ownerSock, agentSock)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(agentSock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner := client{c: ipc.HTTPClient(ownerSock)}
	var out struct {
		Lines []LogLine `json:"lines"`
	}
	if code := owner.do(t, "GET", "/v1/connectors/logs?name=shop&since=1h", nil, &out); code != 200 || len(out.Lines) != 1 {
		t.Fatal(code, out)
	}
	if code := owner.do(t, "GET", "/v1/connectors/logs", nil, nil); code != 400 {
		t.Fatal("name is required", code)
	}
	var tok struct{ Token string }
	owner.do(t, "POST", "/v1/agents/bot/token", nil, &tok)
	agent := client{c: ipc.HTTPClient(agentSock), token: tok.Token}
	if code := agent.do(t, "GET", "/v1/connectors/logs?name=shop", nil, nil); code == 200 {
		t.Fatal("logs are for the owner", code)
	}
}
