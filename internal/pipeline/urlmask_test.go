// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

func TestMaskURLs(t *testing.T) {
	in := map[string]any{
		"url":  "https://account.example/auth?code=one-time&next=%2Fhome",
		"list": []any{"see https://a.example/cb?state=s1&access_token=t#x, ok", "https://a.example/?q=1"},
		"n":    1.0,
	}
	out := maskURLs(in).(map[string]any)
	if out["url"] != "https://account.example/auth?code=***&next=%2Fhome" {
		t.Fatal(out["url"])
	}
	l := out["list"].([]any)
	if l[0] != "see https://a.example/cb?state=***&access_token=***#x, ok" || l[1] != "https://a.example/?q=1" {
		t.Fatal(l)
	}
}

// Токены во фрагменте (implicit flow, response_mode=fragment) маскируются.
func TestMaskURLFragment(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.example/cb#access_token=fake-at&id_token=fake-id&state=s&expires_in=3600": "https://app.example/cb#access_token=***&id_token=***&state=***&expires_in=3600",
		"https://app.example/cb?x=1#code=fake-code":                                            "https://app.example/cb?x=1#code=***",
		"https://app.example/docs#section-2":                                                   "https://app.example/docs#section-2",
	} {
		if got := maskURLs(in); got != want {
			t.Errorf("%s: %v", in, got)
		}
	}
}

type replaceSecrets string

func (r replaceSecrets) Mask(v any) any {
	if s, ok := v.(string); ok {
		return strings.ReplaceAll(s, string(r), "***")
	}
	return v
}

// Текстовые доказательства маскируются, изображения — нет.
func TestEvidenceMasked(t *testing.T) {
	p := &Pipeline{Secrets: replaceSecrets("fake-secret")}
	if got := string(p.evidenceData(abi.EvidenceItem{MIME: "text/plain", Data: []byte("pw fake-secret https://x/cb#access_token=t")})); got != "pw *** https://x/cb#access_token=***" {
		t.Fatal(got)
	}
	if got := string(p.evidenceData(abi.EvidenceItem{MIME: "image/png", Data: []byte("fake-secret")})); got != "fake-secret" {
		t.Fatal(got)
	}
}

// Код в маршруте SPA внутри фрагмента (#/cb?code=…) тоже маскируется.
func TestMaskURLSPAFragment(t *testing.T) {
	got := maskURLs("https://app.example/#/cb?code=fake&state=s")
	if got != "https://app.example/#/cb?code=***&state=***" {
		t.Fatal(got)
	}
}
