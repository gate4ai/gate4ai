// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Выключатель рядом с адресом: `gate4ai relay off` записывает запрет (адрес
// устройства сохраняется), `relay status` показывает его, `relay on` снимает
// запрет без новой регистрации.
func TestRelayOffOn(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "o.sock")
	c, _ := connectors.NewInProcess(mailFake{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(dir, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte(policy + "\nrelay: {url: https://relay.test, device_id: abc, token: \"@relay\"}\n")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); d.Close() })
	relayclient.Attach(ctx, d, false)
	go d.Serve(ctx, sock, filepath.Join(dir, "a.sock"))
	waitSocket(t, sock)

	e, out, _ := envAt(sock, false)
	if code := Run(ctx, e, []string{"relay", "off"}); code != ExitOK || jsonOf(t, out.String())["disabled"] != true {
		t.Fatalf("off: %d %s", code, out)
	}
	if st := d.Config.Current().Relay; !st.Disabled || st.DeviceID != "abc" {
		t.Fatalf("config after off: %+v", st)
	}
	e, out, _ = envAt(sock, false)
	if code := Run(ctx, e, []string{"relay", "status"}); code != ExitOK || jsonOf(t, out.String())["disabled"] != true {
		t.Fatalf("status: %d %s", code, out)
	}
	e, out, _ = envAt(sock, false)
	if code := Run(ctx, e, []string{"relay", "on"}); code != ExitOK || jsonOf(t, out.String())["mcp_url"] != "https://abc.relay.test" {
		t.Fatalf("on: %d %s", code, out)
	}
	if st := d.Config.Current().Relay; st.Disabled || st.DeviceID != "abc" {
		t.Fatalf("config after on: %+v", st)
	}
	e, out, _ = envAt(sock, false)
	if code := Run(ctx, e, []string{"relay", "status"}); code != ExitOK || jsonOf(t, out.String())["disabled"] != nil {
		t.Fatalf("status after on: %d %s", code, out)
	}
	// Агент (токен) выключить доступ не может.
	e, out, _ = envAt(sock, false)
	e.Connect = func(string) *Client { return NewSocketClient(sock, "nope") }
	if code := Run(ctx, e, []string{"relay", "off"}); code == ExitOK {
		t.Fatalf("agent turned internet access off: %s", out)
	}
	e, _, errb := envAt(sock, true)
	if code := Run(ctx, e, []string{"relay", "nope"}); code != ExitUsage || !strings.Contains(errb.String(), "status | off | on | register") {
		t.Fatalf("usage: %d %s", code, errb)
	}
}

// Рядом с адресом из интернета — что видит relay и как выключить: один и тот
// же текст в выводе установщика (`gate4ai ui --print`) и в документах для агента.
func TestUIPrintRelayNote(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "o.sock")
	c, _ := connectors.NewInProcess(mailFake{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(dir, "d"), InitialConfig: []byte(policy), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}})
	if err != nil {
		t.Fatal(err)
	}
	d.AddRoutes(fakeUIRoutes)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); d.Close(); fakeUIRemote.Store("") })
	go d.Serve(ctx, sock, filepath.Join(dir, "a.sock"))
	waitSocket(t, sock)
	print := func(remote string) string {
		fakeUIRemote.Store(remote)
		e, out, _ := envAt(sock, true)
		e.StdoutTTY = true
		if code := Run(ctx, e, []string{"ui", "--print", "--wait", "0"}); code != ExitOK {
			t.Fatalf("%q: %d %s", remote, code, out)
		}
		return out.String()
	}
	for _, remote := range []string{`"remote_phase":"online","remote_url":"https://abc.relay.test/ui/","remote_cert":"ok"`, `"remote_phase":"issuing"`, `"remote_phase":"failed","remote_error":"relay: 503"`} {
		if out := print(remote); !strings.Contains(out, "From the internet: ") || !strings.Contains(out, "\n  "+RelayNote+"\n") {
			t.Fatalf("%q: no relay note: %s", remote, out)
		}
	}
	if out := print(`"remote_disabled":true`); !strings.Contains(out, "From the internet: off (gate4ai relay on, or Settings → Internet access)") || strings.Contains(out, "The relay sees") {
		t.Fatal(out)
	}
	if out := print(""); strings.Contains(out, "From the internet") || strings.Contains(out, "The relay sees") {
		t.Fatal(out)
	}
	for _, want := range []string{"gate4ai relay off", "GATE4AI_NO_RELAY=1", "gate4ai relay on", "encrypted traffic", "random device id"} {
		if !strings.Contains(RelayNote, want) {
			t.Errorf("relay note: no %q", want)
		}
	}
}

// waitSocket ждёт, пока daemon начнёт слушать сокет владельца.
func waitSocket(t *testing.T, sock string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ipc.Alive(sock) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start on " + sock)
}
