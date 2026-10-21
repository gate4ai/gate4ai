// SPDX-License-Identifier: Apache-2.0

package abi

import (
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

func TestCheckResult(t *testing.T) {
	out := manifest.MustSchema(`{ type: object, properties: { n: { type: integer } }, required: [n] }`)
	ok := ExecResult{Structured: map[string]int{"n": 1}, Content: []Content{Text("x"), Image("image/png", []byte{1}),
		Resource("file:///a", "text/plain", "a", nil), ResourceLink("file:///b", "b", "")}}
	if err := CheckResult(ok, &out); err != nil {
		t.Fatal(err)
	}
	if err := CheckResult(ExecResult{Structured: "anything"}, nil); err != nil {
		t.Fatal("without output_schema any structured result is allowed", err)
	}
	bad := []struct {
		r    ExecResult
		want string
	}{
		{ExecResult{}, "structured result is required"},
		{ExecResult{Structured: map[string]any{"n": "1"}}, "does not match"},
		{ExecResult{Structured: map[string]any{"n": 1, "x": 2}}, "does not match"},
		{ExecResult{Structured: map[string]any{"n": 1}, Content: []Content{{Type: "video"}}}, "unknown content type"},
		{ExecResult{Structured: map[string]any{"n": 1}, Content: []Content{Image("text/html", []byte{1})}}, "image/*"},
		{ExecResult{Structured: map[string]any{"n": 1}, Content: []Content{Resource("", "", "a", nil)}}, "uri"},
		{ExecResult{Structured: map[string]any{"n": 1}, Content: []Content{ResourceLink("file:///b", "", "")}}, "name"},
		{ExecResult{Structured: map[string]any{"n": 1}, VerifyAfterS: -1}, "negative"},
	}
	for _, b := range bad {
		if err := CheckResult(b.r, &out); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%+v: want %q, got %v", b.r, b.want, err)
		}
	}
}
