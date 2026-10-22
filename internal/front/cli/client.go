// SPDX-License-Identifier: AGPL-3.0-only

// Package cli — CLI gate4ai: тонкий клиент к daemon. Команды действий
// генерируются из манифестов (каталог daemon), вывод — JSON, коды выхода
// различают отказ, ожидание подтверждения и неподтверждённый результат.
package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
)

// Коды выхода.
const (
	ExitOK           = 0
	ExitError        = 1
	ExitDenied       = 2
	ExitPending      = 3
	ExitVerification = 4
	ExitUsage        = 5
	ExitUnavailable  = 6
)

// Client — HTTP к daemon: поверх сокета или по HTTPS к публичному адресу устройства.
type Client struct {
	HTTP  *http.Client
	Token string
	Base  string // "" — сокет; иначе https://<устройство> (удалённый агент)
	// Addr — путь сокета / pipe для сообщений об ошибке (у трея меняется между подключениями).
	Addr func() string
	// Err — ошибка настройки клиента (неверный GATE4AI_URL): возвращается из Do.
	Err error
	// Kind — кто клиент для «последнего обращения» в Web UI: "cli" (по умолчанию) или "mcp".
	Kind string
}

// ErrUnavailable — daemon недоступен («устройство офлайн»).
var ErrUnavailable = errors.New("gate4ai daemon is not running or not reachable")

// APIError — ошибка API daemon.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	Body    []byte `json:"-"` // тело ответа (подробности, например план установки)
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// Do выполняет запрос; out — для JSON-ответа, raw — для бинарного.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	if c.Err != nil {
		return c.Err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	base := c.Base
	if base == "" {
		base = "http://gate4ai"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
		kind := c.Kind
		if kind == "" {
			kind = "cli"
		}
		req.Header.Set("X-Gate4ai-Client", kind)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ue := &UnavailableError{Remote: c.Base != "", Err: err}
		var ne *net.OpError
		if errors.As(err, &ne) {
			ue.Err = ne.Err // без «Post "http://gate4ai/…": dial unix …»
		}
		switch {
		case ue.Remote:
			ue.Addr = c.Base
		case c.Addr != nil:
			ue.Addr = c.Addr()
		}
		return ue
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error APIError `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		e.Error.Status, e.Error.Body = resp.StatusCode, b
		if e.Error.Code == "" {
			e.Error.Code, e.Error.Message = "http_error", resp.Status
		}
		return &e.Error
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = b
		return nil
	default:
		return json.Unmarshal(b, out)
	}
}

// NewRemoteClient — клиент агента к публичному адресу устройства (relay или
// прямой HTTPS); caFile — свой CA (self-hosted relay, тесты), "" — системные.
func NewRemoteClient(base, token, caFile string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("GATE4AI_URL must be https://<device address>, got %q", base)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates in %s", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{HTTP: &http.Client{Transport: tr}, Token: token, Base: strings.TrimSuffix(u.Scheme+"://"+u.Host, "/")}, nil
}

// NewSocketClient — клиент к сокету path.
func NewSocketClient(path, token string) *Client {
	h := ipc.HTTPClient(path)
	h.Timeout = 0
	return &Client{HTTP: h, Token: token, Addr: func() string { return path }}
}

// NewOwnerClient — клиент к сокету владельца, путь которого ищется при каждом
// подключении (paths.FindOwnerSocket): трей живёт дольше daemon.
func NewOwnerClient() *Client {
	h := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return ipc.Dial(ctx, paths.FindOwnerSocket())
		},
		MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second,
	}}
	return &Client{HTTP: h, Addr: paths.FindOwnerSocket}
}
