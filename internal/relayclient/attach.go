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
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// Этапы подключения устройства к relay (Status.Phase).
const (
	PhaseRegistering  = "registering"  // устройство регистрируется на relay само (адреса ещё нет)
	PhaseIssuing      = "issuing"      // выпуск сертификата устройства (ACME), до туннеля
	PhaseConnecting   = "connecting"   // первое соединение с relay
	PhaseOnline       = "online"       // туннель установлен
	PhaseReconnecting = "reconnecting" // туннель упал, устройство переподключается
	PhaseFailed       = "failed"       // подключение остановлено ошибкой (см. Error); без адреса — регистрация не удалась, повтор в NextRetry
)

// Status — состояние подключения устройства к relay (для Web UI).
type Status struct {
	Configured bool      `json:"configured"`         // адрес устройства есть (device_id)
	Disabled   bool      `json:"disabled,omitempty"` // запрет владельца (relay.disabled): не регистрируется и не подключается
	Online     bool      `json:"online"`
	Phase      string    `json:"phase,omitempty"`     // Phase*; "" — relay не настроен и сам не включается
	Error      string    `json:"error,omitempty"`     // последняя ошибка (failed, reconnecting, issuing — повтор выпуска)
	NextRetry  time.Time `json:"next_retry,omitzero"` // когда повторится регистрация или выпуск сертификата
	URL        string    `json:"url,omitempty"`       // адрес relay
	DeviceID   string    `json:"device_id,omitempty"`
	Host       string    `json:"host,omitempty"`    // <device-id>.<домен relay>
	MCPURL     string    `json:"mcp_url,omitempty"` // https://<host>: MCP по корню
}

// RetryCert — первая пауза перед повторным выпуском сертификата устройства
// (дальше вдвое дольше, до часа).
var RetryCert = 5 * time.Minute

// RetryRegister — пауза между попытками самостоятельной регистрации на relay.
var RetryRegister = time.Hour

// DefaultURL — relay gate4.ai по умолчанию (переменная — для тестов).
var DefaultURL = "https://relay.gate4.ai"

// registerMu — регистрации по одной: самостоятельная и ручная (кнопка, мастер,
// CLI) не создадут два устройства.
var registerMu sync.Mutex

// Attach подключает relay к daemon: маршруты владельца (регистрация, статус),
// туннель устройства и свидетеля журнала; перезапускается при смене конфигурации.
// auto — доступ из интернета включён по умолчанию: пока у устройства нет адреса
// и владелец его не выключил (relay.disabled), daemon сам регистрируется на
// relay (relay.url, иначе DefaultURL) и при неудаче повторяет раз в RetryRegister.
// С прямым публичным адресом (--public) само не регистрируется.
// Возвращает функцию состояния для Web UI.
func Attach(ctx context.Context, d *daemon.Daemon, auto bool) func() Status {
	auto = auto && d.PublicURL() == ""
	retryRegister, retryCert := RetryRegister, RetryCert // читаются один раз: тесты меняют их между Attach
	var mu sync.Mutex
	var cur *Device
	var stop, stopReg context.CancelFunc
	var key string
	var phase, failure string // этап до появления Device; failure — причина PhaseFailed
	var nextTry time.Time     // следующая попытка регистрации или выпуска сертификата
	d.AddPublicURL(func() string {
		if c := d.Config.Current().Relay; c.URL != "" && c.DeviceID != "" && !c.Disabled {
			return "https://" + deviceHost(c)
		}
		return ""
	})
	// register — цикл самостоятельной регистрации, пока не получится или не отменят.
	register := func(rctx context.Context, r config.Relay) {
		for {
			err := autoRegister(rctx, d, r)
			mu.Lock()
			if rctx.Err() != nil {
				mu.Unlock()
				return
			}
			if err == nil {
				mu.Unlock()
				return // новая конфигурация: apply поднимет туннель
			}
			phase, failure, nextTry = PhaseFailed, err.Error(), time.Now().Add(retryRegister)
			mu.Unlock()
			d.Log.Error("relay: device not registered, retrying", "err", err, "in", retryRegister)
			select {
			case <-rctx.Done():
				return
			case <-time.After(retryRegister):
			}
		}
	}
	apply := func(c *config.Config) {
		mu.Lock()
		defer mu.Unlock()
		r := c.Relay
		tok := ""
		if r.URL != "" && r.DeviceID != "" && r.Token != "" && !r.Disabled {
			tok, _ = d.Vault.Get(r.Token)
		}
		k := r.URL + "|" + r.DeviceID + "|" + tok + "|" + r.ACMECA + "|" + r.CAFile
		if r.SelfSigned {
			k += "|self"
		}
		if r.Disabled {
			k += "|off"
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
		if stopReg != nil {
			stopReg()
			stopReg = nil
		}
		phase, failure, nextTry = "", "", time.Time{}
		if r.Disabled {
			return
		}
		if r.DeviceID == "" {
			if auto {
				phase = PhaseRegistering
				rctx, cancel := context.WithCancel(ctx)
				stopReg = cancel
				go register(rctx, r)
			}
			return
		}
		if tok == "" {
			phase, failure = PhaseFailed, "relay token not found in the vault: register the device again"
			return
		}
		phase = PhaseIssuing
		dctx, cancel := context.WithCancel(ctx)
		stop = cancel
		// Выпуск сертификата (ACME) может занять время — не блокируем daemon.
		// Неудачу повторяем с растущей паузой (лимит Let's Encrypt — 5 неудачных
		// проверок в час на имя), а не отключаем relay до перезапуска: этап
		// остаётся issuing, причина — в Error.
		go func() {
			wait := retryCert
			var dev *Device
			for {
				var err error
				dev, err = deviceFor(dctx, d, r, tok)
				mu.Lock()
				if dctx.Err() != nil {
					mu.Unlock()
					return
				}
				if err == nil {
					cur, failure, nextTry = dev, "", time.Time{}
					mu.Unlock()
					break
				}
				failure, nextTry = err.Error(), time.Now().Add(wait)
				mu.Unlock()
				d.Log.Error("relay: device certificate not obtained, retrying", "err", err, "in", wait)
				select {
				case <-dctx.Done():
					return
				case <-time.After(wait):
				}
				wait = min(2*wait, time.Hour)
			}
			d.Journal.SetWitness(dev)
			_ = d.Journal.RetryWitness()
			dev.Run(dctx)
		}()
	}
	status := func() Status {
		mu.Lock()
		dev, ph, fail, next := cur, phase, failure, nextTry
		mu.Unlock()
		c := d.Config.Current().Relay
		st := Status{Configured: c.DeviceID != "", Disabled: c.Disabled, Online: dev != nil && dev.Online(), URL: c.URL, DeviceID: c.DeviceID}
		if c.DeviceID != "" {
			st.Host = deviceHost(c)
			st.MCPURL = "https://" + st.Host
		}
		if c.Disabled {
			return st
		}
		st.Phase, st.Error, st.NextRetry = ph, fail, next
		if dev != nil {
			st.Phase, st.Error = dev.phase()
		}
		if st.Phase == "" && (c.DeviceID != "" || auto) { // конфигурация уже новая, apply ещё не отработал
			st.Phase = PhaseIssuing
			if c.DeviceID == "" {
				st.Phase = PhaseRegistering
			}
		}
		return st
	}
	d.AddRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("POST /v1/relay/register", func(w http.ResponseWriter, r *http.Request) {
			p := daemon.PrincipalFrom(r.Context())
			if !p.IsOwner() {
				daemon.WriteError(w, http.StatusForbidden, "owner_only", "owner only")
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
				daemon.WriteError(w, http.StatusBadRequest, "relay", err.Error())
				return
			}
			daemon.WriteJSON(w, http.StatusOK, res)
		})
		mux.HandleFunc("GET /v1/relay/status", func(w http.ResponseWriter, r *http.Request) {
			st := status()
			out := map[string]any{"configured": st.Configured, "online": st.Online}
			if st.MCPURL != "" {
				out["mcp_url"] = st.MCPURL
			}
			if st.Disabled {
				out["disabled"] = true
			}
			if st.Phase != "" {
				out["phase"] = st.Phase
			}
			if st.Error != "" {
				out["error"] = st.Error
			}
			if !st.NextRetry.IsZero() {
				out["next_retry"] = st.NextRetry.Format(time.RFC3339)
			}
			daemon.WriteJSON(w, http.StatusOK, out)
		})
	})
	apply(d.Config.Current())
	d.Config.Subscribe(apply)
	return status
}

// autoRegister — одна самостоятельная регистрация: на relay.url конфигурации
// (с её ca_file и self_signed — свой relay) или на DefaultURL. Если адрес тем
// временем появился (кнопка, мастер, CLI) или доступ выключили — ничего не делает.
func autoRegister(ctx context.Context, d *daemon.Daemon, r config.Relay) error {
	registerMu.Lock()
	defer registerMu.Unlock()
	if c := d.Config.Current().Relay; c.DeviceID != "" || c.Disabled || ctx.Err() != nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := registerDevice(rctx, d, auth.OwnerVia("daemon"), or(r.URL, DefaultURL), r.CAFile, r.SelfSigned, "relay registration (automatic)")
	return err
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
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
	host := deviceHost(r)
	// Публичный адрес (issuer OAuth и ссылки) — адрес устройства, а не
	// заголовок Host, который задаёт клиент (SNI проверен сертификатом, Host — нет).
	dev := &Device{RelayURL: r.URL, ID: r.DeviceID, Token: tok, RelayTLS: rt,
		Handler: daemon.WithExternalURL(d.PublicHandler(), "https://"+host), Log: d.Log}
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
// новой версией конфигурации (только владелец). Снимает запрет relay.disabled.
func RegisterDevice(ctx context.Context, d *daemon.Daemon, p auth.Principal, relayURL, caFile string, selfSigned bool) (map[string]any, error) {
	if err := p.RequireOwner(); err != nil {
		return nil, err
	}
	registerMu.Lock()
	defer registerMu.Unlock()
	return registerDevice(ctx, d, p, relayURL, caFile, selfSigned, "relay registration")
}

func registerDevice(ctx context.Context, d *daemon.Daemon, p auth.Principal, relayURL, caFile string, selfSigned bool, comment string) (map[string]any, error) {
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
	_, err = d.Config.Edit(p, comment, func(c *config.Config) error {
		c.Relay.URL, c.Relay.DeviceID, c.Relay.Token, c.Relay.CAFile, c.Relay.SelfSigned, c.Relay.Disabled = relayURL, id, "@relay.token", caFile, selfSigned, false
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"device_id": id, "host": host, "mcp_url": "https://" + host}, nil
}

// SetDisabled выключает (on=false) или включает доступ из интернета (только
// владелец). Выключение останавливает туннель, адрес устройства сохраняется:
// повторное включение не тратит новый сертификат. Включение без адреса
// запускает самостоятельную регистрацию (если daemon её ведёт).
func SetDisabled(d *daemon.Daemon, p auth.Principal, disabled bool) error {
	if err := p.RequireOwner(); err != nil {
		return err
	}
	comment := "internet access turned on"
	if disabled {
		comment = "internet access turned off"
	}
	_, err := d.Config.Edit(p, comment, func(c *config.Config) error {
		c.Relay.Disabled = disabled
		return nil
	})
	return err
}

// EnsureRegistered включает доступ из интернета, не создавая лишнего устройства:
// адрес уже есть — снимает запрет; адреса нет — регистрирует на relay.url
// конфигурации или DefaultURL (только владелец).
func EnsureRegistered(ctx context.Context, d *daemon.Daemon, p auth.Principal) (map[string]any, error) {
	if err := p.RequireOwner(); err != nil {
		return nil, err
	}
	registerMu.Lock()
	defer registerMu.Unlock()
	if c := d.Config.Current().Relay; c.DeviceID != "" {
		if c.Disabled {
			if err := SetDisabled(d, p, false); err != nil {
				return nil, err
			}
		}
		host := deviceHost(c)
		return map[string]any{"device_id": c.DeviceID, "host": host, "mcp_url": "https://" + host}, nil
	}
	r := d.Config.Current().Relay
	return registerDevice(ctx, d, p, or(r.URL, DefaultURL), r.CAFile, r.SelfSigned, "relay registration")
}

// NextRetryAt — время следующей попытки (HH:MM по часам этого компьютера) или "".
func (s Status) NextRetryAt() string {
	if s.NextRetry.IsZero() {
		return ""
	}
	return s.NextRetry.Local().Format("15:04")
}
