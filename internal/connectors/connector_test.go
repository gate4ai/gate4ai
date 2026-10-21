// SPDX-License-Identifier: AGPL-3.0-only

package connectors

import (
	"context"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

type echo struct{}

func (echo) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "echo", Actions: []manifest.Action{
		{Name: "say", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { text: { type: string } } }`)},
	}}
}

func (echo) Execute(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Structured: map[string]any{"text": r.Args["text"], "n": 1}, ResultRef: "ref-1",
		Evidence: []abi.EvidenceItem{{Name: "e.txt", MIME: "text/plain", Data: []byte("evidence")}}}, nil
}

func (echo) Verify(ctx context.Context, r abi.VerifyRequest) (abi.VerifyResult, error) {
	if r.ResultRef != "ref-1" {
		return abi.VerifyResult{Status: abi.NotConfirmed, Method: "echo"}, nil
	}
	return abi.VerifyResult{Status: abi.Verified, Method: "echo"}, nil
}

type bad struct{ echo }

func (bad) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "bank", Actions: []manifest.Action{{Name: "pay", Class: "money"}}}
}

func TestInProcessAndRegistry(t *testing.T) {
	c, err := NewInProcess(echo{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	res, err := c.Execute(ctx, abi.ExecRequest{Action: "say", Args: map[string]any{"text": "hi"}})
	// Результат прошёл через JSON, как у WASM-модуля.
	if err != nil || res.Structured.(map[string]any)["text"] != "hi" || res.Structured.(map[string]any)["n"] != 1.0 || string(res.Evidence[0].Data) != "evidence" {
		t.Fatal(res, err)
	}
	if v, err := c.Verify(ctx, abi.VerifyRequest{Action: "say", ResultRef: res.ResultRef}); err != nil || v.Status != abi.Verified {
		t.Fatal(v, err)
	}
	if _, err := c.Execute(ctx, abi.ExecRequest{Action: "nope"}); err == nil {
		t.Fatal("unknown action")
	}
	if _, err := NewInProcess(bad{}); err == nil {
		t.Fatal("invalid manifest accepted")
	}
	r := NewRegistry()
	r.Add(c)
	if _, a, ok := r.Lookup("echo.say"); !ok || a.Name != "say" {
		t.Fatal("lookup")
	}
	if _, _, ok := r.Lookup("echo.nope"); ok {
		t.Fatal("unknown action found")
	}
	if cat := r.Catalog(); len(cat["echo.say"]) != 1 {
		t.Fatal(cat)
	}
	r.Remove("echo")
	if _, ok := r.Get("echo"); ok {
		t.Fatal("not removed")
	}
}
