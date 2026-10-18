// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import "testing"

func TestMaskURLs(t *testing.T) {
	in := map[string]any{
		"url":  "https://lk.example/auth?code=one-time&next=%2Fhome",
		"list": []any{"see https://a.example/cb?state=s1&access_token=t#x, ok", "https://a.example/?q=1"},
		"n":    1.0,
	}
	out := maskURLs(in).(map[string]any)
	if out["url"] != "https://lk.example/auth?code=***&next=%2Fhome" {
		t.Fatal(out["url"])
	}
	l := out["list"].([]any)
	if l[0] != "see https://a.example/cb?state=***&access_token=***#x, ok" || l[1] != "https://a.example/?q=1" {
		t.Fatal(l)
	}
}
