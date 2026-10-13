// SPDX-License-Identifier: FSL-1.1-ALv2

// Package relayclient — сторона устройства для relay gate4.ai: регистрация,
// исходящий туннель (WebSocket + yamux), TLS с сертификатом устройства
// (ACME DNS-01 через API relay), отправка чекпоинтов свидетелю. Сервер relay —
// отдельный модуль github.com/gate4ai/relay; протокол — его docs/protocol.md.
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

	online atomic.Bool
}

// TestDialer — подмена соединений с relay (только тесты); nil — обычный DNS.
var TestDialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Online — туннель установлен.
func (d *Device) Online() bool { return d.online.Load() }

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
		if time.Since(start) > 30*time.Second {
			backoff = d.MinBackoff
		}
		d.Log.Warn("relay tunnel down, reconnecting", "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		if backoff *= 2; backoff > d.MaxBackoff {
			backoff = d.MaxBackoff
		}
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
	srv := &http.Server{Handler: d.Handler, ReadHeaderTimeout: 15 * time.Second}
	go func() { <-ctx.Done(); sess.Close() }()
	err = srv.Serve(tls.NewListener(sess, d.ServerTLS))
	sess.Close()
	return err
}

// Register регистрирует устройство на relay.
func Register(ctx context.Context, relayURL string, relayTLS *tls.Config, dial func(context.Context, string, string) (net.Conn, error)) (id, token, host string, err error) {
	if dial == nil {
		dial = TestDialer
	}
	c := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: relayTLS, DialContext: dial}}
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(relayURL, "/")+"/api/devices", nil)
	resp, err := c.Do(req)
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

func (d *Device) post(ctx context.Context, method, path string, body any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(d.RelayURL, "/")+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("X-Gate4ai-Device", d.ID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient().Do(req)
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
	Resolvers  []string
}

// ACMETLS выпускает/обновляет сертификат и возвращает TLS-конфигурацию устройства.
func ACMETLS(ctx context.Context, c ACMEConfig) (*tls.Config, error) {
	cfg := certmagic.NewDefault()
	cfg.Storage = &certmagic.FileStorage{Path: c.StorageDir}
	ca := c.CA
	if ca == "" {
		ca = certmagic.LetsEncryptProductionCA
	}
	issuer := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{CA: ca, Email: c.Email, Agreed: true,
		DisableHTTPChallenge: true, DisableTLSALPNChallenge: true,
		DNS01Solver: &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: c.Provider, Resolvers: c.Resolvers}}})
	cfg.Issuers = []certmagic.Issuer{issuer}
	if err := cfg.ManageSync(ctx, []string{c.Host}); err != nil {
		return nil, err
	}
	t := cfg.TLSConfig()
	t.NextProtos = []string{"http/1.1"}
	return t, nil
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
