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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/sdk-go/manifest"
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
	Attach(ctx, d)
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
	for i := 0; i < 300 && !e.srv.Online(id); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !e.srv.Online(id) {
		t.Fatal("device not online")
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
