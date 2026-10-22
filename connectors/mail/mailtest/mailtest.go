// SPDX-License-Identifier: AGPL-3.0-only

// Package mailtest — фейковые IMAP и SMTP серверы для тестов коннектора почты и e2e:
// IMAP на imapmemserver из go-imap v2, SMTP на go-smtp; принятое SMTP письмо
// (по желанию) кладётся в «Отправленные», как делает Gmail.
package mailtest

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Учётные данные фейкового ящика; в настройках коннектора пароль — алиас Alias.
const (
	User     = "username"
	Password = "password"
	Alias    = "@mail.test"
)

// Server — пара IMAP + SMTP.
type Server struct {
	IMAPPort, SMTPPort int
	// AutoSaveSent — класть принятые письма в Sent (поведение Gmail).
	AutoSaveSent bool
	// RejectRcpt — адреса, которые SMTP отвергает.
	RejectRcpt map[string]bool

	mu       sync.Mutex
	user     *imapmemserver.User
	imap     *imapserver.Server
	smtp     *smtp.Server
	Received []Envelope
}

// Envelope — принятое SMTP письмо.
type Envelope struct {
	From string
	To   []string
	Data []byte
}

// Start запускает серверы на 127.0.0.1.
func Start() (*Server, error) {
	mem := imapmemserver.New()
	u := imapmemserver.NewUser(User, Password)
	for _, mb := range []string{"INBOX", "Sent", "Drafts"} {
		if err := u.Create(mb, nil); err != nil {
			return nil, err
		}
	}
	mem.AddUser(u)
	s := &Server{AutoSaveSent: true, user: u, RejectRcpt: map[string]bool{}}
	s.imap = imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
	})
	il, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.IMAPPort = il.Addr().(*net.TCPAddr).Port
	go s.imap.Serve(il)

	s.smtp = smtp.NewServer(&smtpBackend{s: s})
	s.smtp.AllowInsecureAuth = true
	s.smtp.Domain = "localhost"
	s.smtp.ReadTimeout = 10 * time.Second
	s.smtp.WriteTimeout = 10 * time.Second
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.SMTPPort = sl.Addr().(*net.TCPAddr).Port
	go s.smtp.Serve(sl)
	return s, nil
}

// Close останавливает серверы.
func (s *Server) Close() {
	s.imap.Close()
	s.smtp.Close()
}

// Settings — настройки коннектора для этого сервера (пароль — алиас Alias).
func (s *Server) Settings(address string) map[string]any {
	return map[string]any{
		"address":         address,
		"imap":            map[string]any{"host": "127.0.0.1", "port": float64(s.IMAPPort), "tls": "none", "username": User, "password": Alias},
		"smtp":            map[string]any{"host": "127.0.0.1", "port": float64(s.SMTPPort), "tls": "none", "username": User, "password": Alias},
		"verify_attempts": float64(3),
		"verify_delay_ms": float64(30),
	}
}

// Secrets — значения секретов для тестового хоста (алиас → пароль).
func (s *Server) Secrets() map[string]string { return map[string]string{Alias: Password} }

// Deliver кладёт письмо во входящие (как будто пришло извне).
func (s *Server) Deliver(raw string) error {
	_, err := s.user.Append("INBOX", bytes.NewReader([]byte(raw)), &imap.AppendOptions{Time: time.Now()})
	return err
}

// Rename переименовывает папку (сервер с другими именами папок).
func (s *Server) Rename(old, name string) error { return s.user.Rename(old, name, nil) }

// Count — число писем в папке.
func (s *Server) Count(folder string) int {
	st, err := s.user.Status(folder, &imap.StatusOptions{NumMessages: true})
	if err != nil || st.NumMessages == nil {
		return -1
	}
	return int(*st.NumMessages)
}

type smtpBackend struct{ s *Server }

func (b *smtpBackend) NewSession(*smtp.Conn) (smtp.Session, error) { return &session{s: b.s}, nil }

type session struct {
	s      *Server
	authed bool
	env    Envelope
}

func (ss *session) AuthMechanisms() []string { return []string{sasl.Plain} }

func (ss *session) Auth(mech string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if username != User || password != Password {
			return errors.New("invalid credentials")
		}
		ss.authed = true
		return nil
	}), nil
}

func (ss *session) Mail(from string, _ *smtp.MailOptions) error {
	if !ss.authed {
		return smtp.ErrAuthRequired
	}
	ss.env.From = from
	return nil
}

func (ss *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if ss.s.RejectRcpt[to] {
		return &smtp.SMTPError{Code: 550, Message: "mailbox unavailable: " + to}
	}
	ss.env.To = append(ss.env.To, to)
	return nil
}

func (ss *session) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	ss.env.Data = b
	ss.s.mu.Lock()
	defer ss.s.mu.Unlock()
	ss.s.Received = append(ss.s.Received, ss.env)
	if ss.s.AutoSaveSent {
		_, err := ss.s.user.Append("Sent", bytes.NewReader(b), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}, Time: time.Now()})
		return err
	}
	return nil
}

func (ss *session) Reset()        { ss.env = Envelope{} }
func (ss *session) Logout() error { return nil }

// Port — порт строкой (для логов).
func Port(p int) string { return strconv.Itoa(p) }
