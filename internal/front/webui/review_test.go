// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
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

// Текст сообщения — только из хранилища сессии, не из адреса (E.20).
func TestMessagesFromSessionOnly(t *testing.T) {
	now := time.Unix(1000, 0)
	u := &UI{Now: func() time.Time { return now }, messages: map[string]message{}}
	to := u.stashMessage("sid", redirect("/settings#relay", "Сохранено")["redirect"].(string))
	if strings.Contains(to, "flash") || !strings.HasSuffix(to, "#relay") {
		t.Fatal(to)
	}
	q, _ := url.ParseQuery(strings.SplitN(strings.SplitN(to, "?", 2)[1], "#", 2)[0])
	id := q.Get("msg")
	if fl, _ := u.takeMessage("sid", id); fl != "Сохранено" {
		t.Fatal(fl)
	}
	if fl, e := u.takeMessage("other", id); fl != "" || e != "" {
		t.Fatal("another session sees the message")
	}
	now = now.Add(messageTTL + time.Second)
	if fl, _ := u.takeMessage("sid", id); fl != "" {
		t.Fatal("expired message")
	}
	if got := u.stashMessage("sid", "/approvals"); got != "/approvals" {
		t.Fatal(got)
	}
}

// Ссылка с ?flash=/?error= не показывает владельцу чужой текст.
func TestNoSpoofedFlash(t *testing.T) {
	u, _, _, base := setup(t)
	_, _, _, get := loginAs(t, u, base)
	p := get("/?flash=" + url.QueryEscape("SPOOFED-FLASH") + "&error=" + url.QueryEscape("SPOOFED-ERROR"))
	if strings.Contains(p, "SPOOFED") {
		t.Fatal("text from the address is shown")
	}
}
