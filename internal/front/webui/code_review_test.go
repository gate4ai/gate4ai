// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Origin сверяется точно: чужой сервер на localhost:<другой порт> не проходит (E.13).
func TestOriginExact(t *testing.T) {
	u := &UI{Addr: "127.0.0.1:7777"}
	for o, want := range map[string]bool{
		"": true, "http://127.0.0.1:7777": true, "http://localhost:7777": true,
		"http://localhost:8080": false, "http://localhost:77770": false, "http://127.0.0.1:8080": false, "null": false,
	} {
		r, _ := http.NewRequest("POST", "http://127.0.0.1:7777/x", nil)
		if o != "" {
			r.Header.Set("Origin", o)
		}
		if got := u.originOK(local, r); got != want {
			t.Errorf("%q: %v", o, got)
		}
	}
}

// back из формы не уводит на чужой хост (E.20).
func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/approvals": "/approvals", "/policy#lists": "/policy#lists", "//evil.example/x": "/", "/\\evil.example": "/",
		"https://evil.example": "/", "evil": "/", "": "/",
	} {
		if got := localPath(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// Ссылка с ?flash=/?error= не показывает владельцу чужой текст.
func TestNoSpoofedFlash(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, _, get := loginAs(t, u, base)
	p := get("/?flash=" + url.QueryEscape("SPOOFED-FLASH") + "&error=" + url.QueryEscape("SPOOFED-ERROR"))
	// Адрес страницы остаётся в ссылке переключателя языка (back=…), но как текст не выводится.
	if strings.Contains(p, ">SPOOFED") {
		t.Fatal("text from the address is shown")
	}
}
