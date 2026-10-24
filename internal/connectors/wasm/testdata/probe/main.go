// SPDX-License-Identifier: FSL-1.1-ALv2

// Тестовый коннектор для проверки WASM-хоста: пытается выйти за возможности,
// упирается в лимиты, смотрит, что видит модуль.
package main

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
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
		_, err := h.FSRead("/etc/passwd", 0)
		out["fs_read"] = code(err)
		_, err = h.FSWrite("/tmp/probe", []byte("x"))
		out["fs_write"] = code(err)
		_, err = h.FSAppend("/tmp/probe", []byte("x"))
		out["fs_append"] = code(err)
		_, err = h.FSList("/")
		out["fs_list"] = code(err)
		_, err = h.FSStat("/etc/passwd")
		out["fs_stat"] = code(err)
		_, err = h.ServiceCall("browser", "open", map[string]any{"url": "https://example.com/"}, nil)
		out["service_call"] = code(err)
		return abi.ExecResult{Structured: out}, nil
	case "settings":
		return abi.ExecResult{Structured: r.Settings}, nil
	case "kv":
		if err := h.KVSet("n", []byte("1")); err != nil {
			return abi.ExecResult{}, err
		}
		v, _, err := h.KVGet("n")
		big := h.KVSet("big", make([]byte, 2<<20))
		_ = h.Evidence("kv.txt", "text/plain", v)
		return abi.ExecResult{Structured: map[string]any{"value": string(v), "quota": code(big)}}, err
	case "spin":
		for {
		}
	case "sleep": // пауза в коннекторе (ленивые списки сайтов): time.Sleep через WASI
		start := time.Now()
		time.Sleep(1200 * time.Millisecond)
		return abi.ExecResult{Structured: map[string]any{"slept_ms": time.Since(start).Milliseconds()}}, nil
	case "hog":
		// Сразу больше limits.memory_mb одним куском: выход за предел памяти
		// не зависит от скорости машины (мелкими кусками под -race раньше
		// наступит срок вызова).
		for {
			sink = append(sink, make([]byte, 256<<20))
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
