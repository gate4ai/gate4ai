// SPDX-License-Identifier: Apache-2.0

// Package netauth — соединения, которые хост открывает для коннектора: TCP,
// TLS (сразу или STARTTLS протокола) и аутентификация в протоколе хостом.
// Коннектор получает уже аутентифицированное соединение и никогда не видит
// пароль. Поддерживаются IMAP LOGIN и SMTP AUTH PLAIN.
package netauth

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// Config — параметры соединения.
type Config struct {
	abi.DialRequest
	Password string // значение секрета (только на стороне хоста)
	// Dial — свой способ соединения (тесты); nil — обычный.
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	RootCAs *x509.CertPool
	Timeout time.Duration
}

// Conn — соединение для модуля: Greeting отдаётся модулю перед данными сервера.
type Conn struct {
	net.Conn
	Greeting []byte
	r        *bufio.Reader
}

// Read — сначала уже буферизованные данные сервера.
func (c *Conn) Read(p []byte) (int, error) { return c.r.Read(p) }

// DefaultPort — порт по умолчанию для протокола и режима TLS.
func DefaultPort(proto, mode string) int {
	switch proto {
	case abi.ProtoIMAP:
		if mode == abi.TLSDirect {
			return 993
		}
		return 143
	case abi.ProtoSMTP:
		switch mode {
		case abi.TLSDirect:
			return 465
		case abi.TLSStartTLS:
			return 587
		}
		return 25
	}
	return 0
}

// Dial открывает соединение по Config.
func Dial(ctx context.Context, c Config) (*Conn, error) {
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.Port == 0 {
		c.Port = DefaultPort(c.Protocol, c.TLS)
	}
	if c.Port == 0 {
		return nil, abi.Errorf(abi.CodeBadRequest, "port is required")
	}
	switch c.TLS {
	case abi.TLSDirect, abi.TLSNone:
	case abi.TLSStartTLS:
		if c.Protocol != abi.ProtoIMAP && c.Protocol != abi.ProtoSMTP {
			return nil, abi.Errorf(abi.CodeBadRequest, "starttls needs protocol imap or smtp")
		}
	default:
		return nil, abi.Errorf(abi.CodeBadRequest, "unknown tls mode %q", c.TLS)
	}
	if c.Auth != nil {
		want := map[string]string{abi.AuthIMAPLogin: abi.ProtoIMAP, abi.AuthSMTPAuthPlain: abi.ProtoSMTP}[c.Auth.Scheme]
		if want == "" || want != c.Protocol {
			return nil, abi.Errorf(abi.CodeBadRequest, "auth %q does not fit protocol %q", c.Auth.Scheme, c.Protocol)
		}
	}
	dial := c.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: c.Timeout}
		dial = d.DialContext
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	raw, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, abi.Errorf(abi.CodeIO, "connect %s: %v", addr, err)
	}
	_ = raw.SetDeadline(time.Now().Add(c.Timeout))
	var conn net.Conn = raw
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, RootCAs: c.RootCAs}
	if c.TLS == abi.TLSDirect {
		tc := tls.Client(raw, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, abi.Errorf(abi.CodeIO, "tls %s: %v", addr, err)
		}
		conn = tc
	}
	out := &Conn{Conn: conn, r: bufio.NewReader(conn)}
	switch c.Protocol {
	case abi.ProtoIMAP:
		err = out.imap(c, tlsCfg)
	case abi.ProtoSMTP:
		err = out.smtp(c, tlsCfg)
	case "":
	default:
		err = abi.Errorf(abi.CodeBadRequest, "unknown protocol %q", c.Protocol)
	}
	if err != nil {
		out.Close()
		return nil, err
	}
	_ = out.Conn.SetDeadline(time.Time{})
	return out, nil
}

func (c *Conn) line() (string, error) {
	s, err := c.r.ReadString('\n')
	if err != nil {
		return "", abi.Errorf(abi.CodeIO, "read: %v", err)
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func (c *Conn) send(s string) error {
	if _, err := io.WriteString(c.Conn, s+"\r\n"); err != nil {
		return abi.Errorf(abi.CodeIO, "write: %v", err)
	}
	return nil
}

func (c *Conn) upgrade(tlsCfg *tls.Config) error {
	if c.r.Buffered() > 0 {
		return abi.Errorf(abi.CodeIO, "unexpected data before TLS handshake")
	}
	tc := tls.Client(c.Conn, tlsCfg)
	if err := tc.Handshake(); err != nil {
		return abi.Errorf(abi.CodeIO, "starttls: %v", err)
	}
	c.Conn, c.r = tc, bufio.NewReader(tc)
	return nil
}

// imapTagged читает ответы до строки с тегом; возвращает её статус (OK/NO/BAD).
func (c *Conn) imapTagged(tag string) (string, error) {
	for {
		l, err := c.line()
		if err != nil {
			return "", err
		}
		if rest, ok := strings.CutPrefix(l, tag+" "); ok {
			return rest, nil
		}
		// Литерал в ответе: "{n}" в конце строки — пропустить n байт.
		if i := strings.LastIndexByte(l, '{'); i >= 0 && strings.HasSuffix(l, "}") {
			if n, err := strconv.Atoi(l[i+1 : len(l)-1]); err == nil && n >= 0 {
				if _, err := io.CopyN(io.Discard, c.r, int64(n)); err != nil {
					return "", abi.Errorf(abi.CodeIO, "read: %v", err)
				}
			}
		}
	}
}

// imapString — строка IMAP: quoted для ASCII, литерал для остального.
func (c *Conn) imapString(s string) (quoted string, literal bool) {
	for _, r := range s {
		if r > 0x7e || r < 0x20 {
			return fmt.Sprintf("{%d}", len(s)), true
		}
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, false
}

func (c *Conn) imap(cfg Config, tlsCfg *tls.Config) error {
	greet, err := c.line()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(greet, "* OK") && !strings.HasPrefix(greet, "* PREAUTH") {
		return abi.Errorf(abi.CodeIO, "imap: unexpected greeting %q", greet)
	}
	preauth := strings.HasPrefix(greet, "* PREAUTH")
	if cfg.TLS == abi.TLSStartTLS {
		if err := c.send("g0 STARTTLS"); err != nil {
			return err
		}
		if st, err := c.imapTagged("g0"); err != nil {
			return err
		} else if !strings.HasPrefix(st, "OK") {
			return abi.Errorf(abi.CodeIO, "imap STARTTLS: %s", st)
		}
		if err := c.upgrade(tlsCfg); err != nil {
			return err
		}
	}
	if cfg.Auth != nil && !preauth {
		user, ul := c.imapString(cfg.Auth.Username)
		pass, pl := c.imapString(cfg.Password)
		// LOGIN с литералами: после "{n}" сервер отвечает "+", затем данные.
		parts := []struct {
			s   string
			lit bool
			raw string
		}{{user, ul, cfg.Auth.Username}, {pass, pl, cfg.Password}}
		cmd := "g1 LOGIN"
		for _, p := range parts {
			cmd += " " + p.s
			if p.lit {
				if err := c.send(cmd); err != nil {
					return err
				}
				l, err := c.line()
				if err != nil {
					return err
				}
				if !strings.HasPrefix(l, "+") {
					return abi.Errorf(abi.CodeAuth, "imap login rejected")
				}
				cmd = p.raw
			}
		}
		if err := c.send(cmd); err != nil {
			return err
		}
		st, err := c.imapTagged("g1")
		if err != nil {
			return err
		}
		if !strings.HasPrefix(st, "OK") {
			return abi.Errorf(abi.CodeAuth, "imap login failed")
		}
		preauth = true
	}
	if preauth {
		c.Greeting = []byte("* PREAUTH [CAPABILITY IMAP4rev1] gate4ai: authenticated by host\r\n")
	} else {
		c.Greeting = []byte("* OK [CAPABILITY IMAP4rev1] gate4ai: connected by host\r\n")
	}
	return nil
}

// smtpReply читает (многострочный) ответ SMTP: код и строки.
func (c *Conn) smtpReply() (int, []string, error) {
	var lines []string
	for {
		l, err := c.line()
		if err != nil {
			return 0, nil, err
		}
		if len(l) < 3 {
			return 0, nil, abi.Errorf(abi.CodeIO, "smtp: bad reply %q", l)
		}
		code, err := strconv.Atoi(l[:3])
		if err != nil {
			return 0, nil, abi.Errorf(abi.CodeIO, "smtp: bad reply %q", l)
		}
		lines = append(lines, strings.TrimSpace(l[3:]))
		if len(l) == 3 || l[3] != '-' {
			return code, lines, nil
		}
	}
}

func (c *Conn) smtpCmd(cmd string, want int) ([]string, error) {
	if err := c.send(cmd); err != nil {
		return nil, err
	}
	code, lines, err := c.smtpReply()
	if err != nil {
		return nil, err
	}
	if code != want {
		verb, _, _ := strings.Cut(cmd, " ")
		return nil, abi.Errorf(abi.CodeIO, "smtp %s: %d %s", verb, code, strings.Join(lines, " "))
	}
	return lines, nil
}

func (c *Conn) smtp(cfg Config, tlsCfg *tls.Config) error {
	code, lines, err := c.smtpReply()
	if err != nil {
		return err
	}
	if code != 220 {
		return abi.Errorf(abi.CodeIO, "smtp greeting: %d %s", code, strings.Join(lines, " "))
	}
	if _, err := c.smtpCmd("EHLO gate4ai.local", 250); err != nil {
		return err
	}
	if cfg.TLS == abi.TLSStartTLS {
		if _, err := c.smtpCmd("STARTTLS", 220); err != nil {
			return err
		}
		if err := c.upgrade(tlsCfg); err != nil {
			return err
		}
		if _, err := c.smtpCmd("EHLO gate4ai.local", 250); err != nil {
			return err
		}
	}
	if cfg.Auth != nil {
		tok := base64.StdEncoding.EncodeToString([]byte("\x00" + cfg.Auth.Username + "\x00" + cfg.Password))
		if _, err := c.smtpCmd("AUTH PLAIN "+tok, 235); err != nil {
			var e *abi.Error
			if errors.As(err, &e) {
				e.Code, e.Message = abi.CodeAuth, "smtp auth failed"
			}
			return err
		}
	}
	return nil
}
