// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	gomail "github.com/emersion/go-message/mail"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// Server — параметры IMAP или SMTP сервера.
type Server struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"` // tls (по умолчанию) | starttls | none (только для localhost/тестов)
	Username string `json:"username"`
	Password string `json:"password"` // алиас Vault (@name): значение подставляет хост при входе
}

// Settings — настройки коннектора (connectors.email в конфигурации).
type Settings struct {
	Address        string `json:"address"`
	Name           string `json:"name"`
	IMAP           Server `json:"imap"`
	SMTP           Server `json:"smtp"`
	SentFolder     string `json:"sent_folder"`
	DraftsFolder   string `json:"drafts_folder"`
	SentCopy       string `json:"sent_copy"` // server (провайдер сохраняет сам, по умолчанию) | client (сохраняет коннектор)
	VerifyAttempts int    `json:"verify_attempts"`
	VerifyDelayMS  int    `json:"verify_delay_ms"`
	MaxBodyChars   int    `json:"max_body_chars"`
}

func parseSettings(m map[string]any) (Settings, error) {
	var s Settings
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("connectors.email: %w", err)
	}
	if s.Address == "" || s.IMAP.Host == "" {
		return s, errors.New("connectors.email: address and imap.host are required")
	}
	if s.IMAP.Username == "" {
		s.IMAP.Username = s.Address
	}
	if s.SMTP.Username == "" {
		s.SMTP.Username = s.IMAP.Username
	}
	if s.SMTP.Password == "" {
		s.SMTP.Password = s.IMAP.Password
	}
	if s.VerifyAttempts <= 0 {
		s.VerifyAttempts = 6
	}
	if s.VerifyDelayMS <= 0 {
		s.VerifyDelayMS = 2000
	}
	if s.MaxBodyChars <= 0 {
		s.MaxBodyChars = 20000
	}
	if s.SentCopy == "" {
		s.SentCopy = "server"
	}
	return s, nil
}

// Mail — коннектор почты.
type Mail struct {
	// Now — часы (для тестов).
	Now func() time.Time
}

func (c *Mail) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.now()
}

// Execute исполняет действие.
func (c *Mail) Execute(ctx context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	s, err := parseSettings(r.Settings)
	if err != nil {
		return abi.ExecResult{}, err
	}
	switch r.Action {
	case "list":
		return c.list(h, s, r.Args)
	case "read":
		return c.read(h, s, r.Args)
	case "draft":
		return c.draft(h, s, r.Args)
	case "send":
		return c.send(h, s, r.Args)
	}
	return abi.ExecResult{}, fmt.Errorf("unknown action %s", r.Action)
}

func tlsMode(s Server) string {
	if s.TLS == "" {
		return abi.TLSDirect
	}
	return s.TLS
}

// dialIMAP — соединение открывает хост и сам выполняет вход по алиасу пароля;
// коннектор получает уже аутентифицированную сессию (PREAUTH).
func dialIMAP(h *abi.Host, s Server) (*client.Client, error) {
	req := abi.DialRequest{Host: s.Host, Port: s.Port, TLS: tlsMode(s), Protocol: abi.ProtoIMAP}
	if s.Password != "" {
		req.Auth = &abi.DialAuth{Scheme: abi.AuthIMAPLogin, Username: s.Username, Secret: s.Password}
	}
	conn, err := h.Dial(req)
	if err != nil {
		return nil, fmt.Errorf("imap connect %s: %w", s.Host, err)
	}
	c, err := client.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("imap: %w", err)
	}
	c.Timeout = 30 * time.Second
	return c, nil
}

func intArg(a map[string]any, k string, def int) int {
	if f, ok := a[k].(float64); ok && f != 0 {
		return int(f)
	}
	return def
}

func strArg(a map[string]any, k, def string) string {
	if s, ok := a[k].(string); ok && s != "" {
		return s
	}
	return def
}

func listArg(a map[string]any, k string) []string {
	var out []string
	if l, ok := a[k].([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// Summary — краткое описание письма.
type Summary struct {
	UID       uint32   `json:"uid"`
	MessageID string   `json:"message_id"`
	From      []string `json:"from"`
	To        []string `json:"to"`
	Subject   string   `json:"subject"`
	Date      string   `json:"date"`
	Seen      bool     `json:"seen"`
}

func addrs(as []*imap.Address) []string {
	out := []string{}
	for _, a := range as {
		s := a.Address()
		if a.PersonalName != "" {
			s = a.PersonalName + " <" + s + ">"
		}
		out = append(out, s)
	}
	return out
}

func (c *Mail) list(h *abi.Host, s Settings, a map[string]any) (abi.ExecResult, error) {
	cl, err := dialIMAP(h, s.IMAP)
	if err != nil {
		return abi.ExecResult{}, err
	}
	defer cl.Logout()
	folder := strArg(a, "folder", "INBOX")
	if _, err := cl.Select(folder, true); err != nil {
		return abi.ExecResult{}, fmt.Errorf("folder %q: %w", folder, err)
	}
	crit := imap.NewSearchCriteria()
	if b, _ := a["unseen_only"].(bool); b {
		crit.WithoutFlags = []string{imap.SeenFlag}
	}
	if q := strArg(a, "query", ""); q != "" {
		crit.Text = []string{q}
	}
	uids, err := cl.UidSearch(crit)
	if err != nil {
		return abi.ExecResult{}, err
	}
	limit := intArg(a, "limit", 20)
	if limit > 100 {
		limit = 100
	}
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}
	out := []any{}
	if len(uids) > 0 {
		set := new(imap.SeqSet)
		set.AddNum(uids...)
		ch := make(chan *imap.Message, len(uids))
		if err := cl.UidFetch(set, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags, imap.FetchUid}, ch); err != nil {
			return abi.ExecResult{}, err
		}
		var msgs []Summary
		for m := range ch {
			sm := Summary{UID: m.Uid}
			for _, f := range m.Flags {
				sm.Seen = sm.Seen || f == imap.SeenFlag
			}
			if e := m.Envelope; e != nil {
				sm.MessageID, sm.Subject, sm.From, sm.To = e.MessageId, e.Subject, addrs(e.From), addrs(e.To)
				if !e.Date.IsZero() {
					sm.Date = e.Date.UTC().Format(time.RFC3339)
				}
			}
			msgs = append(msgs, sm)
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			out = append(out, msgs[i])
		}
	}
	return abi.ExecResult{Structured: map[string]any{"folder": folder, "messages": out}, Source: "email:" + folder}, nil
}

func (c *Mail) read(h *abi.Host, s Settings, a map[string]any) (abi.ExecResult, error) {
	cl, err := dialIMAP(h, s.IMAP)
	if err != nil {
		return abi.ExecResult{}, err
	}
	defer cl.Logout()
	folder := strArg(a, "folder", "INBOX")
	if _, err := cl.Select(folder, true); err != nil {
		return abi.ExecResult{}, fmt.Errorf("folder %q: %w", folder, err)
	}
	uid := uint32(intArg(a, "uid", 0))
	set := new(imap.SeqSet)
	set.AddNum(uid)
	section := &imap.BodySectionName{Peek: true}
	ch := make(chan *imap.Message, 1)
	if err := cl.UidFetch(set, []imap.FetchItem{section.FetchItem(), imap.FetchUid}, ch); err != nil {
		return abi.ExecResult{}, err
	}
	m := <-ch
	if m == nil {
		return abi.ExecResult{}, fmt.Errorf("message uid %d not found in %s", uid, folder)
	}
	raw, _ := io.ReadAll(m.GetBody(section))
	msg, err := parseMessage(raw, s.MaxBodyChars)
	if err != nil {
		return abi.ExecResult{}, err
	}
	msg["uid"] = uid
	msg["folder"] = folder
	src := "email:" + fmt.Sprint(msg["message_id"])
	return abi.ExecResult{Structured: msg, ResultRef: fmt.Sprint(msg["message_id"]), Source: src}, nil
}

// parseMessage извлекает заголовки и текст (text/plain, иначе text/html без тегов).
func parseMessage(raw []byte, max int) (map[string]any, error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && mr == nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	h := mr.Header
	out := map[string]any{}
	out["message_id"], _ = h.MessageID()
	if out["message_id"] != "" {
		out["message_id"] = "<" + out["message_id"].(string) + ">"
	}
	out["subject"], _ = h.Subject()
	for _, k := range []string{"From", "To", "Cc", "Reply-To"} {
		l, _ := h.AddressList(k)
		var ss []string
		for _, a := range l {
			ss = append(ss, a.String())
		}
		if len(ss) > 0 {
			out[strings.ToLower(strings.ReplaceAll(k, "-", "_"))] = ss
		}
	}
	if d, err := h.Date(); err == nil {
		out["date"] = d.UTC().Format(time.RFC3339)
	}
	var plain, html string
	var attachments []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch ph := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := ph.ContentType()
			b, _ := io.ReadAll(io.LimitReader(p.Body, 4<<20))
			if ct == "text/html" && html == "" {
				html = string(b)
			} else if (ct == "text/plain" || ct == "") && plain == "" {
				plain = string(b)
			}
		case *gomail.AttachmentHeader:
			fn, _ := ph.Filename()
			attachments = append(attachments, fn)
		}
	}
	body := plain
	if body == "" {
		body = stripTags(html)
	}
	if r := []rune(body); len(r) > max {
		body = string(r[:max]) + "\n[…truncated]"
	}
	out["body"] = body
	if len(attachments) > 0 {
		out["attachments"] = attachments
	}
	return out, nil
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func newMessageID(from string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	dom := "gate4ai.local"
	if i := strings.LastIndexByte(from, '@'); i >= 0 {
		dom = from[i+1:]
	}
	return "<" + hex.EncodeToString(b) + ".gate4ai@" + dom + ">"
}

// compose собирает RFC 5322 сообщение (text/plain UTF-8, quoted-printable).
func (c *Mail) compose(s Settings, a map[string]any) (raw []byte, msgID string) {
	msgID = newMessageID(s.Address)
	from := s.Address
	if s.Name != "" {
		from = mime.QEncoding.Encode("utf-8", s.Name) + " <" + s.Address + ">"
	}
	var b bytes.Buffer
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from)
	hdr("To", strings.Join(listArg(a, "to"), ", "))
	if cc := listArg(a, "cc"); len(cc) > 0 {
		hdr("Cc", strings.Join(cc, ", "))
	}
	hdr("Subject", mime.QEncoding.Encode("utf-8", strArg(a, "subject", "")))
	hdr("Date", c.now().Format(time.RFC1123Z))
	hdr("Message-ID", msgID)
	if irt := strArg(a, "in_reply_to", ""); irt != "" {
		hdr("In-Reply-To", irt)
		hdr("References", irt)
	}
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", "text/plain; charset=utf-8")
	hdr("Content-Transfer-Encoding", "quoted-printable")
	hdr("X-Mailer", "gate4.ai")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(strArg(a, "body", ""), "\r\n", "\n")
	_, _ = qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	_ = qp.Close()
	return b.Bytes(), msgID
}

func (c *Mail) draft(h *abi.Host, s Settings, a map[string]any) (abi.ExecResult, error) {
	raw, id := c.compose(s, a)
	cl, err := dialIMAP(h, s.IMAP)
	if err != nil {
		return abi.ExecResult{}, err
	}
	defer cl.Logout()
	folder, err := findFolder(cl, s.DraftsFolder, imap.DraftsAttr, []string{"Drafts", "[Gmail]/Drafts", "Черновики", "Draft"})
	if err != nil {
		return abi.ExecResult{}, err
	}
	if err := cl.Append(folder, []string{imap.DraftFlag, imap.SeenFlag}, c.now(), bytes.NewReader(raw)); err != nil {
		return abi.ExecResult{}, fmt.Errorf("save draft: %w", err)
	}
	return abi.ExecResult{Structured: map[string]any{"message_id": id, "folder": folder, "status": "draft saved, not sent"}, ResultRef: id,
		Evidence: []abi.EvidenceItem{{Name: "draft.eml", MIME: "message/rfc822", Data: raw}}}, nil
}

func (c *Mail) send(h *abi.Host, s Settings, a map[string]any) (abi.ExecResult, error) {
	raw, id := c.compose(s, a)
	rcpts := append(listArg(a, "to"), listArg(a, "cc")...)
	resp, err := smtpSend(h, s.SMTP, s.Address, rcpts, raw)
	if err != nil {
		return abi.ExecResult{}, err
	}
	res := map[string]any{"message_id": id, "recipients": rcpts, "smtp_response": resp}
	ev := []abi.EvidenceItem{{Name: "sent.eml", MIME: "message/rfc822", Data: raw},
		{Name: "smtp.txt", MIME: "text/plain", Data: []byte(resp)}}
	if s.SentCopy == "client" {
		cl, err := dialIMAP(h, s.IMAP)
		if err == nil {
			if folder, err := findFolder(cl, s.SentFolder, imap.SentAttr, sentNames); err == nil {
				_ = cl.Append(folder, []string{imap.SeenFlag}, c.now(), bytes.NewReader(raw))
			}
			cl.Logout()
		}
	}
	return abi.ExecResult{Structured: res, ResultRef: id, Evidence: ev}, nil
}

var sentNames = []string{"Sent", "[Gmail]/Sent Mail", "Sent Items", "Sent Messages", "Отправленные", "INBOX.Sent"}

// smtpSend — SMTP-сессию открывает хост: EHLO, STARTTLS и AUTH PLAIN по алиасу
// пароля он выполняет сам, коннектор начинает сразу с MAIL FROM.
func smtpSend(h *abi.Host, s Server, from string, to []string, raw []byte) (string, error) {
	req := abi.DialRequest{Host: s.Host, Port: s.Port, TLS: tlsMode(s), Protocol: abi.ProtoSMTP}
	if s.Password != "" {
		req.Auth = &abi.DialAuth{Scheme: abi.AuthSMTPAuthPlain, Username: s.Username, Secret: s.Password}
	}
	conn, err := h.Dial(req)
	if err != nil {
		return "", fmt.Errorf("smtp connect %s: %w", s.Host, err)
	}
	defer conn.Close()
	c := &smtpConn{c: conn, r: bufio.NewReader(conn)}
	if _, err := c.cmd("MAIL FROM:<"+from+">", 250); err != nil {
		return "", err
	}
	for _, r := range to {
		if _, err := c.cmd("RCPT TO:<"+r+">", 250, 251); err != nil {
			return "", err
		}
	}
	if _, err := c.cmd("DATA", 354); err != nil {
		return "", err
	}
	if _, err := c.c.Write(dotStuff(raw)); err != nil {
		return "", fmt.Errorf("smtp DATA: %w", err)
	}
	resp, err := c.cmd(".", 250)
	if err != nil {
		return "", err
	}
	_, _ = c.cmd("QUIT", 221)
	return "250 " + resp, nil
}

// smtpConn — минимальный клиент SMTP поверх соединения хоста.
type smtpConn struct {
	c net.Conn
	r *bufio.Reader
}

func (c *smtpConn) cmd(line string, want ...int) (string, error) {
	if _, err := io.WriteString(c.c, line+"\r\n"); err != nil {
		return "", err
	}
	verb, _, _ := strings.Cut(line, ":")
	var text []string
	for {
		l, err := c.r.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("smtp %s: %w", verb, err)
		}
		l = strings.TrimRight(l, "\r\n")
		if len(l) < 3 {
			return "", fmt.Errorf("smtp %s: bad reply %q", verb, l)
		}
		text = append(text, strings.TrimSpace(l[min(4, len(l)):]))
		if len(l) > 3 && l[3] == '-' {
			continue
		}
		code, _ := strconv.Atoi(l[:3])
		for _, w := range want {
			if code == w {
				return strings.Join(text, " "), nil
			}
		}
		return "", fmt.Errorf("smtp %s: %s", verb, l)
	}
}

// dotStuff — тело для DATA: точка в начале строки удваивается, в конце — CRLF.
func dotStuff(raw []byte) []byte {
	var b bytes.Buffer
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(".")) {
			b.WriteByte('.')
		}
		b.Write(line)
	}
	if !bytes.HasSuffix(b.Bytes(), []byte("\r\n")) {
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// findFolder — папка по настройке, по атрибуту SPECIAL-USE или по известным именам.
func findFolder(cl *client.Client, configured, attr string, names []string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	ch := make(chan *imap.MailboxInfo, 64)
	done := make(chan error, 1)
	go func() { done <- cl.List("", "*", ch) }()
	var all []string
	found := ""
	for m := range ch {
		all = append(all, m.Name)
		for _, a := range m.Attributes {
			if a == attr && found == "" {
				found = m.Name
			}
		}
	}
	if err := <-done; err != nil {
		return "", err
	}
	if found != "" {
		return found, nil
	}
	for _, n := range names {
		for _, m := range all {
			if strings.EqualFold(m, n) {
				return m, nil
			}
		}
	}
	return "", fmt.Errorf("folder with %s not found; set it in connectors.email", attr)
}

// Verify — проверка по независимому каналу: письмо с этим Message-ID есть
// в «Отправленных» (send) или «Черновиках» (draft). Ошибка — проверить не
// удалось (сеть, IMAP); not_confirmed — письма нет после всех попыток.
func (c *Mail) Verify(ctx context.Context, h *abi.Host, r abi.VerifyRequest) (abi.VerifyResult, error) {
	s, err := parseSettings(r.Settings)
	if err != nil {
		return abi.VerifyResult{}, err
	}
	method, attr, names, cfg := "sent_folder", imap.SentAttr, sentNames, s.SentFolder
	if r.Action == "draft" {
		method, attr, names, cfg = "drafts_folder", imap.DraftsAttr, []string{"Drafts", "[Gmail]/Drafts", "Черновики"}, s.DraftsFolder
	}
	if s.SentCopy == "client" && r.Action == "send" {
		method = "smtp_accepted+client_copy"
	}
	var lastErr error
	for i := 0; i < s.VerifyAttempts; i++ {
		if i > 0 {
			select {
			case <-time.After(time.Duration(s.VerifyDelayMS) * time.Millisecond):
			case <-ctx.Done():
				return abi.VerifyResult{}, ctx.Err()
			}
		}
		found, raw, err := c.findByID(h, s, cfg, attr, names, r.ResultRef)
		if err != nil {
			lastErr = err
			continue
		}
		if found {
			return abi.VerifyResult{Status: abi.Verified, Method: method, Detail: "found " + r.ResultRef,
				Evidence: []abi.EvidenceItem{{Name: "copy.eml", MIME: "message/rfc822", Data: raw}}}, nil
		}
	}
	if lastErr != nil {
		return abi.VerifyResult{}, lastErr
	}
	return abi.VerifyResult{Status: abi.NotConfirmed, Method: method, Detail: "message " + r.ResultRef + " not found"}, nil
}

func (c *Mail) findByID(h *abi.Host, s Settings, cfg, attr string, names []string, id string) (bool, []byte, error) {
	cl, err := dialIMAP(h, s.IMAP)
	if err != nil {
		return false, nil, err
	}
	defer cl.Logout()
	folder, err := findFolder(cl, cfg, attr, names)
	if err != nil {
		return false, nil, err
	}
	if _, err := cl.Select(folder, true); err != nil {
		return false, nil, err
	}
	crit := imap.NewSearchCriteria()
	crit.Header.Set("Message-Id", id)
	uids, err := cl.UidSearch(crit)
	if err != nil || len(uids) == 0 {
		return false, nil, err
	}
	set := new(imap.SeqSet)
	set.AddNum(uids[0])
	section := &imap.BodySectionName{Peek: true}
	ch := make(chan *imap.Message, 1)
	if err := cl.UidFetch(set, []imap.FetchItem{section.FetchItem()}, ch); err != nil {
		return true, nil, nil
	}
	var raw []byte
	if m := <-ch; m != nil {
		raw, _ = io.ReadAll(m.GetBody(section))
	}
	return true, raw, nil
}
