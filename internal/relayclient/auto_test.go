// SPDX-License-Identifier: AGPL-3.0-only

package relayclient

import (
	"context"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

func newDaemon(t *testing.T, conf string) *daemon.Daemon {
	t.Helper()
	c, _ := connectors.NewInProcess(noop{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {a: {profile: p}}\nprofiles: {p: {connectors: [notes]}}\n" + conf)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

// stopAuto останавливает самостоятельную регистрацию и снимает подмену
// соединений: после отмены ctx и захвата registerMu новых соединений не будет.
func stopAuto(cancel context.CancelFunc) {
	cancel()
	registerMu.Lock()
	TestDialer = nil
	registerMu.Unlock()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 500 && !ok(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !ok() {
		t.Fatal("timeout: " + what)
	}
}

// Доступ из интернета включён по умолчанию: daemon без адреса сам регистрируется
// на relay из конфигурации (здесь — свой relay с его CA) и подключается.
// «Выключить» останавливает туннель и прячет публичный адрес, адрес устройства
// остаётся; «Включить» возвращает то же устройство без новой регистрации.
func TestAutoRegister(t *testing.T) {
	e := startRelay(t)
	TestDialer = e.dial
	defer func() { TestDialer = nil }()
	caFile := filepath.Join(t.TempDir(), "relay-ca.pem")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.srv.TLS.Certificates[0].Certificate[0]}), 0o600)
	d := newDaemon(t, "relay: {url: https://relay.test, ca_file: "+caFile+", self_signed: true}\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := Attach(ctx, d, true)
	if st := status(); st.Configured || (st.Phase != PhaseRegistering && st.Phase != PhaseIssuing && st.Phase != PhaseConnecting && st.Phase != PhaseOnline) {
		t.Fatalf("at start: %+v", st)
	}
	waitFor(t, "online", func() bool { return status().Online })
	st := status()
	id := d.Config.Current().Relay.DeviceID
	if id == "" || st.DeviceID != id || st.MCPURL != "https://"+id+".relay.test" || d.PublicURL() != st.MCPURL {
		t.Fatalf("%+v, config %+v", st, d.Config.Current().Relay)
	}
	if n := len(e.srv.Store.Devices()); n != 1 {
		t.Fatal("devices on the relay:", n)
	}
	// Включить ещё раз — не новое устройство.
	if res, err := EnsureRegistered(ctx, d, auth.OwnerVia("socket")); err != nil || res["device_id"] != id {
		t.Fatal(res, err)
	}
	if n := len(e.srv.Store.Devices()); n != 1 {
		t.Fatal("devices on the relay after EnsureRegistered:", n)
	}

	if err := SetDisabled(d, auth.AgentVia("a", "x"), true); !errors.Is(err, auth.ErrNotOwner) {
		t.Fatal("agent turned internet access off", err)
	}
	if err := SetDisabled(d, auth.OwnerVia("socket"), true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "offline", func() bool { return !e.srv.Online(id) })
	if st := status(); !st.Disabled || st.Online || st.Phase != "" || !st.Configured || st.MCPURL == "" || d.PublicURL() != "" {
		t.Fatalf("disabled: %+v, public %q", st, d.PublicURL())
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(e.srv.Store.Devices()); n != 1 {
		t.Fatal("registered while disabled:", n)
	}

	if _, err := EnsureRegistered(ctx, d, auth.OwnerVia("socket")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "online again", func() bool { return status().Online })
	if st := status(); st.Disabled || st.DeviceID != id || d.PublicURL() != "https://"+id+".relay.test" {
		t.Fatalf("re-enabled: %+v", st)
	}
	if n := len(e.srv.Store.Devices()); n != 1 {
		t.Fatal("devices on the relay after re-enable:", n)
	}
}

// Relay недоступен: регистрация не удалась — состояние failed с причиной и
// временем следующей попытки, попытки повторяются; запрет их прекращает.
func TestAutoRegisterRetried(t *testing.T) {
	old := RetryRegister
	RetryRegister = 50 * time.Millisecond
	defer func() { RetryRegister = old }()
	var dials atomic.Int32
	TestDialer = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("no route to relay")
	}
	d := newDaemon(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer stopAuto(cancel)
	status := Attach(ctx, d, true)
	waitFor(t, "two attempts", func() bool { return dials.Load() >= 2 && status().Phase == PhaseFailed })
	st := status()
	if st.Configured || st.Error == "" || st.NextRetry.IsZero() || st.NextRetry.After(time.Now().Add(RetryRegister)) || st.NextRetryAt() == "" {
		t.Fatalf("%+v", st)
	}
	if _, err := d.Config.Edit(auth.OwnerVia("socket"), "off", func(c *config.Config) error { c.Relay.Disabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "disabled", func() bool { return status().Disabled })
	n := dials.Load()
	time.Sleep(5 * RetryRegister)
	if dials.Load() != n || status().Phase != "" || status().Error != "" {
		t.Fatalf("attempts after turning off: %d → %d, %+v", n, dials.Load(), status())
	}
	// Снова включили — регистрация возобновляется.
	if _, err := d.Config.Edit(auth.OwnerVia("socket"), "on", func(c *config.Config) error { c.Relay.Disabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "attempts resumed", func() bool { return dials.Load() > n })
}

// Без самостоятельной регистрации (--relay-auto=false), с запретом в
// конфигурации и с прямым публичным адресом (--public) daemon на relay не ходит.
func TestAutoRegisterOff(t *testing.T) {
	var dials atomic.Int32
	TestDialer = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("no route to relay")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer stopAuto(cancel)
	cases := []struct {
		name, conf string
		auto       bool
		public     string
	}{{"flag", "", false, ""}, {"disabled", "relay: {disabled: true}\n", true, ""}, {"public", "", true, "https://gate.example.com"}}
	for _, c := range cases {
		d := newDaemon(t, c.conf)
		if c.public != "" {
			d.AddPublicURL(func() string { return c.public })
		}
		st := Attach(ctx, d, c.auto)()
		if st.Configured || st.Phase != "" || st.Disabled != (c.conf != "") {
			t.Fatalf("%s: %+v", c.name, st)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if dials.Load() != 0 {
		t.Fatal("registered on the relay:", dials.Load())
	}
}
