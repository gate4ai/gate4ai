// SPDX-License-Identifier: FSL-1.1-ALv2

// Тестовый коннектор для проверки WASM-хоста: пытается выйти за возможности,
// упирается в лимиты, смотрит, что видит модуль.
package main

import (
	"context"
	_ "embed"
	"errors"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/connector"
)

//go:embed manifest.yaml
var manifestYAML []byte

type probe struct{}

func code(err error) string {
	var e *abi.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err == nil {
		return "ok"
	}
	return err.Error()
}

var sink [][]byte

func (probe) Execute(ctx context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	switch r.Action {
	case "deny_all":
		out := map[string]string{}
		_, err := h.HTTP(abi.HTTPRequest{URL: "https://evil.example/"})
		out["http_request:host"] = code(err)
		_, err = h.HTTP(abi.HTTPRequest{URL: "http://127.0.0.1:1/", Headers: map[string]string{"X": "{{secret:@other}}"}})
		out["http_request:secret"] = code(err)
		_, err = h.Dial(abi.DialRequest{Host: "evil.example", Port: 993, TLS: abi.TLSDirect})
		out["tls_dial"] = code(err)
		_, err = h.FSRead("/etc/passwd", 0)
		out["fs_read"] = code(err)
		_, err = h.FSWrite("/tmp/probe", []byte("x"))
		out["fs_write"] = code(err)
		_, err = h.FSAppend("/tmp/probe", []byte("x"))
		out["fs_append"] = code(err)
		_, err = h.FSList("/")
		out["fs_list"] = code(err)
		_, err = h.FSStat("/etc/passwd")
		out["fs_stat"] = code(err)
		_, err = h.BrowserOpen("https://example.com/")
		out["browser_open"] = code(err)
		_, err = h.BrowserObserve(0)
		out["browser_observe"] = code(err)
		return abi.ExecResult{Result: out}, nil
	case "http":
		resp, err := h.HTTP(abi.HTTPRequest{URL: r.Args["url"].(string), Headers: map[string]string{"Authorization": "Bearer {{secret:" + r.Settings["token"].(string) + "}}"}})
		if err != nil {
			return abi.ExecResult{}, err
		}
		h.Log("info", "got "+string(resp.Body))
		return abi.ExecResult{Result: map[string]any{"status": resp.Status, "body": string(resp.Body)}}, nil
	case "settings":
		return abi.ExecResult{Result: r.Settings}, nil
	case "kv":
		if err := h.KVSet("n", []byte("1")); err != nil {
			return abi.ExecResult{}, err
		}
		v, _, err := h.KVGet("n")
		big := h.KVSet("big", make([]byte, 2<<20))
		_ = h.Evidence("kv.txt", "text/plain", v)
		return abi.ExecResult{Result: map[string]any{"value": string(v), "quota": code(big)}}, err
	case "spin":
		for {
		}
	case "hog":
		for {
			sink = append(sink, make([]byte, 8<<20))
		}
	case "crash":
		var m map[string]int
		m["x"] = 1
	}
	return abi.ExecResult{}, nil
}

func (probe) Verify(context.Context, *abi.Host, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified, Method: "probe"}, nil
}

func init() { connector.Register(manifestYAML, probe{}) }

func main() {}
