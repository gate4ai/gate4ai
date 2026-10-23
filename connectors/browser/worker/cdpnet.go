// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// netLog — запросы вкладки по событиям CDP Network: fetch / XHR для network
// (адрес, метод, статус, тип, id для тела ответа) и незавершённые запросы
// любого типа для «сеть успокоилась». Странице ничего не подменяется —
// перехват ей не виден.
type netLog struct {
	mu       sync.Mutex
	entries  []netEntry
	byID     map[string]int // requestId → индекс в entries
	inflight map[string]bool
	last     time.Time // последнее событие сети
	doc      string    // requestId ответа верхнего документа (тело — DocumentBody)
	frame    string    // id верхнего фрейма вкладки (id цели)
}

type netEntry struct {
	id     string
	req    Request
	done   bool
	failed bool
}

// maxNetEntries — сколько последних fetch / XHR хранить.
const maxNetEntries = 200

func (n *netLog) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.entries, n.byID, n.inflight, n.last, n.doc = nil, map[string]int{}, map[string]bool{}, time.Now(), ""
}

// handle — событие Network.* вкладки.
func (n *netLog) handle(m cdpMsg) {
	var p struct {
		RequestID string `json:"requestId"`
		LoaderID  string `json:"loaderId"`
		FrameID   string `json:"frameId"`
		Type      string `json:"type"`
		Request   struct {
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"request"`
		Response struct {
			URL      string `json:"url"`
			Status   int    `json:"status"`
			MimeType string `json:"mimeType"`
		} `json:"response"`
	}
	if json.Unmarshal(m.Params, &p) != nil || p.RequestID == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.byID == nil {
		n.byID, n.inflight = map[string]int{}, map[string]bool{}
	}
	n.last = time.Now()
	switch m.Method {
	case "Network.requestWillBeSent":
		n.inflight[p.RequestID] = true
		if p.Type == "Document" && p.RequestID == p.LoaderID && (n.frame == "" || p.FrameID == n.frame) {
			n.doc = p.RequestID // переход верхнего документа: id запроса — id загрузчика
		}
		if p.Type != "XHR" && p.Type != "Fetch" {
			return
		}
		kind := "fetch"
		if p.Type == "XHR" {
			kind = "xhr"
		}
		if _, ok := n.byID[p.RequestID]; ok { // редирект — тот же запрос
			n.entries[n.byID[p.RequestID]].req.URL = p.Request.URL
			return
		}
		n.entries = append(n.entries, netEntry{id: p.RequestID, req: Request{Method: p.Request.Method, URL: p.Request.URL, Kind: kind}})
		if len(n.entries) > maxNetEntries {
			n.entries = n.entries[len(n.entries)-maxNetEntries:]
		}
		for i, e := range n.entries {
			n.byID[e.id] = i
		}
	case "Network.responseReceived":
		if i, ok := n.byID[p.RequestID]; ok && i < len(n.entries) {
			e := &n.entries[i]
			e.req.Status, e.req.MIME = p.Response.Status, p.Response.MimeType
			if p.Response.URL != "" {
				e.req.URL = p.Response.URL
			}
		}
	case "Network.loadingFinished", "Network.loadingFailed":
		delete(n.inflight, p.RequestID)
		if i, ok := n.byID[p.RequestID]; ok && i < len(n.entries) {
			n.entries[i].done, n.entries[i].failed = true, m.Method == "Network.loadingFailed"
		}
	}
}

// NetRequests — fetch / XHR вкладки с телами текстовых ответов (до 4000
// символов; тело Chrome хранит, пока страница жива).
func (c *CDP) NetRequests(ctx context.Context) ([]Request, []int) {
	c.net.mu.Lock()
	entries := append([]netEntry(nil), c.net.entries...)
	c.net.mu.Unlock()
	out := make([]Request, 0, len(entries))
	sizes := make([]int, 0, len(entries))
	for _, e := range entries {
		q, size := e.req, 0
		if e.done && !e.failed && textMIME(q.MIME) {
			var b struct {
				Body          string `json:"body"`
				Base64Encoded bool   `json:"base64Encoded"`
			}
			if err := c.call(ctx, c.session, "Network.getResponseBody", map[string]any{"requestId": e.id}, &b); err == nil {
				body := b.Body
				if b.Base64Encoded {
					if raw, err := base64.StdEncoding.DecodeString(body); err == nil {
						body = string(raw)
					}
				}
				size = len([]rune(body))
				if r := []rune(body); len(r) > 4000 {
					body = string(r[:4000])
				}
				q.Body = body
			}
		}
		out = append(out, q)
		sizes = append(sizes, size)
	}
	return out, sizes
}

func textMIME(m string) bool {
	return strings.Contains(m, "json") || strings.HasPrefix(m, "text/plain") || strings.Contains(m, "javascript")
}

// WaitIdle ждёт, пока у вкладки quiet не будет незавершённых запросов и новых
// событий сети, не дольше max (idleWaiter); false — не дождался.
func (c *CDP) WaitIdle(ctx context.Context, quiet, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		c.net.mu.Lock()
		idle := len(c.net.inflight) == 0 && time.Since(c.net.last) >= quiet
		c.net.mu.Unlock()
		if idle {
			return true
		}
		if wait(ctx, 50*time.Millisecond) != nil {
			return false
		}
	}
	return false
}

// Pending — незавершённые запросы вкладки (pendingCounter).
func (c *CDP) Pending() int {
	c.net.mu.Lock()
	defer c.net.mu.Unlock()
	return len(c.net.inflight)
}

// DocumentBody — тело ответа верхнего документа как его прислал сервер
// (docBodier): JSON без просмотрщика Chrome.
func (c *CDP) DocumentBody(ctx context.Context) (string, bool) {
	c.net.mu.Lock()
	id := c.net.doc
	c.net.mu.Unlock()
	if id == "" {
		return "", false
	}
	var b struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := c.call(ctx, c.session, "Network.getResponseBody", map[string]any{"requestId": id}, &b); err != nil {
		return "", false
	}
	if b.Base64Encoded {
		raw, err := base64.StdEncoding.DecodeString(b.Body)
		if err != nil {
			return "", false
		}
		return string(raw), true
	}
	return b.Body, true
}

// responseCatch — перехват ответов страницы (CDP Fetch, стадия Response):
// тело ответа на запрос самой страницы — с её заголовками и токенами,
// которые агенту не видны. Страница получает ответ как обычно.
type responseCatch struct {
	c      *CDP
	paused chan cdpMsg
}

// fetchPattern — шаблон адреса Fetch: подстрока, * и ? экранированы.
func fetchPattern(contains string) string {
	r := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`)
	return "*" + r.Replace(contains) + "*"
}

// CatchResponses начинает перехват ответов на запросы, адрес которых содержит
// contains (responseCatcher); Stop заканчивает его.
func (c *CDP) CatchResponses(ctx context.Context, contains string) (ResponseCatch, error) {
	rc := &responseCatch{c: c, paused: make(chan cdpMsg, 64)}
	c.mu.Lock()
	c.catch = rc
	c.mu.Unlock()
	if err := c.call(ctx, c.session, "Fetch.enable", map[string]any{"patterns": []map[string]any{{"urlPattern": fetchPattern(contains), "requestStage": "Response"}}}, nil); err != nil {
		rc.Stop()
		return nil, err
	}
	return rc, nil
}

// Next — тело первого перехваченного ответа (ok — false, пока его нет); ответ
// с ошибкой (4xx, 5xx, сбой сети) — ошибка; больше max байт — ErrTooLarge.
func (rc *responseCatch) Next(ctx context.Context, max int) (File, bool, error) {
	var m cdpMsg
	select {
	case m = <-rc.paused:
	default:
		return File{}, false, nil
	}
	var p struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"request"`
		Status  int    `json:"responseStatusCode"`
		Error   string `json:"responseErrorReason"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"responseHeaders"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return File{}, false, err
	}
	defer func() {
		_ = rc.c.call(ctx, rc.c.session, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID}, nil)
	}()
	if p.Error != "" {
		return File{}, true, fmt.Errorf("the page's request %s %s failed: %s", p.Request.Method, p.Request.URL, p.Error)
	}
	if p.Status >= 400 || (p.Status >= 300 && p.Status < 400) {
		return File{}, true, fmt.Errorf("the page's request %s %s got status %d", p.Request.Method, p.Request.URL, p.Status)
	}
	f := File{URL: p.Request.URL, Via: "response"}
	for _, h := range p.Headers {
		switch strings.ToLower(h.Name) {
		case "content-type":
			f.MIME = h.Value
		case "content-disposition":
			f.Name = dispositionName(h.Value)
		case "content-length":
			var n int
			if _, err := fmt.Sscan(h.Value, &n); err == nil && n > max {
				return File{}, true, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, n, max)
			}
		}
	}
	var b struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	if err := rc.c.call(ctx, rc.c.session, "Fetch.getResponseBody", map[string]any{"requestId": p.RequestID}, &b); err != nil {
		return File{}, true, fmt.Errorf("the body of %s is not available: %w", p.Request.URL, err)
	}
	f.Data = []byte(b.Body)
	if b.Base64Encoded {
		data, err := base64.StdEncoding.DecodeString(b.Body)
		if err != nil {
			return File{}, true, err
		}
		f.Data = data
	}
	if len(f.Data) > max {
		return File{}, true, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, len(f.Data), max)
	}
	return f, true, nil
}

// Stop — перехват выключен; задержанные запросы продолжают путь.
func (rc *responseCatch) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rc.c.mu.Lock()
	if rc.c.catch == rc {
		rc.c.catch = nil
	}
	rc.c.mu.Unlock()
	for {
		select {
		case m := <-rc.paused:
			var p struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(m.Params, &p) == nil {
				_ = rc.c.call(ctx, rc.c.session, "Fetch.continueRequest", map[string]any{"requestId": p.RequestID}, nil)
			}
			continue
		default:
		}
		break
	}
	_ = rc.c.call(ctx, rc.c.session, "Fetch.disable", nil, nil)
}
