// SPDX-License-Identifier: Apache-2.0

package redact

import "testing"

func TestURL(t *testing.T) {
	hide := func(k string) bool { return k == "ehrId" }
	for in, want := range map[string]string{
		"https://a/x?code=1&next=%2Fhome":          "https://a/x?code=***&next=%2Fhome",
		"https://a/cb#access_token=t&expires_in=3": "https://a/cb#access_token=***&expires_in=3",
		"https://a/cb#/route?code=abc&x=1":         "https://a/cb#/route?code=***&x=1",
		"https://a/docs#section-2":                 "https://a/docs#section-2",
		"https://a/api?ehrId=7&page=2":             "https://a/api?ehrId=***&page=2",
	} {
		if got := URL(in, hide); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if URL("https://a/api?ehrId=7", nil) != "https://a/api?ehrId=7" {
		t.Error("nil hide must not hide")
	}
}
