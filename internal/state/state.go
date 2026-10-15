// SPDX-License-Identifier: FSL-1.1-ALv2

// Package state — State Store на SQLite: счётчики лимитов, очередь
// подтверждений, метки происхождения (taint), токены агентов.
package state

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	_ "modernc.org/sqlite"

	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Store — State Store.
type Store struct {
	db  *sql.DB
	key []byte
	Now func() time.Time
	mu  sync.Mutex

	seen map[string]time.Time // агент+канал → последняя запись agent_seen (Seen)
}

const schema = `
CREATE TABLE IF NOT EXISTS actions(
  id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, agent TEXT NOT NULL, action TEXT NOT NULL,
  class TEXT NOT NULL, amount REAL NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS actions_ts ON actions(ts);
CREATE TABLE IF NOT EXISTS approvals(
  id TEXT PRIMARY KEY, created INTEGER NOT NULL, expires INTEGER NOT NULL,
  agent TEXT NOT NULL, action TEXT NOT NULL, class TEXT NOT NULL,
  status TEXT NOT NULL, preview TEXT NOT NULL, preview_hash TEXT NOT NULL,
  decision TEXT NOT NULL, args BLOB NOT NULL, provenance TEXT NOT NULL DEFAULT '{}',
  channel TEXT NOT NULL DEFAULT '', who TEXT NOT NULL DEFAULT '', decided_at INTEGER NOT NULL DEFAULT 0,
  result TEXT NOT NULL DEFAULT '', nonce TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS approvals_status ON approvals(status);
CREATE TABLE IF NOT EXISTS taint(value TEXT NOT NULL, source TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS taint_value ON taint(value);
CREATE TABLE IF NOT EXISTS tokens(
  hash TEXT PRIMARY KEY, agent TEXT NOT NULL, label TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, kind TEXT NOT NULL DEFAULT 'agent',
  expires INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS egress_log(id INTEGER PRIMARY KEY, ts INTEGER NOT NULL, rec TEXT NOT NULL);`

// Open открывает State Store.
func Open(path string, ks keys.Store) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + uiSchema); err != nil {
		db.Close()
		return nil, err
	}
	key, err := keys.RandomBytes(ks, "state.aead", chacha20poly1305.KeySize)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, key: key, Now: time.Now}, nil
}

// Close закрывает базу.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) seal(ad string, plain []byte) []byte {
	a, _ := chacha20poly1305.NewX(s.key)
	nonce := make([]byte, a.NonceSize())
	_, _ = rand.Read(nonce)
	return a.Seal(nonce, nonce, plain, []byte(ad))
}

func (s *Store) open(ad string, box []byte) ([]byte, error) {
	a, _ := chacha20poly1305.NewX(s.key)
	if len(box) < a.NonceSize() {
		return nil, errors.New("state: ciphertext too short")
	}
	return a.Open(nil, box[:a.NonceSize()], box[a.NonceSize():], []byte(ad))
}

// ---- Счётчики ----

// RecordAction учитывает выполненное действие в счётчиках.
func (s *Store) RecordAction(agent, action, class string, amount float64) error {
	_, err := s.db.Exec(`INSERT INTO actions(ts,agent,action,class,amount) VALUES(?,?,?,?,?)`, s.Now().UnixNano(), agent, action, class, amount)
	return err
}

func (s *Store) dayStart() int64 {
	n := s.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location()).UnixNano()
}

// Counters — реализация policy.Counters на текущий момент.
func (s *Store) Counters() policy.Counters { return counters{s} }

type counters struct{ s *Store }

func (c counters) SpentToday() float64 {
	var v sql.NullFloat64
	_ = c.s.db.QueryRow(`SELECT SUM(amount) FROM actions WHERE class='money' AND ts>=?`, c.s.dayStart()).Scan(&v)
	return v.Float64
}

func (c counters) scan(agent, pattern string) (int, float64) {
	rows, err := c.s.db.Query(`SELECT action, amount FROM actions WHERE agent=? AND ts>=?`, agent, c.s.dayStart())
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	var n int
	var sum float64
	for rows.Next() {
		var a string
		var amt float64
		if rows.Scan(&a, &amt) == nil && policy.ActionMatches(pattern, a) {
			n++
			sum += amt
		}
	}
	return n, sum
}

func (c counters) CountToday(agent, pattern string) int {
	n, _ := c.scan(agent, pattern)
	return n
}

func (c counters) AmountToday(agent, pattern string) float64 {
	_, s := c.scan(agent, pattern)
	return s
}

// ---- Метки происхождения ----

// NormalizeValue — нормализация значения для сопоставления (точное совпадение).
func NormalizeValue(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// AddTaint помечает значения как пришедшие из недоверенного источника.
func (s *Store) AddTaint(source string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	now := s.Now().UnixNano()
	for _, v := range values {
		if v = NormalizeValue(v); v != "" {
			if _, err := tx.Exec(`INSERT INTO taint(value,source,ts) VALUES(?,?,?)`, v, source, now); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit()
}

// TaintSources — источники, из которых пришло значение (пусто — чистое).
func (s *Store) TaintSources(value string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT source FROM taint WHERE value=?`, NormalizeValue(value))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ---- Токены агентов ----

// HashToken — sha256 токена (в базе хранятся только хэши).
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// IssueToken выдаёт новый токен агенту; возвращает сам токен (показывается один раз).
func (s *Store) IssueToken(agent, label, kind string) (string, error) {
	return s.IssueTokenTTL(agent, label, kind, 0)
}

// IssueTokenTTL — токен со сроком действия (0 — бессрочный).
func (s *Store) IssueTokenTTL(agent, label, kind string, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := "g4a_" + base64.RawURLEncoding.EncodeToString(b)
	if kind == "" {
		kind = "agent"
	}
	var exp int64
	if ttl > 0 {
		exp = s.Now().Add(ttl).UnixNano()
	}
	_, err := s.db.Exec(`INSERT INTO tokens(hash,agent,label,created,kind,expires) VALUES(?,?,?,?,?,?)`, HashToken(tok), agent, label, s.Now().UnixNano(), kind, exp)
	return tok, err
}

// ErrBadToken — токен неизвестен или отозван.
var ErrBadToken = errors.New("invalid or revoked token")

// AgentByToken возвращает агента по токену доступа (не refresh).
func (s *Store) AgentByToken(tok string) (string, error) {
	agent, kind, label, err := s.lookup(tok)
	_ = label
	if err != nil || kind == "refresh" {
		return "", ErrBadToken
	}
	return agent, nil
}

// RefreshToken проверяет refresh-токен OAuth; возвращает агента и метку (client_id).
func (s *Store) RefreshToken(tok string) (agent, label string, err error) {
	agent, kind, label, err := s.lookup(tok)
	if err != nil || kind != "refresh" {
		return "", "", ErrBadToken
	}
	return agent, label, nil
}

func (s *Store) lookup(tok string) (agent, kind, label string, err error) {
	var revoked int
	var exp int64
	err = s.db.QueryRow(`SELECT agent, kind, label, revoked, expires FROM tokens WHERE hash=?`, HashToken(tok)).Scan(&agent, &kind, &label, &revoked, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (revoked != 0 || (exp != 0 && s.Now().UnixNano() > exp))) {
		return "", "", "", ErrBadToken
	}
	return agent, kind, label, err
}

// RevokeAgent отзывает все токены агента; возвращает число отозванных.
func (s *Store) RevokeAgent(agent string) (int, error) {
	res, err := s.db.Exec(`UPDATE tokens SET revoked=1 WHERE agent=? AND revoked=0`, agent)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RevokeLabel отзывает токены агента с меткой label; возвращает число отозванных.
func (s *Store) RevokeLabel(agent, label string) (int, error) {
	res, err := s.db.Exec(`UPDATE tokens SET revoked=1 WHERE agent=? AND label=? AND revoked=0`, agent, label)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// RevokeToken отзывает один токен.
func (s *Store) RevokeToken(tok string) error {
	_, err := s.db.Exec(`UPDATE tokens SET revoked=1 WHERE hash=?`, HashToken(tok))
	return err
}

// TokenInfo — сведения о токене (без самого токена).
type TokenInfo struct {
	Agent   string    `json:"agent"`
	Label   string    `json:"label"`
	Kind    string    `json:"kind"`
	Created time.Time `json:"created"`
	Revoked bool      `json:"revoked"`
	ID      string    `json:"id"` // первые 12 символов хэша
	// LastUsed — последнее использование (с точностью до 10 с); нулевое — не использовался.
	LastUsed time.Time `json:"last_used,omitempty"`
}

// Tokens — токены всех агентов.
func (s *Store) Tokens() ([]TokenInfo, error) {
	rows, err := s.db.Query(`SELECT t.hash,t.agent,t.label,t.kind,t.created,t.revoked,COALESCE(s.ts,0) FROM tokens t
		LEFT JOIN token_seen s ON s.hash=t.hash ORDER BY t.created`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var t TokenInfo
		var h string
		var c int64
		var r int
		var lu int64
		if err := rows.Scan(&h, &t.Agent, &t.Label, &t.Kind, &c, &r, &lu); err != nil {
			return nil, err
		}
		t.ID, t.Created, t.Revoked = h[:12], time.Unix(0, c), r != 0
		if lu != 0 {
			t.LastUsed = time.Unix(0, lu)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- KV ----

// Get читает значение kv.
func (s *Store) Get(k string) (string, bool) {
	var v string
	if s.db.QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v) != nil {
		return "", false
	}
	return v, true
}

// Set пишет значение kv.
func (s *Store) Set(k, v string) error {
	_, err := s.db.Exec(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// AddEgress пишет запись журнала соединений (куда и сколько отправил агент).
func (s *Store) AddEgress(ts time.Time, rec any) error {
	_, err := s.db.Exec(`INSERT INTO egress_log(ts, rec) VALUES(?,?)`, ts.UnixNano(), mustJSON(rec))
	return err
}

// EgressLog — последние записи журнала соединений.
func (s *Store) EgressLog(limit int) ([]json.RawMessage, error) {
	rows, err := s.db.Query(`SELECT rec FROM egress_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(r))
	}
	return out, rows.Err()
}
