// SPDX-License-Identifier: AGPL-3.0-only

package state

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

// Спаривание агента: агент сам создаёт ключ и присылает его хэш; daemon
// придумывает короткий код, агент показывает его человеку в чате, владелец
// сверяет код и подтверждает подключение (Web UI, Telegram). После этого хэш
// ключа — обычный токен агента; ключ может быть привязан к каталогу, из
// которого запущен агент.

const pairSchema = `
CREATE TABLE IF NOT EXISTS pairings(
  id TEXT PRIMARY KEY, agent TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '', key_hash TEXT NOT NULL,
  code TEXT NOT NULL, dir TEXT NOT NULL DEFAULT '', caller TEXT NOT NULL DEFAULT '', via TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL, expires INTEGER NOT NULL, status TEXT NOT NULL,
  decided_at INTEGER NOT NULL DEFAULT 0, channel TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS pairings_status ON pairings(status);
CREATE TABLE IF NOT EXISTS token_dirs(hash TEXT PRIMARY KEY, dir TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS approval_callers(id TEXT PRIMARY KEY, caller TEXT NOT NULL);`

// Pairing — запрос агента на подключение.
type Pairing struct {
	ID      string          `json:"id"`
	Agent   string          `json:"agent"`          // имя агента в конфигурации
	Kind    string          `json:"kind,omitempty"` // тип агента (claude-code, codex…)
	KeyHash string          `json:"-"`              // sha256 ключа (HashToken)
	Code    string          `json:"code"`           // код для сверки: агент показывает его в чате
	Dir     string          `json:"dir,omitempty"`  // ключ работает только для агента из этого каталога
	Caller  json.RawMessage `json:"caller,omitempty"`
	Via     string          `json:"via"` // agent-socket | https
	Created time.Time       `json:"created"`
	Expires time.Time       `json:"expires"`
	Status  string          `json:"status"` // pending | approved | rejected | timeout
	Decided time.Time       `json:"decided_at,omitempty"`
	Channel string          `json:"channel,omitempty"` // webui | telegram
}

// Пределы запросов на подключение: запрос можно сделать без токена, поэтому
// их число и срок ограничены.
const (
	PairingTTL     = 15 * time.Minute
	MaxPendingPair = 10 // на канал (сокет агентов, публичный адрес)
)

// ErrTooManyPairings — слишком много ждущих запросов на подключение.
var ErrTooManyPairings = errors.New("too many pending connection requests: the owner should approve or reject them in the Web UI first")

// ErrNoPairing — запроса нет.
var ErrNoPairing = errors.New("connection request not found")

// codeAlphabet — без похожих символов (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewPairingCode — код для сверки вида K7Q-4FM (≈30 бит; его придумывает
// daemon, а не агент: чужой процесс не подберёт ключ под тот же код).
func NewPairingCode() string {
	var b strings.Builder
	for i := 0; i < 6; i++ {
		if i == 3 {
			b.WriteByte('-')
		}
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	return b.String()
}

// PutPairing ставит запрос. Тот же ключ, ещё ждущий решения, — тот же запрос
// (повтор команды агентом не плодит запросы).
func (s *Store) PutPairing(p *Pairing) (*Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if old, err := s.scanPairing(s.db.QueryRow(`SELECT `+pairCols+` FROM pairings WHERE key_hash=? AND status='pending' AND expires>?`, p.KeyHash, now.UnixNano())); err == nil {
		return old, nil
	}
	var n int
	// Предел — свой у каждого канала: запросы с публичного адреса не мешают
	// подключить агента на этом компьютере.
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pairings WHERE status='pending' AND expires>? AND via=?`, now.UnixNano(), p.Via).Scan(&n); err != nil {
		return nil, err
	}
	if n >= MaxPendingPair {
		return nil, ErrTooManyPairings
	}
	var used int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE hash=?`, p.KeyHash).Scan(&used); err != nil {
		return nil, err
	}
	if used > 0 {
		return nil, errors.New("this key is already in use: generate a new one")
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	p.ID, p.Code, p.Created, p.Expires, p.Status = "p_"+hex.EncodeToString(b), NewPairingCode(), now, now.Add(PairingTTL), Pending
	_, err := s.db.Exec(`INSERT INTO pairings(id,agent,kind,key_hash,code,dir,caller,via,created,expires,status) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Agent, p.Kind, p.KeyHash, p.Code, p.Dir, string(p.Caller), p.Via, p.Created.UnixNano(), p.Expires.UnixNano(), p.Status)
	if err != nil {
		return nil, err
	}
	return p, nil
}

const pairCols = `id,agent,kind,key_hash,code,dir,caller,via,created,expires,status,decided_at,channel`

func (s *Store) scanPairing(row interface{ Scan(...any) error }) (*Pairing, error) {
	var p Pairing
	var caller string
	var created, expires, decided int64
	if err := row.Scan(&p.ID, &p.Agent, &p.Kind, &p.KeyHash, &p.Code, &p.Dir, &caller, &p.Via, &created, &expires, &p.Status, &decided, &p.Channel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoPairing
		}
		return nil, err
	}
	if caller != "" {
		p.Caller = json.RawMessage(caller)
	}
	p.Created, p.Expires = time.Unix(0, created), time.Unix(0, expires)
	if decided != 0 {
		p.Decided = time.Unix(0, decided)
	}
	if p.Status == Pending && !s.Now().Before(p.Expires) {
		p.Status = Timeout
	}
	return &p, nil
}

// GetPairing — запрос по id.
func (s *Store) GetPairing(id string) (*Pairing, error) {
	return s.scanPairing(s.db.QueryRow(`SELECT `+pairCols+` FROM pairings WHERE id=?`, id))
}

// PendingPairings — ждущие решения запросы, новые первыми.
func (s *Store) PendingPairings() ([]*Pairing, error) {
	rows, err := s.db.Query(`SELECT `+pairCols+` FROM pairings WHERE status='pending' AND expires>? ORDER BY created DESC`, s.Now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pairing
	for rows.Next() {
		p, err := s.scanPairing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DecidePairing — решение владельца, атомарно и один раз. Одобрение
// превращает хэш ключа в токен агента (с привязкой к каталогу, если она
// запрошена). Просроченный запрос не одобряется (ErrExpired).
func (s *Store) DecidePairing(id string, approve bool, channel string) (*Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := s.scanPairing(tx.QueryRow(`SELECT `+pairCols+` FROM pairings WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	switch p.Status {
	case Pending:
	case Timeout:
		return nil, ErrExpired
	default:
		return nil, ErrAlreadyDecided
	}
	status := Rejected
	if approve {
		status = Approved
		label := "paired"
		if p.Dir != "" {
			label = "paired:" + p.Dir
		}
		if _, err := tx.Exec(`INSERT INTO tokens(hash,agent,label,created,kind,expires) VALUES(?,?,?,?,?,0)`, p.KeyHash, p.Agent, label, now.UnixNano(), "agent"); err != nil {
			return nil, err
		}
		if p.Dir != "" {
			if _, err := tx.Exec(`INSERT INTO token_dirs(hash,dir) VALUES(?,?)`, p.KeyHash, p.Dir); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE pairings SET status=?, decided_at=?, channel=? WHERE id=?`, status, now.UnixNano(), channel, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	p.Status, p.Decided, p.Channel = status, now, channel
	return p, nil
}

// TokenDir — каталог, к которому привязан токен ("" — не привязан).
func (s *Store) TokenDir(tok string) string {
	var d string
	_ = s.db.QueryRow(`SELECT dir FROM token_dirs WHERE hash=?`, HashToken(tok)).Scan(&d)
	return d
}

// SetApprovalCaller — кто запросил действие (каталог и процессы агента): для
// квитанции, когда действие исполняется после подтверждения.
func (s *Store) SetApprovalCaller(id string, caller json.RawMessage) error {
	_, err := s.db.Exec(`INSERT INTO approval_callers(id,caller) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET caller=excluded.caller`, id, string(caller))
	return err
}

// ApprovalCaller — вызывающий запроса подтверждения (nil — неизвестен).
func (s *Store) ApprovalCaller(id string) json.RawMessage {
	var c string
	if s.db.QueryRow(`SELECT caller FROM approval_callers WHERE id=?`, id).Scan(&c) != nil || c == "" {
		return nil
	}
	return json.RawMessage(c)
}
