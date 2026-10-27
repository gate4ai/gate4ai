// SPDX-License-Identifier: AGPL-3.0-only

// Package relayclient — сторона устройства для relay gate4.ai: регистрация,
// исходящий туннель (WebSocket + yamux), TLS с сертификатом устройства
// (ACME DNS-01 через API relay), отправка чекпоинтов свидетелю. Сервер relay —
// пакет github.com/gate4ai/gate4ai/relay; протокол — relay/docs/protocol.md.
package relayclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"github.com/libdns/libdns"

	"github.com/gate4ai/gate4ai/internal/audit"
)

// Device — сторона устройства: исходящий туннель к relay и TLS-сервер с
// сертификатом устройства для входящих соединений агентов.
type Device struct {
	RelayURL               string // https://relay.gate4.ai
	ID, Token              string
	RelayTLS               *tls.Config // проверка сертификата relay (nil — системные CA)
	ServerTLS              *tls.Config // сертификат устройства: расшифровка только здесь
	Handler                http.Handler
	Log                    *slog.Logger
	MinBackoff, MaxBackoff time.Duration
	// Dial — свой способ соединения с relay (тесты); nil — обычный.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)

	online  atomic.Bool
	lastErr atomic.Pointer[string] // последняя ошибка туннеля (nil — туннель ещё не падал)
}

// TestDialer — подмена соединений с relay (только тесты); nil — обычный DNS.
var TestDialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Online — туннель установлен.
func (d *Device) Online() bool { return d.online.Load() }

// phase — этап подключения работающего устройства и последняя ошибка туннеля.
func (d *Device) phase() (string, string) {
	switch e := d.lastErr.Load(); {
	case d.Online():
		return PhaseOnline, ""
	case e == nil:
		return PhaseConnecting, ""
	default:
		return PhaseReconnecting, *e
	}
}

func (d *Device) httpClient() *http.Client {
	dial := d.Dial
	if dial == nil {
		dial = TestDialer
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: d.RelayTLS, DialContext: dial}}
}

// Run держит туннель с автопереподключением до отмены ctx.
func (d *Device) Run(ctx context.Context) error {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.MinBackoff == 0 {
		d.MinBackoff = time.Second
	}
	if d.MaxBackoff == 0 {
		d.MaxBackoff = time.Minute
	}
	backoff := d.MinBackoff
	for ctx.Err() == nil {
		start := time.Now()
		err := d.once(ctx)
		d.online.Store(false)
		if ctx.Err() != nil {
			return nil
		}
		msg := "tunnel closed"
		if err != nil {
			msg = err.Error()
		}
		d.lastErr.Store(&msg)
		if time.Since(start) > 30*time.Second {
			backoff = d.MinBackoff
		}
		d.Log.Warn("relay tunnel down, reconnecting", "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, d.MaxBackoff)
	}
	return nil
}

func (d *Device) once(ctx context.Context) error {
	u := strings.Replace(strings.TrimSuffix(d.RelayURL, "/"), "https://", "wss://", 1) + "/tunnel"
	h := http.Header{}
	h.Set("Authorization", "Bearer "+d.Token)
	h.Set("X-Gate4ai-Device", d.ID)
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	ws, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPHeader: h, HTTPClient: d.httpClient()})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == 401 {
			return errors.New("relay rejected device credentials")
		}
		return err
	}
	ws.SetReadLimit(-1)
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.KeepAliveInterval = 15 * time.Second
	sess, err := yamux.Server(conn, cfg)
	if err != nil {
		conn.Close()
		return err
	}
	d.online.Store(true)
	d.Log.Info("relay tunnel up", "host", d.ID)
	// Без IdleTimeout keep-alive соединение агента (yamux-поток и горутина)
	// живёт, пока клиент молчит, — вечно. ReadTimeout/WriteTimeout не ставим:
	// ответы бывают долгими (ожидание @status до 2 мин, потоки MCP).
	srv := &http.Server{Handler: d.Handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 2 * time.Minute}
	// Сессия закрывается и по отмене ctx, и сама (обрыв туннеля): горутина не
	// переживает переподключение.
	go func() {
		select {
		case <-ctx.Done():
		case <-sess.CloseChan():
		}
		sess.Close()
	}()
	err = srv.Serve(tls.NewListener(yamuxListener{sess}, d.ServerTLS))
	sess.Close()
	return err
}

// Register регистрирует устройство на relay.
func Register(ctx context.Context, relayURL string, relayTLS *tls.Config, dial func(context.Context, string, string) (net.Conn, error)) (id, token, host string, err error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(relayURL, "/")+"/api/devices", nil)
	resp, err := (&Device{RelayTLS: relayTLS, Dial: dial}).httpClient().Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", "", fmt.Errorf("relay register: %s", resp.Status)
	}
	var v struct {
		ID    string `json:"device_id"`
		Token string `json:"token"`
		Host  string `json:"host"`
	}
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v.ID, v.Token, v.Host, err
}

// do — запрос устройства к API relay; body nil — без тела.
func (d *Device) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(d.RelayURL, "/")+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("X-Gate4ai-Device", d.ID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return d.httpClient().Do(req)
}

func (d *Device) post(ctx context.Context, method, path string, body any) error {
	b, _ := json.Marshal(body)
	resp, err := d.do(ctx, method, path, b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("relay %s: %s %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Witness — свидетель чекпоинтов (audit.Witness): relay получает только хэши.
func (d *Device) Witness(cp audit.Checkpoint) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return d.post(ctx, "POST", "/api/witness", witnessCheckpoint{Seq: cp.Seq, Head: cp.Head, TS: cp.TS, Sig: cp.Sig})
}

// LastWitnessed — последний чекпоинт, принятый relay (audit.WitnessReader):
// по нему журнал замечает обрезку хвоста вместе с его чекпоинтами.
func (d *Device) LastWitnessed() (audit.Checkpoint, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := d.do(ctx, "GET", "/api/witness/"+d.ID, nil)
	if err != nil {
		return audit.Checkpoint{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return audit.Checkpoint{}, false, fmt.Errorf("relay /api/witness: %s", resp.Status)
	}
	var cps []witnessCheckpoint
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cps); err != nil {
		return audit.Checkpoint{}, false, err
	}
	var last witnessCheckpoint
	for _, c := range cps {
		if c.Seq > last.Seq {
			last = c
		}
	}
	if last.Seq == 0 {
		return audit.Checkpoint{}, false, nil
	}
	return audit.Checkpoint{Seq: last.Seq, Head: last.Head, TS: last.TS, Sig: last.Sig}, true, nil
}

// witnessCheckpoint — тело POST /api/witness (протокол relay, docs/protocol.md
// в репозитории relay).
type witnessCheckpoint struct {
	Seq  int64     `json:"seq"`
	Head string    `json:"head"`
	TS   time.Time `json:"ts"`
	Sig  string    `json:"sig"`
}

// DNSProvider — libdns-провайдер для certmagic: TXT-записи DNS-01 ставит API relay.
type DNSProvider struct{ D *Device }

func (p DNSProvider) change(ctx context.Context, method, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	var out []libdns.Record
	for _, r := range recs {
		rr := r.RR()
		if rr.Type != "TXT" {
			continue
		}
		name := libdns.AbsoluteName(rr.Name, zone)
		if err := p.D.post(ctx, method, "/api/acme/txt", map[string]string{"name": strings.TrimSuffix(name, "."), "value": rr.Data}); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// AppendRecords ставит TXT.
func (p DNSProvider) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return p.change(ctx, "POST", zone, recs)
}

// DeleteRecords удаляет TXT.
func (p DNSProvider) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	return p.change(ctx, "DELETE", zone, recs)
}

// ACMEConfig — выпуск сертификата устройства через ACME DNS-01 (certmagic).
type ACMEConfig struct {
	Host       string // <device-id>.relay.gate4.ai
	StorageDir string
	CA         string // по умолчанию Let's Encrypt production
	Email      string
	Provider   certmagic.DNSProvider

	// issuer — эмитент вместо ACME (тесты); renewCheck — как часто проверять
	// продление (0 — по умолчанию certmagic).
	issuer     certmagic.Issuer
	renewCheck time.Duration
}

// ACMETLS выпускает сертификат и возвращает TLS-конфигурацию устройства;
// сертификат продлевается в фоне до отмены ctx. Кэш certmagic — свой: у
// глобального (certmagic.NewDefault) фоновое продление идёт с конфигурацией
// по умолчанию — чужое хранилище, без DNS-01, — и сертификат истекал бы у
// daemon, работающего дольше срока сертификата.
func ACMETLS(ctx context.Context, c ACMEConfig) (*tls.Config, error) {
	cfg, cache := acmeConfig(c)
	if err := cfg.ManageSync(ctx, []string{c.Host}); err != nil {
		cache.Stop()
		return nil, err
	}
	go func() { <-ctx.Done(); cache.Stop() }()
	t := cfg.TLSConfig()
	t.NextProtos = []string{"http/1.1"}
	return t, nil
}

// acmeConfig — конфигурация certmagic со своим кэшем: продление (фоновое и при
// загрузке) получает эту же конфигурацию — наше хранилище и эмитент с DNS-01.
func acmeConfig(c ACMEConfig) (*certmagic.Config, *certmagic.Cache) {
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert:   func(certmagic.Certificate) (*certmagic.Config, error) { return cfg, nil },
		RenewCheckInterval: c.renewCheck,
	})
	cfg = certmagic.New(cache, certmagic.Config{Storage: &certmagic.FileStorage{Path: c.StorageDir}})
	if c.issuer != nil {
		cfg.Issuers = []certmagic.Issuer{c.issuer}
		return cfg, cache
	}
	ca := c.CA
	if ca == "" {
		ca = certmagic.LetsEncryptProductionCA
	}
	issuer := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{CA: ca, Email: c.Email, Agreed: true,
		DisableHTTPChallenge: true, DisableTLSALPNChallenge: true,
		DNS01Solver: &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: c.Provider}}})
	cfg.Issuers = []certmagic.Issuer{quietIssuer{issuer}}
	return cfg, cache
}

// quietIssuer — ACME-эмитент без вопросов в stdin: ManageSync интерактивен, и без
// email certmagic спрашивает его в терминале — daemon, запущенный в нём, ждал бы ответа.
type quietIssuer struct{ *certmagic.ACMEIssuer }

func (q quietIssuer) PreCheck(ctx context.Context, names []string, _ bool) error {
	return q.ACMEIssuer.PreCheck(ctx, names, false)
}

// SelfSigned — самоподписанный сертификат (режим разработки и тесты).
func SelfSigned(hosts ...string) (tls.Certificate, *x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: hosts[0]},
		DNSNames: hosts, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf, nil
}
