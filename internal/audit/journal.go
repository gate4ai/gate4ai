// SPDX-License-Identifier: FSL-1.1-ALv2

package audit

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	_ "modernc.org/sqlite"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Witness — внешний свидетель чекпоинтов (relay); видит только хэши.
type Witness interface {
	Witness(cp Checkpoint) error
}

// Journal — журнал квитанций.
type Journal struct {
	db       *sql.DB
	key      []byte
	priv     ed25519.PrivateKey
	Evidence *Evidence

	mu        sync.Mutex
	every     int
	witness   Witness
	now       func() time.Time
	lastCPAt  time.Time
	onReceipt []func(Entry)
}

// Entry — квитанция с её хэшем.
type Entry struct {
	Receipt  Receipt `json:"receipt"`
	Hash     string  `json:"hash"`
	Redacted bool    `json:"redacted,omitempty"`
}

// Options — настройки журнала.
type Options struct {
	CheckpointEach int     // чекпоинт каждые N записей (по умолчанию 100)
	Witness        Witness // свидетель (relay), может быть nil
	Now            func() time.Time
}

// Open открывает журнал: SQLite в dbPath, доказательства в evDir, ключи в ks.
func Open(dbPath, evDir string, ks keys.Store, opt Options) (*Journal, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("journal schema: %w", err)
	}
	key, err := keys.RandomBytes(ks, "audit.aead", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	seed, err := keys.RandomBytes(ks, "device.ed25519", ed25519.SeedSize)
	if err != nil {
		return nil, err
	}
	ev, err := OpenEvidence(evDir, ks)
	if err != nil {
		return nil, err
	}
	j := &Journal{db: db, key: key, priv: ed25519.NewKeyFromSeed(seed), Evidence: ev,
		every: opt.CheckpointEach, witness: opt.Witness, now: opt.Now}
	if j.every <= 0 {
		j.every = 100
	}
	if j.now == nil {
		j.now = time.Now
	}
	var ts sql.NullInt64
	_ = db.QueryRow(`SELECT MAX(ts) FROM checkpoints`).Scan(&ts)
	if ts.Valid {
		j.lastCPAt = time.Unix(0, ts.Int64)
	}
	return j, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS receipts(
  seq INTEGER PRIMARY KEY,
  id TEXT NOT NULL UNIQUE,
  ts INTEGER NOT NULL,
  agent TEXT NOT NULL,
  action TEXT NOT NULL,
  class TEXT NOT NULL,
  effect TEXT NOT NULL,
  hash TEXT NOT NULL UNIQUE,
  prev_hash TEXT NOT NULL,
  preview_hash TEXT NOT NULL DEFAULT '',
  evidence TEXT NOT NULL DEFAULT '[]',
  body BLOB,
  redacted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS receipts_ts ON receipts(ts);
CREATE INDEX IF NOT EXISTS receipts_agent ON receipts(agent, ts);
CREATE INDEX IF NOT EXISTS receipts_action ON receipts(action, ts);
CREATE TABLE IF NOT EXISTS checkpoints(
  seq INTEGER PRIMARY KEY,
  head TEXT NOT NULL,
  ts INTEGER NOT NULL,
  sig TEXT NOT NULL,
  witnessed INTEGER NOT NULL DEFAULT 0
);`

// Close закрывает базу.
func (j *Journal) Close() error { return j.db.Close() }

// PublicKey — публичный ключ устройства (Ed25519).
func (j *Journal) PublicKey() ed25519.PublicKey { return j.priv.Public().(ed25519.PublicKey) }

// OnReceipt подписывает на новые квитанции (Web UI, трей).
func (j *Journal) OnReceipt(f func(Entry)) {
	j.mu.Lock()
	j.onReceipt = append(j.onReceipt, f)
	j.mu.Unlock()
}

// SetWitness задаёт свидетеля (relay).
func (j *Journal) SetWitness(w Witness) {
	j.mu.Lock()
	j.witness = w
	j.mu.Unlock()
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "r_" + hex.EncodeToString(b)
}

func (j *Journal) seal(id string, plain []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(j.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plain, []byte("gate4ai-receipt:"+id)), nil
}

func (j *Journal) open(id string, box []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(j.key)
	if err != nil {
		return nil, err
	}
	if len(box) < a.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return a.Open(nil, box[:a.NonceSize()], box[a.NonceSize():], []byte("gate4ai-receipt:"+id))
}

// Append дописывает квитанцию в цепочку и возвращает её хэш.
func (j *Journal) Append(r *Receipt) (string, error) {
	j.mu.Lock()
	var prev string
	var seq int64
	err := j.db.QueryRow(`SELECT seq, hash FROM receipts ORDER BY seq DESC LIMIT 1`).Scan(&seq, &prev)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		j.mu.Unlock()
		return "", err
	}
	r.Seq = seq + 1
	r.PrevHash = prev
	if r.ID == "" {
		r.ID = newID()
	}
	if r.TS.IsZero() {
		r.TS = j.now()
	}
	r.TS = r.TS.UTC().Truncate(time.Microsecond)
	canon, err := r.Canonical()
	if err != nil {
		j.mu.Unlock()
		return "", err
	}
	hash := HashBytes(canon)
	box, err := j.seal(r.ID, canon)
	if err != nil {
		j.mu.Unlock()
		return "", err
	}
	ev, _ := json.Marshal(r.Verification.EvidenceRefs)
	preview := ""
	if r.Approval != nil {
		preview = r.Approval.PreviewHash
	}
	_, err = j.db.Exec(`INSERT INTO receipts(seq,id,ts,agent,action,class,effect,hash,prev_hash,preview_hash,evidence,body) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Seq, r.ID, r.TS.UnixNano(), r.Agent, r.Action, r.Class, r.Decision.Effect, hash, prev, preview, string(ev), box)
	if err != nil {
		j.mu.Unlock()
		return "", err
	}
	var cpErr error
	if r.Seq%int64(j.every) == 0 {
		_, cpErr = j.checkpointLocked()
	}
	subs := append([]func(Entry){}, j.onReceipt...)
	j.mu.Unlock()
	for _, f := range subs {
		f(Entry{Receipt: *r, Hash: hash})
	}
	return hash, cpErr
}

// Filter — выборка из журнала.
type Filter struct {
	Agent, Action, Effect string
	Since, Until          time.Time
	Limit                 int
	Desc                  bool
}

// List возвращает квитанции по фильтру.
func (j *Journal) List(f Filter) ([]Entry, error) {
	q := `SELECT seq,id,hash,body,redacted,ts,agent,action,class,effect,prev_hash FROM receipts WHERE 1=1`
	var args []any
	if f.Agent != "" {
		q += ` AND agent=?`
		args = append(args, f.Agent)
	}
	if f.Action != "" {
		q += ` AND action=?`
		args = append(args, f.Action)
	}
	if f.Effect != "" {
		q += ` AND effect=?`
		args = append(args, f.Effect)
	}
	if !f.Since.IsZero() {
		q += ` AND ts>=?`
		args = append(args, f.Since.UnixNano())
	}
	if !f.Until.IsZero() {
		q += ` AND ts<?`
		args = append(args, f.Until.UnixNano())
	}
	if f.Desc {
		q += ` ORDER BY seq DESC`
	} else {
		q += ` ORDER BY seq`
	}
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := j.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := j.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(...any) error }

func (j *Journal) scan(s scanner) (Entry, error) {
	var (
		seq, ts                                int64
		id, hash, agent, action, class, effect string
		prev                                   string
		body                                   []byte
		red                                    int
	)
	if err := s.Scan(&seq, &id, &hash, &body, &red, &ts, &agent, &action, &class, &effect, &prev); err != nil {
		return Entry{}, err
	}
	e := Entry{Hash: hash, Redacted: red != 0}
	if e.Redacted {
		e.Receipt = Receipt{Seq: seq, ID: id, TS: time.Unix(0, ts).UTC(), Agent: agent, Action: action, Class: class,
			Decision: Decision{Effect: effect}, PrevHash: prev}
		return e, nil
	}
	plain, err := j.open(id, body)
	if err != nil {
		return Entry{}, fmt.Errorf("receipt %s: decrypt: %w", id, err)
	}
	if err := json.Unmarshal(plain, &e.Receipt); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// ErrNotFound — квитанция не найдена.
var ErrNotFound = errors.New("receipt not found")

// Find ищет квитанцию по id, префиксу хэша (с "#" или без) или хэшу превью.
func (j *Journal) Find(key string) (Entry, error) {
	key = strings.TrimPrefix(strings.TrimSpace(key), "#")
	if key == "" {
		return Entry{}, ErrNotFound
	}
	const cols = `SELECT seq,id,hash,body,redacted,ts,agent,action,class,effect,prev_hash FROM receipts `
	var rows *sql.Rows
	var err error
	if strings.HasPrefix(key, "r_") {
		rows, err = j.db.Query(cols+`WHERE id=?`, key)
	} else {
		if len(key) < 6 || strings.Trim(strings.ToLower(key), "0123456789abcdef") != "" {
			return Entry{}, fmt.Errorf("hash prefix must be at least 6 hex characters")
		}
		k := strings.ToLower(key) + "%"
		rows, err = j.db.Query(cols+`WHERE hash LIKE ? OR preview_hash LIKE ? LIMIT 2`, k, k)
	}
	if err != nil {
		return Entry{}, err
	}
	defer rows.Close()
	var found []Entry
	for rows.Next() {
		e, err := j.scan(rows)
		if err != nil {
			return Entry{}, err
		}
		found = append(found, e)
	}
	switch len(found) {
	case 0:
		return Entry{}, ErrNotFound
	case 1:
		return found[0], nil
	}
	return Entry{}, fmt.Errorf("hash prefix %q is ambiguous", key)
}

// Head — номер и хэш последней квитанции.
func (j *Journal) Head() (int64, string, error) {
	var seq int64
	var hash string
	err := j.db.QueryRow(`SELECT seq, hash FROM receipts ORDER BY seq DESC LIMIT 1`).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return seq, hash, err
}
