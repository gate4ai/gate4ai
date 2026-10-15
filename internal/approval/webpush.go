// SPDX-License-Identifier: FSL-1.1-ALv2

package approval

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

// WebPush — канал «браузерный push» (Web Push, RFC 8030): краткое уведомление
// в браузер, подписанный на странице Web UI (локальной или удалённой /ui/ на
// адресе relay). Сообщение идёт через push-сервис браузера и зашифровано для
// этого браузера (RFC 8291, aes128gcm); сервер подписан ключом VAPID устройства
// (RFC 8292). В уведомлении только агент, действие и короткий хэш; решение
// принимается на странице с сессией.
type WebPush struct {
	State *state.Store
	Log   *slog.Logger
	HTTP  *http.Client
	Now   func() time.Time
	// Subject — контакт VAPID (sub): mailto: или https:.
	Subject string

	priv *ecdsa.PrivateKey
	pub  []byte // несжатая точка P-256 (65 байт)
}

// NewWebPush загружает или создаёт ключ VAPID устройства (в keychain).
func NewWebPush(ks keys.Store, st *state.Store) (*WebPush, error) {
	seed, err := keys.RandomBytes(ks, "webpush.vapid", 32)
	if err != nil {
		return nil, err
	}
	// Скаляр должен быть меньше порядка кривой: при редком неудачном значении —
	// детерминированно хэшируем дальше.
	var priv *ecdsa.PrivateKey
	for i := 0; i < 8; i++ {
		if priv, err = ecdsa.ParseRawPrivateKey(elliptic.P256(), seed); err == nil {
			break
		}
		h := sha256.Sum256(seed)
		seed = h[:]
	}
	if err != nil {
		return nil, err
	}
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	return &WebPush{State: st, Log: slog.Default(), HTTP: &http.Client{Timeout: 15 * time.Second}, Now: time.Now,
		Subject: "https://gate4.ai", priv: priv, pub: pub}, nil
}

// PublicKey — ключ сервера для PushManager.subscribe (applicationServerKey), base64url.
func (w *WebPush) PublicKey() string { return base64.RawURLEncoding.EncodeToString(w.pub) }

// Message — содержимое уведомления (его показывает Service Worker).
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"` // путь страницы относительно базы подписки
	Tag   string `json:"tag,omitempty"`
}

// Notify — новый запрос подтверждения.
func (w *WebPush) Notify(a *state.Approval) {
	w.sendAll(Message{Title: "gate4.ai: нужно подтверждение", Body: fmt.Sprintf("%s → %s #%s", a.Agent, a.Action, audit.Short(a.PreviewHash)),
		URL: "/approvals", Tag: a.ID})
}

// Resolved — решение принято (уведомление не нужно).
func (w *WebPush) Resolved(*state.Approval) {}

// Proposed — агент предложил изменение настроек.
func (w *WebPush) Proposed(p *state.Proposal) {
	w.sendAll(Message{Title: "gate4.ai: агент предлагает изменение", Body: p.Agent + ": " + p.Summary + " — примените в Web UI на компьютере",
		URL: "/proposals/" + p.ID, Tag: p.ID})
}

// Denied — отказ политикой: в push не отправляем (шумно).
func (w *WebPush) Denied(string, string, string) {}

// Test отправляет пробное уведомление во все подписанные браузеры.
func (w *WebPush) Test() error {
	subs, _ := w.State.PushSubs()
	if len(subs) == 0 {
		return errors.New("no browser is subscribed: open the Web UI in the browser and press «Включить в этом браузере»")
	}
	return w.sendAll(Message{Title: "gate4.ai: проверка уведомлений", Body: "Так будут приходить запросы подтверждения.", URL: "/approvals"})
}

func (w *WebPush) sendAll(m Message) error {
	subs, err := w.State.PushSubs()
	if err != nil {
		return err
	}
	var last error
	for _, s := range subs {
		msg := m
		msg.URL = s.Label + m.URL // база страницы подписки: "" или "/ui"
		err := w.Send(context.Background(), s, msg)
		w.State.ChannelResult("webpush", err)
		var gone *GoneError
		if errors.As(err, &gone) {
			_ = w.State.RemovePushSub(s.Endpoint)
		}
		if err != nil {
			w.Log.Warn("webpush", "err", err)
			last = err
		}
	}
	return last
}

// GoneError — подписка больше не действует (404/410): её нужно удалить.
type GoneError struct{ Status int }

func (e *GoneError) Error() string { return fmt.Sprintf("push subscription expired (%d)", e.Status) }

// Send отправляет одно уведомление.
func (w *WebPush) Send(ctx context.Context, s state.PushSub, m Message) error {
	payload, _ := json.Marshal(m)
	body, err := encrypt(s, payload)
	if err != nil {
		return err
	}
	eu, err := url.Parse(s.Endpoint)
	if err != nil || eu.Scheme != "https" {
		return errors.New("push endpoint must be https")
	}
	jwt, err := w.vapid(eu.Scheme + "://" + eu.Host)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "1800")
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+w.PublicKey())
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("push service: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	switch {
	case resp.StatusCode == 404 || resp.StatusCode == 410:
		return &GoneError{Status: resp.StatusCode}
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("push service: %s %s", resp.Status, bytes.TrimSpace(rb))
	}
	return nil
}

// vapid — JWT ES256 для push-сервиса (RFC 8292).
func (w *WebPush) vapid(aud string) (string, error) {
	enc := base64.RawURLEncoding
	h := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	cl, _ := json.Marshal(map[string]any{"aud": aud, "exp": w.Now().Add(12 * time.Hour).Unix(), "sub": w.Subject})
	signing := h + "." + enc.EncodeToString(cl)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, w.priv, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + enc.EncodeToString(sig), nil
}

// encrypt — RFC 8291 (aes128gcm, одна запись).
func encrypt(s state.PushSub, plaintext []byte) ([]byte, error) {
	dec := func(v string) ([]byte, error) {
		if b, err := base64.RawURLEncoding.DecodeString(v); err == nil {
			return b, nil
		}
		return base64.URLEncoding.DecodeString(v)
	}
	uaPub, err := dec(s.Keys.P256dh)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	authSecret, err := dec(s.Keys.Auth)
	if err != nil || len(authSecret) != 16 {
		return nil, errors.New("auth secret must be 16 bytes")
	}
	curve := ecdh.P256()
	ua, err := curve.NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("p256dh: %w", err)
	}
	as, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPub...), asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	const rs = 4096
	if len(plaintext)+1+16 > rs {
		return nil, errors.New("push message too long")
	}
	ct := gcm.Seal(nil, nonce, append(plaintext, 0x02), nil) // 0x02 — последняя запись
	out := make([]byte, 0, 16+4+1+len(asPub)+len(ct))
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, rs)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	return append(out, ct...), nil
}

// decrypt — обратная операция (для тестов: так расшифровывает браузер).
func decrypt(uaPriv *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short body")
	}
	salt, idlen := body[:16], int(body[20])
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		return nil, err
	}
	shared, err := uaPriv.ECDH(as)
	if err != nil {
		return nil, err
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...), asPub...)
	ikm, _ := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(pt, "\x02"), nil
}
