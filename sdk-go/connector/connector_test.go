// SPDX-License-Identifier: Apache-2.0

package connector_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// echo — коннектор, который разговаривает с сервером через abi.Conn: читающая
// горутина и пишущая горутина одновременно, как go-imap.
type echo struct{}

func (echo) Execute(ctx context.Context, h *abi.Host, req abi.ExecRequest) (abi.ExecResult, error) {
	switch req.Action {
	case "boom":
		panic("boom")
	case "fail":
		return abi.ExecResult{}, errors.New("nope")
	}
	ps, _ := req.Settings["dial_port"].(string)
	if ps == "" {
		ps = req.Settings["port"].(string)
	}
	port, _ := strconv.Atoi(ps)
	c, err := h.Dial(abi.DialRequest{Host: "127.0.0.1", Port: port, TLS: abi.TLSNone})
	if err != nil {
		return abi.ExecResult{}, err
	}
	defer c.Close()
	lines := make(chan string)
	go func() {
		r := bufio.NewReader(c)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- strings.TrimSpace(l)
		}
	}()
	var out []string
	for _, w := range req.Args["words"].([]any) {
		io.WriteString(c, w.(string)+"\n")
		out = append(out, <-lines)
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
  network: [ { host: 127.0.0.1, port: "${setting:port}", scheme: tcp } ]
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
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				r := bufio.NewReader(c)
				for {
					s, err := r.ReadString('\n')
					if err != nil {
						c.Close()
						return
					}
					io.WriteString(c, strings.ToUpper(s))
				}
			}()
		}
	}()
	m, err := manifest.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	h := hosttest.New(m, map[string]any{"port": port}, nil)
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
	// Порт не из настроек — capability_denied.
	h.Settings = map[string]any{"port": port, "dial_port": "1"}
	if _, err := h.Execute(echo{}, "say", map[string]any{"words": []any{"a"}}); err == nil || !strings.Contains(err.Error(), "capability_denied") {
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
