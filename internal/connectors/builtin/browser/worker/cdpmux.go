// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Mux — порт отладки Chrome закрыт: Chrome слушает CDP только в pipe
// (--remote-debugging-pipe), а клиентам — нашим подключениям, Stagehand SDK
// и его расширению внутри Chrome — Mux отдаёт websocket на 127.0.0.1 под
// адресом с секретным токеном (/t/<token>/…). Без токена — 404: другой
// пользователь ОС или страница в браузере, которые видят порт, к Chrome не
// подключатся.
//
// В pipe одно подключение к Chrome, а клиентов много: Mux разводит их так,
// как разводил бы Chrome. id запросов — свои у каждого клиента; сессия
// (sessionId) принадлежит клиенту, который к ней подключился (attachToTarget
// или auto-attach), и её события идут только ему; события целей
// (Target.targetCreated…) — клиентам, включившим setDiscoverTargets;
// auto-attach браузера — клиентам, включившим его; прочие события браузера
// (загрузки) — всем. Отключившийся клиент отцепляется от своих сессий.
//
// Chrome в headless-режиме Mux ещё и маскирует: на каждую вкладку и iframe
// ставит Emulation.setUserAgentOverride с UA без «HeadlessChrome» и полными
// метаданными client hints (снятыми со страницы 127.0.0.1 — безопасный
// контекст), до того как клиент пустит вкладку дальше.
type Mux struct {
	w     io.Writer // в Chrome (его fd 3)
	token string
	ln    net.Listener
	srv   *http.Server
	// toChrome — очередь записи в pipe: чтение событий Chrome (route) само
	// пишет в Chrome (маска UA) и не должно ждать, пока Chrome прочитает.
	toChrome chan []byte

	mu         sync.Mutex
	next       int64
	pending    map[int64]muxPending
	clients    map[*muxClient]bool
	owner      map[string]*muxClient // сессия → клиент
	attaching  map[string][]*muxClient
	discover   map[*muxClient]bool
	autoAttach map[*muxClient]json.RawMessage // клиент → его параметры setAutoAttach браузера
	targets    map[string]json.RawMessage     // известные цели (targetInfo) для setDiscoverTargets
	version    map[string]any                 // Browser.getVersion
	ua         map[string]any                 // headless: параметры setUserAgentOverride (nil — не нужно)
	closed     bool
	done       chan struct{}
}

type muxPending struct {
	c      *muxClient // nil — внутренний запрос Mux
	id     json.RawMessage
	method string
	target string // attachToTarget: какая цель
	reply  chan cdpMsg
}

type muxClient struct {
	m    *Mux
	out  chan []byte
	conn *websocket.Conn
	gone chan struct{}
	once sync.Once
}

// muxIn — сообщение клиента (id может быть любым JSON-числом).
type muxIn struct {
	ID        json.RawMessage `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// muxRaw — сообщение Chrome как есть, с разобранными полями маршрута.
type muxRaw struct {
	ID        *int64          `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
}

// MuxClientQueue — сколько сообщений ждёт медленного клиента; больше —
// клиент отключается (иначе он держал бы всех).
const MuxClientQueue = 8192

// StartMux — Mux поверх pipe Chrome (r — его fd 4, w — fd 3): слушает
// 127.0.0.1 на свободном порту. Адрес для клиентов — URL().
func StartMux(ctx context.Context, r io.Reader, w io.Writer) (*Mux, error) {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	m := &Mux{w: w, token: hex.EncodeToString(raw), ln: ln, pending: map[int64]muxPending{}, clients: map[*muxClient]bool{},
		owner: map[string]*muxClient{}, attaching: map[string][]*muxClient{}, discover: map[*muxClient]bool{},
		autoAttach: map[*muxClient]json.RawMessage{}, targets: map[string]json.RawMessage{}, done: make(chan struct{}),
		toChrome: make(chan []byte, MuxClientQueue)}
	go m.read(r)
	go m.writeLoop()
	m.srv = &http.Server{Handler: http.HandlerFunc(m.serveHTTP), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = m.srv.Serve(ln) }()
	if err := m.init(ctx); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// URL — адрес DevTools для клиентов: http://127.0.0.1:<port>/t/<token>
// (клиенты дописывают /json/version).
func (m *Mux) URL() string { return "http://" + m.ln.Addr().String() + "/t/" + m.token }

func (m *Mux) wsURL() string {
	return "ws://" + m.ln.Addr().String() + "/t/" + m.token + "/devtools/browser"
}

// Done закрывается, когда Chrome закрыл pipe (вышел).
func (m *Mux) Done() <-chan struct{} { return m.done }

// Close отключает клиентов и закрывает адрес.
func (m *Mux) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cs := make([]*muxClient, 0, len(m.clients))
	for c := range m.clients {
		cs = append(cs, c)
	}
	for id, p := range m.pending {
		if p.reply != nil {
			close(p.reply)
		}
		delete(m.pending, id)
	}
	m.mu.Unlock()
	_ = m.srv.Close()
	for _, c := range cs {
		c.close()
	}
	select {
	case <-m.done:
	default:
		close(m.done)
	}
}

// init — версия браузера, обнаружение целей (для типов вкладок и
// setDiscoverTargets клиентов) и, для headless, параметры маскировки UA.
func (m *Mux) init(ctx context.Context) error {
	var v map[string]any
	if err := m.call(ctx, "Browser.getVersion", nil, "", &v); err != nil {
		return fmt.Errorf("chrome pipe: %w", err)
	}
	m.mu.Lock()
	m.version = v
	m.mu.Unlock()
	if err := m.call(ctx, "Target.setDiscoverTargets", map[string]any{"discover": true}, "", nil); err != nil {
		return err
	}
	// --headless=new отвечает product «Chrome/…», а выдаёт себя в userAgent.
	if agent, _ := v["userAgent"].(string); strings.Contains(agent, "HeadlessChrome/") {
		ua, err := m.probeUA(ctx, v)
		if err != nil {
			return fmt.Errorf("headless user agent: %w", err)
		}
		m.mu.Lock()
		m.ua = ua
		m.mu.Unlock()
	}
	return nil
}

// probeUA — UA без «HeadlessChrome» и метаданные client hints: страница на
// 127.0.0.1 (безопасный контекст, где есть navigator.userAgentData) во
// временной фоновой вкладке.
func (m *Mux) probeUA(ctx context.Context, v map[string]any) (map[string]any, error) {
	var t struct {
		TargetID string `json:"targetId"`
	}
	if err := m.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank", "background": true}, "", &t); err != nil {
		return nil, err
	}
	defer func() {
		_ = m.call(context.Background(), "Target.closeTarget", map[string]any{"targetId": t.TargetID}, "", nil)
	}()
	var a struct {
		SessionID string `json:"sessionId"`
	}
	if err := m.call(ctx, "Target.attachToTarget", map[string]any{"targetId": t.TargetID, "flatten": true}, "", &a); err != nil {
		return nil, err
	}
	if err := m.call(ctx, "Page.navigate", map[string]any{"url": m.URL() + "/probe"}, a.SessionID, nil); err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Value map[string]any `json:"value"`
		} `json:"result"`
	}
	expr := `new Promise(r => { const go = () => navigator.userAgentData.getHighEntropyValues(["architecture","bitness","fullVersionList","model","platformVersion","wow64"]).then(r);
		document.readyState === "complete" ? go() : addEventListener("load", go) })`
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := m.call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true}, a.SessionID, &r)
		if err == nil && r.Result.Value != nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the probe page did not load")
		}
		if err := wait(ctx, 200*time.Millisecond); err != nil {
			return nil, err
		}
	}
	h := r.Result.Value
	ua, _ := v["userAgent"].(string)
	md := map[string]any{"platform": h["platform"], "platformVersion": h["platformVersion"], "architecture": h["architecture"],
		"model": h["model"], "mobile": h["mobile"], "bitness": h["bitness"], "wow64": h["wow64"],
		"brands": h["brands"], "fullVersionList": h["fullVersionList"]}
	return map[string]any{"userAgent": strings.Replace(ua, "HeadlessChrome/", "Chrome/", 1), "userAgentMetadata": md}, nil
}

// Headless — Chrome запущен без экрана (маскировка UA включена).
func (m *Mux) Headless() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ua != nil
}

// call — внутренний запрос Mux к Chrome.
func (m *Mux) call(ctx context.Context, method string, params any, session string, out any) error {
	ch := make(chan cdpMsg, 1)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("chrome pipe closed")
	}
	m.next++
	id := m.next
	m.pending[id] = muxPending{method: method, reply: ch}
	m.mu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if session != "" {
		msg["sessionId"] = session
	}
	b, _ := json.Marshal(msg)
	if err := m.write(b); err != nil {
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return errors.New("chrome pipe closed")
		}
		if r.Error != nil {
			return fmt.Errorf("cdp %s: %s", method, r.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// notifyInternal — внутренний запрос без ожидания ответа.
func (m *Mux) notifyInternal(method string, params any, session string) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.next++
	id := m.next
	m.pending[id] = muxPending{method: method}
	m.mu.Unlock()
	msg := map[string]any{"id": id, "method": method, "params": params}
	if session != "" {
		msg["sessionId"] = session
	}
	b, _ := json.Marshal(msg)
	_ = m.write(b)
}

// write ставит сообщение в очередь записи в Chrome.
func (m *Mux) write(b []byte) error {
	select {
	case <-m.done:
		return errors.New("chrome pipe closed")
	case m.toChrome <- append(b, 0):
		return nil
	}
}

func (m *Mux) writeLoop() {
	for {
		select {
		case <-m.done:
			return
		case b := <-m.toChrome:
			if _, err := m.w.Write(b); err != nil {
				fmt.Fprintln(os.Stderr, "browser: chrome pipe:", err)
				m.Close()
				return
			}
		}
	}
}

// read — сообщения Chrome (через \0) до конца pipe.
func (m *Mux) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		b, err := br.ReadBytes(0)
		if len(b) > 1 {
			m.route(b[:len(b)-1])
		}
		if err != nil {
			m.Close()
			return
		}
	}
}

// route — сообщение Chrome нужному клиенту.
func (m *Mux) route(b []byte) {
	var msg muxRaw
	if json.Unmarshal(b, &msg) != nil {
		return
	}
	if msg.ID != nil {
		m.reply(msg, b)
		return
	}
	var to []*muxClient
	m.mu.Lock()
	switch {
	case msg.Method == "Target.attachedToTarget":
		var p struct {
			SessionID  string          `json:"sessionId"`
			TargetInfo json.RawMessage `json:"targetInfo"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		var c *muxClient
		if c0 := m.owner[p.SessionID]; c0 != nil {
			c = c0 // ответ attachToTarget пришёл раньше события
		} else if msg.SessionID != "" {
			c = m.owner[msg.SessionID] // дочерняя сессия (iframe, popup) — у владельца родительской
		} else {
			info := targetOf(p.TargetInfo)
			if w := m.attaching[info.ID]; len(w) > 0 {
				c = w[0] // ответ attachToTarget этого клиента ещё в пути
			} else {
				for ac := range m.autoAttach {
					c = ac
					break
				}
			}
		}
		if c != nil {
			m.owner[p.SessionID] = c
			to = []*muxClient{c}
		}
		if info := targetOf(p.TargetInfo); m.ua != nil && (info.Type == "page" || info.Type == "iframe") {
			// До того, как событие дойдёт до клиента: он пустит вкладку
			// (runIfWaitingForDebugger) уже с правильным UA.
			ua := m.ua
			m.mu.Unlock()
			m.notifyInternal("Emulation.setUserAgentOverride", ua, p.SessionID)
			m.mu.Lock()
		}
	case msg.Method == "Target.detachedFromTarget":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if c := m.owner[p.SessionID]; c != nil {
			to = []*muxClient{c}
		} else if c := m.owner[msg.SessionID]; c != nil {
			to = []*muxClient{c}
		}
		delete(m.owner, p.SessionID)
	case msg.SessionID != "":
		if c := m.owner[msg.SessionID]; c != nil {
			to = []*muxClient{c}
		}
	case strings.HasPrefix(msg.Method, "Target.target"):
		m.track(msg.Method, msg.Params)
		for c := range m.discover {
			to = append(to, c)
		}
	default:
		for c := range m.clients {
			to = append(to, c)
		}
	}
	m.mu.Unlock()
	for _, c := range to {
		c.send(b)
	}
}

// track — список целей по событиям обнаружения (под m.mu).
func (m *Mux) track(method string, params json.RawMessage) {
	var p struct {
		TargetInfo json.RawMessage `json:"targetInfo"`
		TargetID   string          `json:"targetId"`
	}
	_ = json.Unmarshal(params, &p)
	switch method {
	case "Target.targetCreated", "Target.targetInfoChanged":
		if info := targetOf(p.TargetInfo); info.ID != "" {
			m.targets[info.ID] = p.TargetInfo
		}
	case "Target.targetDestroyed":
		delete(m.targets, p.TargetID)
	}
}

type targetInfo struct {
	ID   string `json:"targetId"`
	Type string `json:"type"`
}

func targetOf(raw json.RawMessage) targetInfo {
	var t targetInfo
	_ = json.Unmarshal(raw, &t)
	return t
}

// reply — ответ Chrome: клиенту с его id или внутреннему запросу.
func (m *Mux) reply(msg muxRaw, b []byte) {
	m.mu.Lock()
	p, ok := m.pending[*msg.ID]
	delete(m.pending, *msg.ID)
	if ok && p.method == "Target.attachToTarget" && p.c != nil {
		w := m.attaching[p.target]
		for i, c := range w {
			if c == p.c {
				w = append(w[:i], w[i+1:]...)
				break
			}
		}
		if len(w) == 0 {
			delete(m.attaching, p.target)
		} else {
			m.attaching[p.target] = w
		}
	}
	var sid struct {
		SessionID string `json:"sessionId"`
	}
	var ua map[string]any
	if ok && p.c != nil && (p.method == "Target.attachToTarget" || p.method == "Target.attachToBrowserTarget") && json.Unmarshal(msg.Result, &sid) == nil && sid.SessionID != "" {
		m.owner[sid.SessionID] = p.c
		if t := targetOf(m.targets[p.target]); m.ua != nil && (t.Type == "page" || t.Type == "iframe") {
			ua = m.ua
		}
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if ua != nil {
		// До ответа клиенту: его следующие команды (переход) — уже с маской.
		m.notifyInternal("Emulation.setUserAgentOverride", ua, sid.SessionID)
	}
	if p.reply != nil {
		var r cdpMsg
		_ = json.Unmarshal(b, &r)
		p.reply <- r
		return
	}
	if p.c == nil {
		return
	}
	p.c.send(withID(b, p.id))
}

// withID — сообщение с id клиента вместо id Mux.
func withID(b []byte, id json.RawMessage) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	m["id"] = id
	out, _ := json.Marshal(m)
	return out
}

// serveHTTP — /t/<token>/json/version, /t/<token>/devtools/browser (websocket),
// /t/<token>/probe (страница для снятия метаданных UA).
func (m *Mux) serveHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/t/")
	tok, path, _ := strings.Cut(rest, "/")
	if !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(m.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	switch {
	case path == "json/version" || path == "json/version/":
		m.mu.Lock()
		v := m.version
		ua := m.ua
		m.mu.Unlock()
		agent, _ := v["userAgent"].(string)
		if ua != nil {
			agent, _ = ua["userAgent"].(string)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"Browser": v["product"], "Protocol-Version": v["protocolVersion"], "User-Agent": agent,
			"V8-Version": v["jsVersion"], "webSocketDebuggerUrl": m.wsURL()})
	case strings.HasPrefix(path, "devtools/browser"):
		// Origin не проверяем: доступ даёт токен в адресе; расширение
		// Stagehand подключается со своим chrome-extension://<id>.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		conn.SetReadLimit(64 << 20)
		m.serveClient(conn)
	case path == "probe":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>gate4ai</title>"))
	default:
		http.NotFound(w, r)
	}
}

// serveClient — одно подключение клиента до его закрытия.
func (m *Mux) serveClient(conn *websocket.Conn) {
	c := &muxClient{m: m, out: make(chan []byte, MuxClientQueue), conn: conn, gone: make(chan struct{})}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, "chrome closed")
		return
	}
	m.clients[c] = true
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.writeLoop(ctx)
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			break
		}
		m.fromClient(c, b)
	}
	m.drop(c)
	c.close()
}

func (c *muxClient) send(b []byte) {
	select {
	case <-c.gone:
	case c.out <- b:
	default:
		// Клиент не читает: отключить, а не держать остальных.
		fmt.Fprintln(os.Stderr, "browser: a CDP client does not read its messages; disconnecting it")
		c.close()
	}
}

func (c *muxClient) writeLoop(ctx context.Context) {
	for {
		select {
		case <-c.gone:
			return
		case b := <-c.out:
			if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *muxClient) close() {
	c.once.Do(func() {
		close(c.gone)
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	})
}

// fromClient — запрос клиента в Chrome под id Mux.
func (m *Mux) fromClient(c *muxClient, b []byte) {
	var in muxIn
	if json.Unmarshal(b, &in) != nil || in.Method == "" || len(in.ID) == 0 {
		return
	}
	if in.SessionID == "" {
		switch in.Method {
		case "Target.setDiscoverTargets":
			m.setDiscover(c, in)
			return
		case "Target.setAutoAttach":
			if m.setAutoAttach(c, in) {
				return
			}
		}
	}
	p := muxPending{c: c, id: in.ID, method: in.Method}
	if in.Method == "Target.attachToTarget" {
		var t struct {
			TargetID string `json:"targetId"`
		}
		_ = json.Unmarshal(in.Params, &t)
		p.target = t.TargetID
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.next++
	id := m.next
	m.pending[id] = p
	if p.target != "" {
		m.attaching[p.target] = append(m.attaching[p.target], c)
	}
	m.mu.Unlock()
	out := map[string]any{"id": id, "method": in.Method}
	if len(in.Params) > 0 {
		out["params"] = in.Params
	}
	if in.SessionID != "" {
		out["sessionId"] = in.SessionID
	}
	nb, _ := json.Marshal(out)
	if err := m.write(nb); err != nil {
		c.close()
	}
}

// local — ответ клиенту без Chrome.
func (c *muxClient) local(id json.RawMessage, result any) {
	b, _ := json.Marshal(map[string]any{"id": id, "result": result})
	c.send(b)
}

// setDiscover — обнаружение целей у Mux включено всегда: клиент только
// подписывается и получает уже известные цели, как от Chrome.
func (m *Mux) setDiscover(c *muxClient, in muxIn) {
	var p struct {
		Discover bool `json:"discover"`
	}
	_ = json.Unmarshal(in.Params, &p)
	m.mu.Lock()
	var known []json.RawMessage
	if p.Discover {
		m.discover[c] = true
		for _, t := range m.targets {
			known = append(known, t)
		}
	} else {
		delete(m.discover, c)
	}
	m.mu.Unlock()
	c.local(in.ID, map[string]any{})
	for _, t := range known {
		b, _ := json.Marshal(map[string]any{"method": "Target.targetCreated", "params": map[string]json.RawMessage{"targetInfo": t}})
		c.send(b)
	}
}

// setAutoAttach браузера: первый включивший передаётся в Chrome, у
// следующих — тот же режим; выключает последний. true — ответ уже дан.
func (m *Mux) setAutoAttach(c *muxClient, in muxIn) bool {
	var p struct {
		AutoAttach bool `json:"autoAttach"`
	}
	_ = json.Unmarshal(in.Params, &p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.AutoAttach {
		first := len(m.autoAttach) == 0
		m.autoAttach[c] = in.Params
		if !first {
			c.local(in.ID, map[string]any{})
			return true
		}
		return false
	}
	delete(m.autoAttach, c)
	if len(m.autoAttach) > 0 {
		c.local(in.ID, map[string]any{})
		return true
	}
	return false
}

// drop — клиент отключился: его сессии отцепляются (вкладки, ждущие его
// runIfWaitingForDebugger, идут дальше), подписки снимаются.
func (m *Mux) drop(c *muxClient) {
	m.mu.Lock()
	delete(m.clients, c)
	delete(m.discover, c)
	var sessions []string
	for s, o := range m.owner {
		if o == c {
			sessions = append(sessions, s)
			delete(m.owner, s)
		}
	}
	_, had := m.autoAttach[c]
	delete(m.autoAttach, c)
	off := had && len(m.autoAttach) == 0 && !m.closed
	for t, w := range m.attaching {
		var keep []*muxClient
		for _, x := range w {
			if x != c {
				keep = append(keep, x)
			}
		}
		if len(keep) == 0 {
			delete(m.attaching, t)
		} else {
			m.attaching[t] = keep
		}
	}
	for id, p := range m.pending {
		if p.c == c {
			p.c = nil // ответ придёт — никому
			m.pending[id] = p
		}
	}
	m.mu.Unlock()
	if off {
		m.notifyInternal("Target.setAutoAttach", map[string]any{"autoAttach": false, "waitForDebuggerOnStart": false}, "")
	}
	for _, s := range sessions {
		m.notifyInternal("Target.detachFromTarget", map[string]any{"sessionId": s}, "")
	}
}
