// SPDX-License-Identifier: FSL-1.1-ALv2

// Package cli — CLI gate4ai: тонкий клиент к daemon. Команды действий
// генерируются из манифестов (каталог daemon), вывод — JSON, коды выхода
// различают отказ, ожидание подтверждения и неподтверждённый результат.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gate4ai/gate4ai/internal/platform/ipc"
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

// Client — HTTP поверх сокета daemon.
type Client struct {
	HTTP  *http.Client
	Token string
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
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://gate4ai"+path, rd)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
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

// NewSocketClient — клиент к сокету path.
func NewSocketClient(path, token string) *Client {
	h := ipc.HTTPClient(path)
	h.Timeout = 0
	return &Client{HTTP: h, Token: token}
}
