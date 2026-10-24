// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Frame — кадр просмотра сеанса: JPEG того, что видно во вкладке, и её
// размер в CSS-пикселях (по нему ввод переводится в координаты страницы).
type Frame struct {
	JPEG   []byte  `json:"-"`
	Width  float64 `json:"width"`  // ширина области страницы, CSS px
	Height float64 `json:"height"` // высота, CSS px
	URL    string  `json:"url,omitempty"`
	At     time.Time
}

// ViewOptions — качество кадров.
type ViewOptions struct {
	MaxWidth  int // 0 — ViewMaxWidth
	MaxHeight int // 0 — ViewMaxHeight
	Quality   int // JPEG 1..100, 0 — ViewQuality
}

// Пределы просмотра по умолчанию: телефон и ноутбук, около 50–150 КБ на кадр.
const (
	ViewMaxWidth  = 1280
	ViewMaxHeight = 1600
	ViewQuality   = 60
)

// Input — действие человека в просмотре. Координаты — доли области страницы
// (0..1 по ширине и высоте кадра), так они не зависят от масштаба картинки
// на экране зрителя.
type Input struct {
	Type      string  `json:"type"`             // mouse | wheel | key | text
	Action    string  `json:"action,omitempty"` // mouse: down | up | move; key: down | up
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	Button    string  `json:"button,omitempty"` // left (по умолчанию) | middle | right
	Clicks    int     `json:"clicks,omitempty"`
	DeltaX    float64 `json:"dx,omitempty"`
	DeltaY    float64 `json:"dy,omitempty"`
	Key       string  `json:"key,omitempty"`  // KeyboardEvent.key
	Code      string  `json:"code,omitempty"` // KeyboardEvent.code
	KeyCode   int     `json:"keyCode,omitempty"`
	Text      string  `json:"text,omitempty"`      // text: вставить; key: символ клавиши
	Modifiers int     `json:"modifiers,omitempty"` // Alt=1, Ctrl=2, Meta=4, Shift=8
}

// MaxInputText — предел вставки текста одним действием.
const MaxInputText = 64 << 10

// View — просмотр вкладки: своё подключение CDP к ней (сеансу агента оно
// не мешает), кадры Page.startScreencast и ввод Input.dispatch*.
type View struct {
	c *CDP

	mu   sync.Mutex
	w, h float64 // последний размер области страницы
	url  string
}

// targeter — движок знает id цели своей вкладки.
type targeter interface {
	TargetID() string
}

// TargetID — id цели вкладки сеанса ("" — движок без CDP).
func (ss *Session) TargetID() string {
	if t, ok := ss.Executor.Engine.(targeter); ok {
		return t.TargetID()
	}
	return ""
}

// ErrNoView — у сеанса нет вкладки, которую можно показать (фейковый движок).
var ErrNoView = errors.New("this browser session cannot be shown")

// View открывает просмотр вкладки сеанса: onFrame получает каждый кадр
// (вызов не должен ждать: медленный зритель пропускает кадры, а не держит
// Chrome — следующий кадр Chrome пришлёт только после подтверждения).
func (ss *Session) View(ctx context.Context, o ViewOptions, onFrame func(Frame)) (*View, error) {
	target := ss.TargetID()
	ss.s.mu.Lock()
	cdp := ss.s.cdp
	ss.s.mu.Unlock()
	if target == "" || cdp == "" {
		return nil, ErrNoView
	}
	return OpenView(ctx, cdp, target, o, onFrame)
}

// OpenView — просмотр вкладки target Chrome по адресу DevTools cdp.
func OpenView(ctx context.Context, cdp, target string, o ViewOptions, onFrame func(Frame)) (*View, error) {
	if o.MaxWidth <= 0 {
		o.MaxWidth = ViewMaxWidth
	}
	if o.MaxHeight <= 0 {
		o.MaxHeight = ViewMaxHeight
	}
	if o.Quality <= 0 || o.Quality > 100 {
		o.Quality = ViewQuality
	}
	v := &View{}
	c, err := dialCDP(ctx, cdp, tabOpts{target: target, hook: func(m cdpMsg) bool { return v.event(m, onFrame) }})
	if err != nil {
		return nil, err
	}
	v.c = c
	var u string
	if c.Eval(ctx, "location.href", &u) == nil {
		v.mu.Lock()
		v.url = u
		v.mu.Unlock()
	}
	if err := c.call(ctx, c.session, "Page.startScreencast", map[string]any{"format": "jpeg", "quality": o.Quality,
		"maxWidth": o.MaxWidth, "maxHeight": o.MaxHeight, "everyNthFrame": 1}, nil); err != nil {
		c.Close()
		return nil, err
	}
	return v, nil
}

// event — кадры и адрес вкладки (CDP.hook).
func (v *View) event(m cdpMsg, onFrame func(Frame)) bool {
	switch m.Method {
	case "Page.screencastFrame":
		var p struct {
			Data      string `json:"data"`
			SessionID int    `json:"sessionId"`
			Metadata  struct {
				DeviceWidth  float64 `json:"deviceWidth"`
				DeviceHeight float64 `json:"deviceHeight"`
			} `json:"metadata"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			return true
		}
		// Подтверждение — сразу: Chrome шлёт следующий кадр только после него.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = v.c.call(ctx, v.c.session, "Page.screencastFrameAck", map[string]any{"sessionId": p.SessionID}, nil)
		}()
		img, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			return true
		}
		v.mu.Lock()
		v.w, v.h = p.Metadata.DeviceWidth, p.Metadata.DeviceHeight
		u := v.url
		v.mu.Unlock()
		onFrame(Frame{JPEG: img, Width: p.Metadata.DeviceWidth, Height: p.Metadata.DeviceHeight, URL: u, At: time.Now()})
		return true
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				ParentID string `json:"parentId"`
				URL      string `json:"url"`
			} `json:"frame"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.Frame.ParentID == "" {
			v.mu.Lock()
			v.url = p.Frame.URL
			v.mu.Unlock()
		}
		return true
	case "Page.navigatedWithinDocument":
		var p struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			v.mu.Lock()
			v.url = p.URL
			v.mu.Unlock()
		}
		return true
	}
	return false
}

// Closed — подключение просмотра потеряно (вкладку закрыли, Chrome упал).
func (v *View) Closed() bool { return v.c.lost() }

// Close останавливает кадры и закрывает подключение (вкладка остаётся).
func (v *View) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = v.c.call(ctx, v.c.session, "Page.stopScreencast", nil, nil)
	cancel()
	_ = v.c.Close()
}

// buttons — CDP-кнопка и маска buttons для mouseMoved.
var buttons = map[string]struct {
	name string
	mask int
}{"": {"left", 1}, "left": {"left", 1}, "right": {"right", 2}, "middle": {"middle", 4}}

// CheckInput проверяет действие до исполнения.
func CheckInput(in Input) error {
	switch in.Type {
	case "mouse":
		switch in.Action {
		case "down", "up", "move":
		default:
			return fmt.Errorf("input: mouse action %q (down, up, move)", in.Action)
		}
		if _, ok := buttons[in.Button]; !ok {
			return fmt.Errorf("input: button %q (left, middle, right)", in.Button)
		}
	case "wheel":
	case "key":
		if in.Action != "down" && in.Action != "up" {
			return fmt.Errorf("input: key action %q (down, up)", in.Action)
		}
		if in.Key == "" {
			return errors.New("input: key is required")
		}
	case "text":
		if len(in.Text) > MaxInputText {
			return fmt.Errorf("input: text is longer than %d bytes", MaxInputText)
		}
	default:
		return fmt.Errorf("input: type %q (mouse, wheel, key, text)", in.Type)
	}
	if bad(in.X) || bad(in.Y) || bad(in.DeltaX) || bad(in.DeltaY) || in.X < 0 || in.X > 1 || in.Y < 0 || in.Y > 1 {
		return errors.New("input: x and y are fractions of the page area, 0..1")
	}
	if in.Modifiers < 0 || in.Modifiers > 15 || in.Clicks < 0 || in.Clicks > 3 {
		return errors.New("input: bad modifiers or clicks")
	}
	return nil
}

func bad(f float64) bool { return math.IsNaN(f) || math.IsInf(f, 0) }

// Input исполняет действие человека на вкладке.
func (v *View) Input(ctx context.Context, in Input) error {
	if err := CheckInput(in); err != nil {
		return err
	}
	v.mu.Lock()
	x, y := in.X*v.w, in.Y*v.h
	v.mu.Unlock()
	switch in.Type {
	case "mouse":
		b := buttons[in.Button]
		p := map[string]any{"x": x, "y": y, "modifiers": in.Modifiers}
		switch in.Action {
		case "down":
			p["type"], p["button"], p["clickCount"], p["buttons"] = "mousePressed", b.name, max(in.Clicks, 1), b.mask
		case "up":
			p["type"], p["button"], p["clickCount"] = "mouseReleased", b.name, max(in.Clicks, 1)
		case "move":
			p["type"] = "mouseMoved"
			if in.Clicks > 0 { // перетаскивание: кнопка зажата
				p["button"], p["buttons"] = b.name, b.mask
			}
		}
		return v.c.call(ctx, v.c.session, "Input.dispatchMouseEvent", p, nil)
	case "wheel":
		return v.c.call(ctx, v.c.session, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": x, "y": y,
			"deltaX": in.DeltaX, "deltaY": in.DeltaY, "modifiers": in.Modifiers}, nil)
	case "text":
		if in.Text == "" {
			return nil
		}
		return v.c.call(ctx, v.c.session, "Input.insertText", map[string]any{"text": in.Text}, nil)
	}
	p := map[string]any{"key": in.Key, "code": in.Code, "windowsVirtualKeyCode": in.KeyCode, "nativeVirtualKeyCode": in.KeyCode, "modifiers": in.Modifiers}
	if in.Action == "up" {
		p["type"] = "keyUp"
	} else if in.Text != "" && in.Modifiers&^8 == 0 {
		p["type"], p["text"], p["unmodifiedText"] = "keyDown", in.Text, in.Text // печатный символ (с Shift — тоже)
	} else {
		p["type"] = "rawKeyDown" // Enter, стрелки, сочетания с Ctrl / Alt / Meta
		if in.Key == "Enter" {
			p["type"], p["text"] = "keyDown", "\r"
		}
	}
	return v.c.call(ctx, v.c.session, "Input.dispatchKeyEvent", p, nil)
}
