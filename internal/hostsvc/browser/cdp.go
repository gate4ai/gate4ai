// SPDX-License-Identifier: FSL-1.1-ALv2

package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// CDP — движок на прямом Chrome DevTools Protocol (без LLM): переходы, текст,
// снимок элемента. Extract недоступен (нужен Stagehand и модель пользователя).
type CDP struct {
	conn    *websocket.Conn
	session string
	target  string

	mu      sync.Mutex
	next    int64
	pending map[int64]chan cdpMsg
	events  chan cdpMsg
	ctx     context.Context
	cancel  context.CancelFunc
}

type cdpMsg struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// BrowserWSURL — адрес websocket браузера по http://127.0.0.1:<port>.
func BrowserWSURL(ctx context.Context, httpURL string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(httpURL, "/")+"/json/version", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cdp: %w", err)
	}
	defer resp.Body.Close()
	var v struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || v.WS == "" {
		return "", errors.New("cdp: no webSocketDebuggerUrl")
	}
	return v.WS, nil
}

// DialCDP подключается к браузеру и открывает новую вкладку.
func DialCDP(ctx context.Context, httpURL string) (*CDP, error) {
	ws, err := BrowserWSURL(ctx, httpURL)
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, ws, nil)
	if err != nil {
		return nil, fmt.Errorf("cdp dial: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	cctx, cancel := context.WithCancel(context.Background())
	c := &CDP{conn: conn, pending: map[int64]chan cdpMsg{}, events: make(chan cdpMsg, 256), ctx: cctx, cancel: cancel}
	go c.readLoop()
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &t); err != nil {
		c.Close()
		return nil, err
	}
	c.target = t.TargetID
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true}, &a); err != nil {
		c.Close()
		return nil, err
	}
	c.session = a.SessionID
	if err := c.call(ctx, c.session, "Page.enable", nil, nil); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *CDP) readLoop() {
	for {
		_, b, err := c.conn.Read(c.ctx)
		if err != nil {
			c.mu.Lock()
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var m cdpMsg
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		select {
		case c.events <- m:
		default:
		}
	}
}

func (c *CDP) call(ctx context.Context, session, method string, params, out any) error {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan cdpMsg, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if session != "" {
		msg["sessionId"] = session
	}
	b, _ := json.Marshal(msg)
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("cdp %s: %w", method, err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return errors.New("cdp: connection closed")
		}
		if m.Error != nil {
			return fmt.Errorf("cdp %s: %s", method, m.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Navigate переходит и ждёт Page.loadEventFired.
func (c *CDP) Navigate(ctx context.Context, url string) error {
	for len(c.events) > 0 {
		<-c.events
	}
	var r struct {
		ErrorText string `json:"errorText"`
	}
	if err := c.call(ctx, c.session, "Page.navigate", map[string]any{"url": url}, &r); err != nil {
		return err
	}
	if r.ErrorText != "" {
		return fmt.Errorf("navigate: %s", r.ErrorText)
	}
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case ev := <-c.events:
			if ev.Method == "Page.loadEventFired" && (ev.SessionID == "" || ev.SessionID == c.session) {
				return nil
			}
		case <-timeout.C:
			return errors.New("navigate: page load timeout")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *CDP) eval(ctx context.Context, expr string, out any) error {
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := c.call(ctx, c.session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &r); err != nil {
		return err
	}
	if r.Exception != nil {
		return errors.New("evaluate: " + r.Exception.Text)
	}
	return json.Unmarshal(r.Result.Value, out)
}

func (c *CDP) URL(ctx context.Context) (string, error) {
	var s string
	err := c.eval(ctx, "location.href", &s)
	return s, err
}

func (c *CDP) Title(ctx context.Context) (string, error) {
	var s string
	err := c.eval(ctx, "document.title", &s)
	return s, err
}

func (c *CDP) Text(ctx context.Context) (string, error) {
	var s string
	err := c.eval(ctx, "document.body ? document.body.innerText : ''", &s)
	return s, err
}

func (c *CDP) Screenshot(ctx context.Context, selector string) ([]byte, error) {
	sel, _ := json.Marshal(selector)
	var box struct{ X, Y, W, H float64 }
	if err := c.eval(ctx, `(() => { const e = document.querySelector(`+string(sel)+`); if (!e) return null;
		const r = e.getBoundingClientRect(); return {X: r.x + scrollX, Y: r.y + scrollY, W: r.width, H: r.height}; })()`, &box); err != nil {
		return nil, err
	}
	if box.W == 0 || box.H == 0 {
		return nil, fmt.Errorf("element %s not found or empty", selector)
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := c.call(ctx, c.session, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": true,
		"clip": map[string]any{"x": box.X, "y": box.Y, "width": box.W, "height": box.H, "scale": 1}}, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

func (c *CDP) Extract(context.Context, string) (any, error) {
	return nil, errors.New("extract needs engine: stagehand and a model configured by the user")
}

// Close закрывает вкладку и соединение (сам Chrome продолжает работать).
func (c *CDP) Close() error {
	if c.target != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": c.target}, nil)
		cancel()
	}
	c.cancel()
	return c.conn.Close(websocket.StatusNormalClosure, "")
}
