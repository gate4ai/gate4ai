// SPDX-License-Identifier: Apache-2.0

package connector_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// echo — коннектор, который ходит в функции хоста: хранилище (kv) и
// доказательства. Слова накапливаются в kv под ключом из настроек.
type echo struct{}

func (echo) Execute(ctx context.Context, h *abi.Host, req abi.ExecRequest) (abi.ExecResult, error) {
	switch req.Action {
	case "boom":
		panic("boom")
	case "fail":
		return abi.ExecResult{}, errors.New("nope")
	}
	key, _ := req.Settings["key"].(string)
	var out []string
	for _, w := range req.Args["words"].([]any) {
		prev, _, err := h.KVGet(key)
		if err != nil {
			return abi.ExecResult{}, err
		}
		up := strings.ToUpper(w.(string))
		if err := h.KVSet(key, append(append(prev, up...), '\n')); err != nil {
			return abi.ExecResult{}, err
		}
		out = append(out, up)
	}
	_ = h.Evidence("log.txt", "text/plain", []byte(strings.Join(out, ",")))
	return abi.ExecResult{Structured: map[string]any{"echo": out}, Content: []abi.Content{abi.Text(strings.Join(out, " "))}}, nil
}

func (echo) Verify(context.Context, *abi.Host, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified, Method: "none"}, nil
}

const yaml = `
connector: echo
capabilities:
  storage: { quota_mb: 1 }
actions:
  - name: say
    class: read
    input_schema:
      type: object
      properties: { words: { type: array, items: { type: string } } }
      required: [words]
    output_schema:
      type: object
      properties: { echo: { type: array, items: { type: string } } }
      required: [echo]
  - { name: boom, class: read }
  - { name: fail, class: read }
`

func TestConnectorOnTestHost(t *testing.T) {
	m, err := manifest.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	h := hosttest.New(m, map[string]any{"key": "words"}, nil)
	res, err := h.Execute(echo{}, "say", map[string]any{"words": []any{"a", "bb", "ccc"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Structured.(map[string]any)["echo"].([]any); len(got) != 3 || got[2] != "CCC" || res.Content[0].Text != "A BB CCC" {
		t.Fatal(got)
	}
	if len(res.Evidence) != 1 || string(res.Evidence[0].Data) != "A,BB,CCC" {
		t.Fatal(res.Evidence)
	}
	if v, ok, _ := h.KV.Get("words"); !ok || string(v) != "A\nBB\nCCC\n" {
		t.Fatalf("kv: %q %v", v, ok)
	}
	// Хранилище не объявлено — capability_denied.
	m2, _ := manifest.Parse([]byte(strings.Replace(yaml, "storage: { quota_mb: 1 }", "{}", 1)))
	h2 := hosttest.New(m2, map[string]any{"key": "words"}, nil)
	if _, err := h2.Execute(echo{}, "say", map[string]any{"words": []any{"a"}}); err == nil || !strings.Contains(err.Error(), "capability_denied") {
		t.Fatal(err)
	}
	if _, err := h.Execute(echo{}, "boom", nil); err == nil || !strings.Contains(err.Error(), "panic: boom") {
		t.Fatal(err)
	}
	if _, err := h.Execute(echo{}, "fail", nil); err == nil || !strings.Contains(err.Error(), "connector_error: nope") {
		t.Fatal(err)
	}
	out := connector.Handle(m, echo{}, nil, connector.OpExecute, []byte(`{"action":"rm"}`))
	if err := abi.Decode(out, nil); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatal(err)
	}
	var mm manifest.Manifest
	if err := abi.Decode(connector.Handle(m, echo{}, nil, connector.OpManifest, nil), &mm); err != nil || !manifest.Same(m, mm) {
		t.Fatal("manifest roundtrip", err)
	}
}
