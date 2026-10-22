// SPDX-License-Identifier: AGPL-3.0-only

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

	mu       sync.Mutex
	every    int
	witness  Witness
	now      func() time.Time
	lastCPAt time.Time
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
	if err := migrate(db); err != nil {
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
	if err := j.coverLegacyRedactions(); err != nil {
		db.Close()
		return nil, fmt.Errorf("journal: %w", err)
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

// migrate добавляет колонки, появившиеся после первой версии схемы:
// receipts.kind ('intent' — квитанция-намерение), receipts.intent (ссылка
// итоговой квитанции на намерение), checkpoints.prev (цепочка чекпоинтов).
func migrate(db *sql.DB) error {
	for _, c := range []struct{ table, col, def string }{
		{"receipts", "kind", "TEXT NOT NULL DEFAULT ''"},
		{"receipts", "intent", "TEXT NOT NULL DEFAULT ''"},
		{"checkpoints", "prev", "TEXT NOT NULL DEFAULT ''"},
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.col).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.col + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS receipts_kind ON receipts(kind)`)
	return err
}

// kindIntent — квитанция-намерение (execution.status "started").
const kindIntent = "intent"

// Close закрывает базу.
func (j *Journal) Close() error { return j.db.Close() }

// PublicKey — публичный ключ устройства (Ed25519).
func (j *Journal) PublicKey() ed25519.PublicKey { return j.priv.Public().(ed25519.PublicKey) }

// SetWitness задаёт свидетеля (relay).
func (j *Journal) SetWitness(w Witness) {
	j.mu.Lock()
	j.witness = w
	j.mu.Unlock()
}

// NewID — идентификатор квитанции (его можно задать до записи: вложенные вызовы ссылаются на него).
func NewID() string {
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

// Append дописывает квитанцию в цепочку и возвращает её хэш. Непустой хэш —
// квитанция записана (ошибка тогда — только чекпоинта); пустой — не записана.
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
		r.ID = NewID()
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
	kind := ""
	if r.Execution.Status == ExecStarted {
		kind = kindIntent
	}
	_, err = j.db.Exec(`INSERT INTO receipts(seq,id,ts,agent,action,class,effect,hash,prev_hash,preview_hash,evidence,body,kind,intent) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Seq, r.ID, r.TS.UnixNano(), r.Agent, r.Action, r.Class, r.Decision.Effect, hash, prev, preview, string(ev), box, kind, r.Intent)
	if err != nil {
		j.mu.Unlock()
		return "", err
	}
	var cpErr error
	var cp *Checkpoint
	if r.Seq%int64(j.every) == 0 {
		cp, cpErr = j.checkpointLocked()
	}
	w := j.witness
	j.mu.Unlock()
	// Свидетель (HTTP к relay) — вне мьютекса: запись журнала его не ждёт.
	j.witnessCP(w, cp)
	return hash, cpErr
}

// OpenIntents — квитанции-намерения без итоговой квитанции: исполнение
// прервала остановка daemon (восстановление при старте).
func (j *Journal) OpenIntents() ([]Entry, error) {
	return j.query(`SELECT `+entryCols+` FROM receipts r
		WHERE kind=? AND NOT EXISTS (SELECT 1 FROM receipts f WHERE f.intent=r.id) ORDER BY seq`, kindIntent)
}

// Filter — выборка из журнала.
type Filter struct {
	Agent, Action, Effect string
	Since, Until          time.Time
	Limit                 int
	Desc                  bool
	// All — и квитанции-намерения (для проверки цепочки и экспорта); без него
	// в выборке только итоговые квитанции.
	All bool
}

// List возвращает квитанции по фильтру.
func (j *Journal) List(f Filter) ([]Entry, error) {
	q := `SELECT ` + entryCols + ` FROM receipts WHERE 1=1`
	var args []any
	if !f.All {
		q += ` AND kind != 'intent'`
	}
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
	return j.query(q, args...)
}

// query — квитанции по запросу SELECT entryCols.
func (j *Journal) query(q string, args ...any) ([]Entry, error) {
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

// entryCols — колонки квитанции для scan и scanIndex.
const entryCols = `seq,id,hash,body,redacted,ts,agent,action,class,effect,prev_hash`

// scan — квитанция с расшифрованным телом (отредактированная — только индекс).
func (j *Journal) scan(s scanner) (Entry, error) {
	e, body, err := scanIndex(s)
	if err != nil || e.Redacted {
		return e, err
	}
	id := e.Receipt.ID
	e.Receipt = Receipt{}
	plain, err := j.open(id, body)
	if err != nil {
		return Entry{}, fmt.Errorf("receipt %s: decrypt: %w", id, err)
	}
	if err := json.Unmarshal(plain, &e.Receipt); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// scanIndex — квитанция только по колонкам индекса (без расшифровки) и её
// зашифрованное тело.
func scanIndex(s scanner) (Entry, []byte, error) {
	var (
		seq, ts                                      int64
		id, hash, agent, action, class, effect, prev string
		body                                         []byte
		red                                          int
	)
	if err := s.Scan(&seq, &id, &hash, &body, &red, &ts, &agent, &action, &class, &effect, &prev); err != nil {
		return Entry{}, nil, err
	}
	return Entry{Hash: hash, Redacted: red != 0, Receipt: Receipt{Seq: seq, ID: id, TS: time.Unix(0, ts).UTC(), Agent: agent, Action: action,
		Class: class, Decision: Decision{Effect: effect}, PrevHash: prev}}, body, nil
}

// ErrNotFound — квитанция не найдена.
var ErrNotFound = errors.New("receipt not found")

// Find ищет квитанцию по id, префиксу хэша (с "#" или без) или хэшу превью.
func (j *Journal) Find(key string) (Entry, error) {
	key = strings.TrimPrefix(strings.TrimSpace(key), "#")
	if key == "" {
		return Entry{}, ErrNotFound
	}
	const cols = `SELECT ` + entryCols + ` FROM receipts `
	var found []Entry
	var err error
	if strings.HasPrefix(key, "r_") {
		found, err = j.query(cols+`WHERE id=?`, key)
	} else {
		if len(key) < 6 || strings.Trim(strings.ToLower(key), "0123456789abcdef") != "" {
			return Entry{}, fmt.Errorf("hash prefix must be at least 6 hex characters")
		}
		k := strings.ToLower(key) + "%"
		// По хэшу превью находится итоговая квитанция, не намерение того же действия.
		found, err = j.query(cols+`WHERE hash LIKE ? OR (preview_hash LIKE ? AND kind != 'intent') LIMIT 2`, k, k)
	}
	if err != nil {
		return Entry{}, err
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
