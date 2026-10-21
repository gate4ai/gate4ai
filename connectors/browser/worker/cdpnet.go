// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	n.entries, n.byID, n.inflight, n.last = nil, map[string]int{}, map[string]bool{}, time.Now()
}

// handle — событие Network.* вкладки.
func (n *netLog) handle(m cdpMsg) {
	var p struct {
		RequestID string `json:"requestId"`
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
func (c *CDP) NetRequests(ctx context.Context) ([]Request, []int, error) {
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
	return out, sizes, nil
}

func textMIME(m string) bool {
	return strings.Contains(m, "json") || strings.HasPrefix(m, "text/plain") || strings.Contains(m, "javascript")
}

// WaitIdle ждёт, пока у вкладки quiet не будет незавершённых запросов и новых
// событий сети, не дольше max (idleWaiter).
func (c *CDP) WaitIdle(ctx context.Context, quiet, max time.Duration) {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		c.net.mu.Lock()
		idle := len(c.net.inflight) == 0 && time.Since(c.net.last) >= quiet
		c.net.mu.Unlock()
		if idle {
			return
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}
