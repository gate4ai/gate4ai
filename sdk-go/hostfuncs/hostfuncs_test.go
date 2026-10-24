// SPDX-License-Identifier: Apache-2.0

package hostfuncs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/capability"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

type secrets map[string]string

func (s secrets) Value(a string) (string, error) {
	if v, ok := s[a]; ok {
		return v, nil
	}
	return "", errors.New("missing")
}

func dispatcher(t *testing.T, yaml string, settings map[string]any) *Dispatcher {
	t.Helper()
	m, err := manifest.Parse([]byte("connector: x\n" + yaml))
	if err != nil {
		t.Fatal(err)
	}
	c, err := capability.New(m, settings, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{Check: c, Secrets: secrets{"@api": "tok-SECRET-123", "@mail": "pässword \"q\""}, Timeout: 5 * time.Second}
	t.Cleanup(d.Close)
	return d
}

func call(t *testing.T, d *Dispatcher, fn string, req, out any) error {
	t.Helper()
	b, _ := json.Marshal(req)
	return abi.Decode(d.Call(fn, b), out)
}

func code(err error) string {
	var e *abi.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestFSAndKV(t *testing.T) {
	root := t.TempDir()
	d := dispatcher(t, `capabilities: { files: ["${setting:root}/**"], storage: { quota_mb: 1 } }`, map[string]any{"root": root})
	p := filepath.Join(root, "a.md")
	var w abi.FSWriteResponse
	if err := call(t, d, abi.FnFSAppend, abi.FSRequest{Path: p, Data: []byte("one\n")}, &w); err != nil || w.SizeBefore != -1 || w.SizeAfter != 4 {
		t.Fatal(w, err)
	}
	if err := call(t, d, abi.FnFSWrite, abi.FSRequest{Path: p, Data: []byte("two")}, &w); err != nil || w.SizeAfter != 3 {
		t.Fatal(w, err)
	}
	var r abi.FSReadResponse
	if err := call(t, d, abi.FnFSRead, abi.FSRequest{Path: p, Max: 2}, &r); err != nil || string(r.Data) != "tw" || !r.Truncated {
		t.Fatal(r, err)
	}
	var l abi.FSListResponse
	if err := call(t, d, abi.FnFSList, abi.FSRequest{Path: root}, &l); err != nil || len(l.Entries) != 1 || l.Entries[0].Name != "a.md" {
		t.Fatal(l, err)
	}
	var st abi.FSStatResponse
	if err := call(t, d, abi.FnFSStat, abi.FSRequest{Path: root + "/none"}, &st); err != nil || st.Exists {
		t.Fatal(st, err)
	}
	if err := call(t, d, abi.FnFSRead, abi.FSRequest{Path: root + "/../x"}, &r); code(err) != abi.CodeCapabilityDenied {
		t.Fatal("escape:", err)
	}
	if err := call(t, d, abi.FnKVSet, abi.KVRequest{Key: "k", Value: []byte("v")}, nil); err == nil {
		t.Fatal("kv without store")
	}
	d.KV = &memKV{m: map[string][]byte{}}
	if err := call(t, d, abi.FnKVSet, abi.KVRequest{Key: "k", Value: []byte("v")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := call(t, d, abi.FnKVSet, abi.KVRequest{Key: "big", Value: make([]byte, 2<<20)}, nil); code(err) != abi.CodeLimit {
		t.Fatal("quota:", err)
	}
	var kv abi.KVResponse
	if err := call(t, d, abi.FnKVGet, abi.KVRequest{Key: "k"}, &kv); err != nil || string(kv.Value) != "v" {
		t.Fatal(kv, err)
	}
	// Вызовы других коннекторов не объявлены; сетевых функций у хоста нет вовсе.
	if err := call(t, d, abi.FnServiceCall, abi.ServiceCallRequest{Target: "browser", Action: "open"}, nil); code(err) != abi.CodeCapabilityDenied {
		t.Fatal(err)
	}
	for _, fn := range []string{"http_request", "tls_dial", "conn_read", "conn_write", "conn_close", "rm_rf"} {
		if err := call(t, d, fn, struct{}{}, nil); code(err) != abi.CodeNotSupported {
			t.Fatal(fn, err)
		}
	}
}

type memKV struct{ m map[string][]byte }

func (k *memKV) Get(key string) ([]byte, bool, error) { v, ok := k.m[key]; return v, ok, nil }
func (k *memKV) Set(key string, v []byte, q int64) error {
	if int64(len(v)) > q {
		return abi.Errorf(abi.CodeLimit, "quota")
	}
	k.m[key] = v
	return nil
}
func (k *memKV) Delete(key string) error { delete(k.m, key); return nil }

type fakeRouter struct{ got []abi.ServiceCallRequest }

func (f *fakeRouter) ServiceCall(_ context.Context, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	f.got = append(f.got, r)
	return abi.ExecResult{Structured: map[string]any{"sites": rule.Params["domains"], "contract": float64(rule.Contract)}}, nil
}

func TestServiceCallAndLog(t *testing.T) {
	d := dispatcher(t, `capabilities: { services: [ { target: browser, contract: 1, actions: [open, observe], params: { domains: ["${setting:allowed}"] } } ] }`,
		map[string]any{"allowed": []any{"lk.example", "b.example"}})
	var logs []string
	d.Log = func(l, m string) { logs = append(logs, m) }
	if err := call(t, d, abi.FnServiceCall, abi.ServiceCallRequest{Target: "browser", Action: "open"}, nil); code(err) != abi.CodeNotSupported {
		t.Fatal("no router:", err)
	}
	fr := &fakeRouter{}
	d.Services = fr
	for _, bad := range []abi.ServiceCallRequest{{Target: "browser", Action: "act"}, {Target: "mail", Action: "send"}} {
		if err := call(t, d, abi.FnServiceCall, bad, nil); code(err) != abi.CodeCapabilityDenied {
			t.Fatal(bad, err)
		}
	}
	var res abi.ExecResult
	if err := call(t, d, abi.FnServiceCall, abi.ServiceCallRequest{Target: "browser", Action: "open", Args: map[string]any{"url": "https://lk.example/"}}, &res); err != nil {
		t.Fatal(err)
	}
	st := res.Structured.(map[string]any)
	if fmt.Sprint(st["sites"]) != "[lk.example b.example]" || st["contract"] != 1.0 || len(fr.got) != 1 || fr.got[0].Args["url"] != "https://lk.example/" {
		t.Fatal(res, fr.got)
	}
	d.used = []string{"tok-SECRET-123"}
	call(t, d, abi.FnLog, abi.LogRequest{Level: "info", Msg: "token tok-SECRET-123 used"}, nil)
	if len(logs) != 1 || strings.Contains(logs[0], "tok-SECRET") {
		t.Fatal(logs)
	}
}
