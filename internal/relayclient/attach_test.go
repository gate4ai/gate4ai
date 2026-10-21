// SPDX-License-Identifier: AGPL-3.0-only

package relayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

type noop struct{}

func (noop) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "notes", Actions: []manifest.Action{{Name: "read", Class: "read"}}}
}
func (noop) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Structured: "ok"}, nil
}
func (noop) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func TestDaemonThroughRelay(t *testing.T) {
	e := startRelay(t)
	TestDialer = e.dial
	defer func() { TestDialer = nil }()
	home, _ := os.MkdirTemp("", "g4arelay")
	defer os.RemoveAll(home)
	// CA relay для self-hosted варианта.
	cert := e.srv.TLS.Certificates[0]
	caFile := filepath.Join(home, "relay-ca.pem")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600)

	c, _ := connectors.NewInProcess(noop{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {a: {profile: p}}\nprofiles: {p: {connectors: [notes]}}\nretention: {checkpoint_each: 2}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := Attach(ctx, d)
	if st := status(); st.Configured || st.Phase != "" {
		t.Fatalf("before registration: %+v", st)
	}
	if _, err := RegisterDevice(ctx, d, auth.AgentVia("a", "x"), "https://relay.test", caFile, true); err != auth.ErrNotOwner {
		t.Fatal("agent registered relay", err)
	}
	res, err := RegisterDevice(ctx, d, auth.OwnerVia("socket"), "https://relay.test", caFile, true)
	if err != nil {
		t.Fatal(err)
	}
	id := res["device_id"].(string)
	if d.Config.Current().Relay.DeviceID != id || d.Config.Current().Relay.Token != "@relay.token" {
		t.Fatal("config not updated")
	}
	// Сразу после регистрации — этап подключения, а не «нет связи».
	if st := status(); st.Phase != PhaseIssuing && st.Phase != PhaseConnecting && st.Phase != PhaseOnline {
		t.Fatalf("after registration: %+v", st)
	}
	for i := 0; i < 300 && !e.srv.Online(id); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !e.srv.Online(id) {
		t.Fatal("device not online")
	}
	for i := 0; i < 100 && status().Phase != PhaseOnline; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if st := status(); st.Phase != PhaseOnline || !st.Online || st.Error != "" {
		t.Fatalf("online: %+v", st)
	}
	// Relay оборвал туннель: этап «переподключается» с причиной, затем снова онлайн.
	e.srv.Disconnect(id)
	for i := 0; i < 300 && status().Phase != PhaseReconnecting; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if st := status(); st.Phase != PhaseReconnecting || st.Online || st.Error == "" {
		t.Fatalf("reconnecting: %+v", st)
	}
	for i := 0; i < 300 && status().Phase != PhaseOnline; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if st := status(); st.Phase != PhaseOnline {
		t.Fatalf("after reconnect: %+v", st)
	}
	// Облачный клиент видит OAuth-метаданные устройства через relay (TLS до устройства).
	cl := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: id + ".relay.test",
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				c, _ := x509.ParseCertificate(raw[0])
				if c.Subject.CommonName != id+".relay.test" {
					t.Errorf("relay terminated TLS: %s", c.Subject.CommonName)
				}
				return nil
			}},
		DialContext: func(ctx context.Context, n, a string) (net.Conn, error) { return e.dial(ctx, n, a) }}}
	resp, err := cl.Get("https://" + id + ".relay.test/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	json.NewDecoder(resp.Body).Decode(&meta)
	if meta["issuer"] != "https://"+id+".relay.test" {
		t.Fatal(meta)
	}
	// Свидетель: чекпоинт журнала уходит на relay.
	d.Journal.Append(sampleReceipt())
	d.Journal.Append(sampleReceipt())
	for i := 0; i < 100 && len(e.srv.Store.Checkpoints(id)) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cps := e.srv.Store.Checkpoints(id)
	if len(cps) != 1 || cps[0].Seq != 2 {
		t.Fatal(cps)
	}
}

func sampleReceipt() *audit.Receipt {
	return &audit.Receipt{Agent: "a", Action: "notes.read", Class: "read", Decision: audit.Decision{Effect: "allow"}, Execution: audit.Execution{Status: "ok"}}
}

// Устройство зарегистрировано, но токена нет в Vault: этап failed с причиной,
// а не вечное «переподключается».
func TestStatusFailedWithoutToken(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4arelay")
	defer os.RemoveAll(home)
	c, _ := connectors.NewInProcess(noop{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {a: {profile: p}}\nprofiles: {p: {connectors: [notes]}}\nrelay: {url: https://relay.test, device_id: abc, token: '@relay.token'}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := Attach(ctx, d)()
	if !st.Configured || st.Online || st.Phase != PhaseFailed || st.Error == "" {
		t.Fatalf("%+v", st)
	}
}

// Сертификат устройства не выпускается: relay не отключается до перезапуска —
// этап остаётся issuing, причина видна в статусе, выпуск повторяется.
func TestCertFailureRetried(t *testing.T) {
	old := RetryCert
	RetryCert = 50 * time.Millisecond
	defer func() { RetryCert = old }()
	var hits atomic.Int32
	ca := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) })) // ACME CA без каталога
	defer ca.Close()
	c, _ := connectors.NewInProcess(noop{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {a: {profile: p}}\nprofiles: {p: {connectors: [notes]}}\nrelay: {url: 'https://relay.test', device_id: abc, token: '@relay.token', acme_ca: '" + ca.URL + "/directory'}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Vault.Set("relay.token", "dev_x"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := Attach(ctx, d)
	for i := 0; i < 500 && (status().Error == "" || hits.Load() < 2); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if st := status(); st.Online || st.Phase != PhaseIssuing || st.Error == "" || hits.Load() < 2 {
		t.Fatalf("%+v, CA requests %d", st, hits.Load())
	}
}
