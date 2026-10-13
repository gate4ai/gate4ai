// SPDX-License-Identifier: FSL-1.1-ALv2

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeBackend struct{ calls []string }

func (f *fakeBackend) Tools(context.Context) ([]Tool, error) {
	return []Tool{{Name: "email.send", Description: "Send", Class: "external", InputSchema: map[string]any{"type": "object"}},
		{Name: "files.read", Description: "Read", Class: "read", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (f *fakeBackend) Call(_ context.Context, action string, args map[string]any) (map[string]any, error) {
	f.calls = append(f.calls, action)
	switch action {
	case "email.send":
		return map[string]any{"status": "denied", "code": "denied_by_policy", "message": "denied_by_policy: recipient not in allowlist; user notified"}, nil
	case "@status":
		return map[string]any{"status": "pending", "id": args["id"]}, nil
	}
	return map[string]any{"status": "ok", "result": "text"}, nil
}

func call(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(params)
	id := int64(1)
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(b)})
	out, err := s.HandleMessage(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	return m
}

func TestMCP(t *testing.T) {
	fb := &fakeBackend{}
	s := NewServer(fb)
	init := call(t, s, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
	r := init["result"].(map[string]any)
	if r["protocolVersion"] != "2025-06-18" || r["serverInfo"].(map[string]any)["name"] != "gate4ai" {
		t.Fatal(init)
	}
	if call(t, s, "initialize", map[string]any{"protocolVersion": "1999-01-01"})["result"].(map[string]any)["protocolVersion"] != ProtocolVersion {
		t.Fatal("unknown version → latest")
	}
	list := call(t, s, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, x := range list {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "email_send,files_read,gate4ai_status" {
		t.Fatal(names)
	}
	for _, n := range names {
		if strings.Contains(n, "policy") || strings.Contains(n, "config") {
			t.Fatal("no policy tools in MCP")
		}
	}
	if list[1].(map[string]any)["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Fatal("annotations")
	}
	res := call(t, s, "tools/call", map[string]any{"name": "email_send", "arguments": map[string]any{"to": []string{"x@y.z"}}})["result"].(map[string]any)
	if res["isError"] != true || !strings.HasPrefix(res["content"].([]any)[0].(map[string]any)["text"].(string), "denied_by_policy: recipient not in allowlist") {
		t.Fatal(res)
	}
	res = call(t, s, "tools/call", map[string]any{"name": "files_read", "arguments": map[string]any{}})["result"].(map[string]any)
	if res["isError"] != false || res["structuredContent"].(map[string]any)["status"] != "ok" {
		t.Fatal(res)
	}
	res = call(t, s, "tools/call", map[string]any{"name": "gate4ai_status", "arguments": map[string]any{"id": "a_1"}})["result"].(map[string]any)
	if res["isError"] != false || fb.calls[len(fb.calls)-1] != "@status" {
		t.Fatal(res)
	}
	if e := call(t, s, "tools/call", map[string]any{"name": "Bad Name!"}); e["error"] == nil {
		t.Fatal("bad tool name must error")
	}
	if e := call(t, s, "nope", nil); e["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatal(e)
	}
	// Уведомление — без ответа.
	out, _ := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if out != nil {
		t.Fatal("notification answered")
	}
}
