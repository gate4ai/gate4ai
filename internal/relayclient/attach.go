// SPDX-License-Identifier: AGPL-3.0-only

package relayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// Этапы подключения устройства к relay (Status.Phase).
const (
	PhaseIssuing      = "issuing"      // выпуск сертификата устройства (ACME), до туннеля
	PhaseConnecting   = "connecting"   // первое соединение с relay
	PhaseOnline       = "online"       // туннель установлен
	PhaseReconnecting = "reconnecting" // туннель упал, устройство переподключается
	PhaseFailed       = "failed"       // подключение остановлено ошибкой (см. Error)
)

// Status — состояние подключения устройства к relay (для Web UI).
type Status struct {
	Configured bool   `json:"configured"`
	Online     bool   `json:"online"`
	Phase      string `json:"phase,omitempty"` // Phase*; "" — relay не настроен
	Error      string `json:"error,omitempty"` // последняя ошибка (failed, reconnecting)
	URL        string `json:"url,omitempty"`   // адрес relay
	DeviceID   string `json:"device_id,omitempty"`
	Host       string `json:"host,omitempty"`    // <device-id>.<домен relay>
	MCPURL     string `json:"mcp_url,omitempty"` // https://<host>/mcp
}

// DefaultURL — relay gate4.ai по умолчанию.
const DefaultURL = "https://relay.gate4.ai"

// Attach подключает relay к daemon: маршруты владельца (регистрация, статус),
// туннель устройства и свидетеля журнала; перезапускается при смене конфигурации.
// Возвращает функцию состояния для Web UI.
func Attach(ctx context.Context, d *daemon.Daemon) func() Status {
	var mu sync.Mutex
	var cur *Device
	var stop context.CancelFunc
	var key string
	var phase, failure string // этап до появления Device; failure — причина PhaseFailed
	d.AddPublicURL(func() string {
		if c := d.Config.Current().Relay; c.URL != "" && c.DeviceID != "" {
			return "https://" + deviceHost(c)
		}
		return ""
	})
	apply := func(c *config.Config) {
		mu.Lock()
		defer mu.Unlock()
		r := c.Relay
		tok := ""
		if r.URL != "" && r.DeviceID != "" && r.Token != "" {
			tok, _ = d.Vault.Get(r.Token)
		}
		k := r.URL + "|" + r.DeviceID + "|" + tok + "|" + r.ACMECA + "|" + r.CAFile
		if r.SelfSigned {
			k += "|self"
		}
		if k == key {
			return
		}
		key = k
		if stop != nil {
			stop()
			stop, cur = nil, nil
			d.Journal.SetWitness(nil)
		}
		phase, failure = "", ""
		if tok == "" {
			if r.DeviceID != "" {
				phase, failure = PhaseFailed, "relay token not found in the vault: register the device again"
			}
			return
		}
		phase = PhaseIssuing
		dctx, cancel := context.WithCancel(ctx)
		stop = cancel
		// Выпуск сертификата (ACME) может занять время — не блокируем daemon.
		go func() {
			dev, err := deviceFor(dctx, d, r, tok)
			mu.Lock()
			if dctx.Err() != nil {
				mu.Unlock()
				return
			}
			if err != nil {
				phase, failure = PhaseFailed, err.Error()
				mu.Unlock()
				d.Log.Error("relay disabled", "err", err)
				return
			}
			cur = dev
			mu.Unlock()
			d.Journal.SetWitness(dev)
			_ = d.Journal.RetryWitness()
			dev.Run(dctx)
		}()
	}
	status := func() Status {
		mu.Lock()
		dev, ph, fail := cur, phase, failure
		mu.Unlock()
		c := d.Config.Current().Relay
		st := Status{Configured: c.DeviceID != "", Online: dev != nil && dev.Online(), URL: c.URL, DeviceID: c.DeviceID}
		if c.DeviceID != "" {
			st.Host = deviceHost(c)
			st.MCPURL = "https://" + st.Host + "/mcp"
			st.Phase, st.Error = ph, fail
			if dev != nil {
				st.Phase, st.Error = dev.phase()
			}
			if st.Phase == "" { // конфигурация уже новая, apply ещё не отработал
				st.Phase = PhaseIssuing
			}
		}
		return st
	}
	d.AddRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/relay/register", func(w http.ResponseWriter, r *http.Request) {
			p := daemon.PrincipalFrom(r.Context())
			if !p.IsOwner() {
				http.Error(w, `{"error":{"code":"owner_only"}}`, 403)
				return
			}
			var req struct {
				URL        string `json:"url"`
				CAFile     string `json:"ca_file"`
				SelfSigned bool   `json:"self_signed"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			res, err := RegisterDevice(r.Context(), d, p, req.URL, req.CAFile, req.SelfSigned)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "relay", "message": err.Error()}})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(res)
		})
		mux.HandleFunc("GET /v1/relay/status", func(w http.ResponseWriter, r *http.Request) {
			st := status()
			out := map[string]any{"configured": st.Configured, "online": st.Online}
			if st.MCPURL != "" {
				out["mcp_url"] = st.MCPURL
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		})
	})
	apply(d.Config.Current())
	d.Config.Subscribe(apply)
	return status
}

func deviceHost(r config.Relay) string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return ""
	}
	return r.DeviceID + "." + u.Hostname()
}

func relayTLS(r config.Relay) (*tls.Config, error) {
	if r.CAFile == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(r.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("relay.ca_file: no certificates")
	}
	return &tls.Config{RootCAs: pool}, nil
}

func deviceFor(ctx context.Context, d *daemon.Daemon, r config.Relay, tok string) (*Device, error) {
	rt, err := relayTLS(r)
	if err != nil {
		return nil, err
	}
	dev := &Device{RelayURL: r.URL, ID: r.DeviceID, Token: tok, RelayTLS: rt, Handler: d.PublicHandler(), Log: d.Log}
	host := deviceHost(r)
	if r.SelfSigned {
		cert, _, err := SelfSigned(host)
		if err != nil {
			return nil, err
		}
		dev.ServerTLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		return dev, nil
	}
	// ACME DNS-01 в фоне: туннель поднимается, когда сертификат готов.
	t, err := ACMETLS(ctx, ACMEConfig{Host: host, StorageDir: filepath.Join(d.Home, "certs"), CA: r.ACMECA, Email: r.Email, Provider: DNSProvider{D: dev}})
	if err != nil {
		return nil, err
	}
	dev.ServerTLS = t
	return dev, nil
}

// RegisterDevice регистрирует устройство на relay: токен — в Vault, адрес и id —
// новой версией конфигурации (только владелец).
func RegisterDevice(ctx context.Context, d *daemon.Daemon, p auth.Principal, relayURL, caFile string, selfSigned bool) (map[string]any, error) {
	if err := p.RequireOwner(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(relayURL, "https://") {
		return nil, errors.New("relay url must start with https://")
	}
	rc := config.Relay{URL: relayURL, CAFile: caFile}
	rt, err := relayTLS(rc)
	if err != nil {
		return nil, err
	}
	id, tok, host, err := Register(ctx, relayURL, rt, nil)
	if err != nil {
		return nil, err
	}
	if err := d.Vault.Set("relay.token", tok); err != nil {
		return nil, err
	}
	raw, _ := d.Config.CurrentRaw()
	c, err := d.Config.Validate(raw)
	if err != nil {
		return nil, err
	}
	c.Relay.URL, c.Relay.DeviceID, c.Relay.Token, c.Relay.CAFile, c.Relay.SelfSigned = relayURL, id, "@relay.token", caFile, selfSigned
	out, err := config.Marshal(c)
	if err != nil {
		return nil, err
	}
	if _, err := d.Config.Put(p, out, "relay registration"); err != nil {
		return nil, err
	}
	return map[string]any{"device_id": id, "host": host, "mcp_url": "https://" + host + "/mcp"}, nil
}
