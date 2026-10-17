// SPDX-License-Identifier: AGPL-3.0-only

package approval

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

// Фейковый push-сервис: проверяет VAPID и расшифровывает сообщение так, как
// это делает браузер (RFC 8291 / RFC 8292).
func TestWebPush(t *testing.T) {
	ks := keys.NewMemory()
	st, err := state.Open(filepath.Join(t.TempDir(), "s.db"), ks)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w, err := NewWebPush(ks, st)
	if err != nil {
		t.Fatal(err)
	}
	if w2, _ := NewWebPush(ks, st); w2.PublicKey() != w.PublicKey() {
		t.Fatal("VAPID key must persist")
	}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	_, _ = rand.Read(authSecret)

	var got Message
	status := 201
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" {
			t.Error("headers", r.Header)
		}
		// Authorization: vapid t=<jwt>, k=<key>
		a := strings.TrimPrefix(r.Header.Get("Authorization"), "vapid ")
		parts := strings.Split(a, ", ")
		jwt, k := strings.TrimPrefix(parts[0], "t="), strings.TrimPrefix(parts[1], "k=")
		if k != w.PublicKey() {
			t.Error("k")
		}
		seg := strings.Split(jwt, ".")
		kb, _ := base64.RawURLEncoding.DecodeString(k)
		x, y := elliptic.Unmarshal(elliptic.P256(), kb)
		sig, _ := base64.RawURLEncoding.DecodeString(seg[2])
		sum := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
		if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			t.Error("bad VAPID signature")
		}
		cl, _ := base64.RawURLEncoding.DecodeString(seg[1])
		var claims map[string]any
		_ = json.Unmarshal(cl, &claims)
		if claims["aud"] != "https://"+r.Host {
			t.Error("aud", claims)
		}
		body, _ := io.ReadAll(r.Body)
		pt, err := decrypt(ua, authSecret, body)
		if err != nil {
			t.Error("decrypt", err)
		}
		_ = json.Unmarshal(pt, &got)
		rw.WriteHeader(status)
	}))
	defer srv.Close()
	w.HTTP = srv.Client()

	var sub state.PushSub
	sub.Endpoint = srv.URL + "/push/abc"
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(authSecret)
	sub.Label = "/ui"
	if err := st.AddPushSub(sub); err != nil {
		t.Fatal(err)
	}
	w.Notify(&state.Approval{ID: "a_1", Agent: "claude-code", Action: "files.append", PreviewHash: "a3f9c21e00"})
	if got.URL != "/ui/approvals" || !strings.Contains(got.Body, "claude-code → files.append") {
		t.Fatalf("%+v", got)
	}
	if s, _ := st.ChannelStats(); s["webpush"].Sent != 1 {
		t.Fatal(s)
	}
	// Подписка истекла — удаляется.
	status = 410
	if err := w.Test(); err == nil {
		t.Fatal("410 must fail")
	}
	if subs, _ := st.PushSubs(); len(subs) != 0 {
		t.Fatal("expired subscription kept")
	}
	if err := w.Test(); err == nil {
		t.Fatal("no subscribers must fail")
	}
}
