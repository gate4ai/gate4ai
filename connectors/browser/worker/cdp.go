// SPDX-License-Identifier: AGPL-3.0-only

package worker

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
	closed  error // соединение с Chrome потеряно (окно закрыли, Chrome упал)
	events  chan cdpMsg
	ctx     context.Context
	cancel  context.CancelFunc
	net     netLog
	owned   bool   // вкладку открыл движок: закрыть её в Close
	http    string // адрес DevTools (http://127.0.0.1:<port>) — для NewTab
	dialogs        // JS-диалоги вкладки (Dialoger)
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
	return dialCDP(ctx, httpURL, tabOpts{})
}

// NewTab — ещё одна вкладка того же Chrome (тот же профиль и cookie) со своим
// подключением; Close закрывает её. background — в фоне (временная вкладка
// одноразового вызова: страница сессии остаётся впереди), иначе — впереди
// (вкладка сеанса: человек видит, что в ней происходит).
func (c *CDP) NewTab(ctx context.Context, background bool) (Engine, error) {
	return dialCDP(ctx, c.http, tabOpts{new: true, background: background})
}

// AttachCDP — подключение к уже открытой вкладке targetID (её ведёт другой
// движок): диалоги вкладки, переключение на неё, проверка, что Chrome жив.
// Вкладку Close не закрывает.
func AttachCDP(ctx context.Context, httpURL, targetID string) (*CDP, error) {
	return dialCDP(ctx, httpURL, tabOpts{target: targetID})
}

// tabOpts — к какой вкладке подключаться: new — новая (background — в фоне),
// target — к этой; иначе — пустая стартовая вкладка Chrome или новая.
type tabOpts struct {
	new, background bool
	target          string
}

// dialCDP — подключение к вкладке.
func dialCDP(ctx context.Context, httpURL string, o tabOpts) (*CDP, error) {
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
	c := &CDP{conn: conn, pending: map[int64]chan cdpMsg{}, events: make(chan cdpMsg, 256), ctx: cctx, cancel: cancel, http: httpURL, target: o.target}
	c.dialogs.answer = func(ctx context.Context, accept bool, text string) error {
		p := map[string]any{"accept": accept}
		if text != "" {
			p["promptText"] = text
		}
		return c.call(ctx, c.session, "Page.handleJavaScriptDialog", p, nil)
	}
	go c.readLoop()
	// Пустая стартовая вкладка запущенного Chrome — рабочая: вторая вкладка не
	// нужна. Нет такой (чужой Chrome по cdp_url) или вкладка временная — своя.
	var list struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
			Attached bool   `json:"attached"`
		} `json:"targetInfos"`
	}
	if !o.new && o.target == "" && c.call(ctx, "", "Target.getTargets", nil, &list) == nil {
		for _, t := range list.TargetInfos {
			if t.Type == "page" && t.URL == "about:blank" && !t.Attached {
				c.target = t.TargetID
				break
			}
		}
	}
	if c.target == "" {
		var t struct {
			TargetID string `json:"targetId"`
		}
		if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "background": o.background}, &t); err != nil {
			c.Close()
			return nil, err
		}
		c.target, c.owned = t.TargetID, true
	}
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": c.target, "flatten": true}, &a); err != nil {
		c.Close()
		return nil, err
	}
	c.session = a.SessionID
	c.net.mu.Lock()
	c.net.frame = c.target // id верхнего фрейма вкладки — id её цели
	c.net.mu.Unlock()
	if err := c.call(ctx, c.session, "Page.enable", nil, nil); err != nil {
		c.Close()
		return nil, err
	}
	if o.target != "" {
		return c, nil // вкладку ведёт другой движок: сеть и перехват он ставит сам
	}
	// Обработчики click страницы — для observe (до её скриптов, в каждом документе).
	_ = c.call(ctx, c.session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": clickHookJS}, nil)
	c.net.reset()
	if err := c.call(ctx, c.session, "Network.enable", map[string]any{"maxResourceBufferSize": 4 << 20}, nil); err != nil {
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
			c.closed = fmt.Errorf("cdp: connection to Chrome lost: %w", err)
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
		if strings.HasPrefix(m.Method, "Network.") {
			if m.SessionID == "" || m.SessionID == c.session {
				c.net.handle(m)
			}
			continue
		}
		if strings.HasPrefix(m.Method, "Page.javascriptDialog") {
			if m.SessionID == c.session {
				c.dialogs.event(m)
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
	if c.closed != nil {
		// Ответа не будет: иначе вызов ждал бы его до конца ctx (а у нативного
		// пакета ctx без срока — вызов висел бы).
		err := c.closed
		c.mu.Unlock()
		return err
	}
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

// NavigateTimeout — сколько ждать Page.loadEventFired; дальше переход
// считается состоявшимся (страница входа с редиректами грузится долго, а
// адрес нужен сразу).
var NavigateTimeout = 15 * time.Second

// Navigate переходит и ждёт Page.loadEventFired (не дольше NavigateTimeout).
func (c *CDP) Navigate(ctx context.Context, url string) error {
	for len(c.events) > 0 {
		<-c.events
	}
	c.net.reset()
	var r struct {
		ErrorText string `json:"errorText"`
	}
	if err := c.call(ctx, c.session, "Page.navigate", map[string]any{"url": url}, &r); err != nil {
		return err
	}
	if r.ErrorText != "" {
		return fmt.Errorf("navigate: %s", r.ErrorText)
	}
	timeout := time.NewTimer(NavigateTimeout)
	defer timeout.Stop()
	for {
		select {
		case ev := <-c.events:
			if ev.Method == "Page.loadEventFired" && (ev.SessionID == "" || ev.SessionID == c.session) {
				return nil
			}
		case <-timeout.C:
			return nil // загрузка не закончилась — адрес и страница всё равно доступны
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Eval — Runtime.evaluate по значению.
func (c *CDP) Eval(ctx context.Context, expr string, out any) error {
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := c.call(ctx, c.session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true}, &r); err != nil {
		return err
	}
	if r.Exception != nil {
		return errors.New("evaluate: " + r.Exception.Text)
	}
	return json.Unmarshal(r.Result.Value, out)
}

// InsertText — Input.insertText в элемент в фокусе.
func (c *CDP) InsertText(ctx context.Context, text string) error {
	return c.call(ctx, c.session, "Input.insertText", map[string]any{"text": text}, nil)
}

// Key — нажатие клавиши или сочетания (keyDown + keyUp) с модификаторами.
func (c *CDP) Key(ctx context.Context, name string) error {
	k, err := ParseKey(name)
	if err != nil {
		return err
	}
	return c.key(ctx, k)
}

// Type — каждый символ отдельным нажатием (keydown, keypress, input, keyup),
// между символами — пауза delay.
func (c *CDP) Type(ctx context.Context, text string, delay time.Duration) error {
	for i, r := range []rune(text) {
		if i > 0 && delay > 0 {
			if err := wait(ctx, delay); err != nil {
				return err
			}
		}
		if err := c.key(ctx, charKey(r)); err != nil {
			return err
		}
	}
	return nil
}

func (c *CDP) key(ctx context.Context, k Key) error {
	key, code, vk, text, mods := k.cdpKey()
	down := map[string]any{"type": "rawKeyDown", "key": key, "code": code, "windowsVirtualKeyCode": vk, "modifiers": mods}
	if text != "" {
		down["type"], down["text"] = "keyDown", text
	}
	if err := c.call(ctx, c.session, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return c.call(ctx, c.session, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key, "code": code, "windowsVirtualKeyCode": vk, "modifiers": mods}, nil)
}

func (c *CDP) URL(ctx context.Context) (string, error) {
	var s string
	err := c.Eval(ctx, "location.href", &s)
	return s, err
}

func (c *CDP) Title(ctx context.Context) (string, error) {
	var s string
	err := c.Eval(ctx, "document.title", &s)
	return s, err
}

func (c *CDP) Text(ctx context.Context) (string, error) {
	var s string
	err := c.Eval(ctx, visibleTextJS+"(document.body)", &s)
	return s, err
}

func (c *CDP) Screenshot(ctx context.Context, clip *Clip, full bool) ([]byte, error) {
	p := map[string]any{"format": "png"}
	if clip != nil {
		if clip.W <= 0 || clip.H <= 0 {
			return nil, errors.New("screenshot: empty area")
		}
		p["captureBeyondViewport"] = true // элемент и страница могут быть больше окна
		p["clip"] = map[string]any{"x": clip.X, "y": clip.Y, "width": clip.W, "height": clip.H, "scale": 1}
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := c.call(ctx, c.session, "Page.captureScreenshot", p, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

func (c *CDP) Extract(context.Context, string) (any, error) {
	return nil, errors.New("extract needs engine: stagehand and a model configured by the user")
}

// Ping — Chrome отвечает (запрос к браузеру, а не к странице: открытый на
// ней диалог не мешает).
func (c *CDP) Ping(ctx context.Context) error {
	return c.call(ctx, "", "Browser.getVersion", nil, nil)
}

// Activate — вкладка впереди (handoff: человек видит её).
func (c *CDP) Activate(ctx context.Context) error {
	return c.call(ctx, "", "Target.activateTarget", map[string]any{"targetId": c.target}, nil)
}

// QuitBrowser закрывает Chrome штатно (Browser.close): профиль и cookies
// сохраняются на диск.
func (c *CDP) QuitBrowser(ctx context.Context) {
	_ = c.call(ctx, "", "Browser.close", nil, nil)
}

// Close закрывает вкладку и соединение (сам Chrome продолжает работать).
func (c *CDP) Close() error {
	if c.target != "" && c.owned {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = c.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": c.target}, nil)
		cancel()
	}
	c.cancel()
	return c.conn.Close(websocket.StatusNormalClosure, "")
}
