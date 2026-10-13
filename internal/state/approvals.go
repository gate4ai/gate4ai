// SPDX-License-Identifier: FSL-1.1-ALv2

package state

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/gate4ai/gate4ai/internal/policy"
)

// Статусы подтверждения.
const (
	Pending  = "pending"
	Approved = "approved"
	Rejected = "rejected"
	Timeout  = "timeout"
	Done     = "done" // исполнено после подтверждения (Result заполнен)
)

// Approval — запрос подтверждения в очереди.
type Approval struct {
	ID          string              `json:"id"`
	Created     time.Time           `json:"created"`
	Expires     time.Time           `json:"expires"`
	Agent       string              `json:"agent"`
	Action      string              `json:"action"`
	Class       string              `json:"class"`
	Status      string              `json:"status"`
	Preview     string              `json:"preview"`      // что именно будет сделано (человекочитаемо)
	PreviewHash string              `json:"preview_hash"` // хэш превью (короткий — в мессенджере)
	Decision    policy.Decision     `json:"decision"`     // почему нужно подтверждение
	Args        map[string]any      `json:"-"`            // полные аргументы (зашифрованы в базе)
	Provenance  map[string][]string `json:"-"`
	Channel     string              `json:"channel,omitempty"`
	Who         string              `json:"who,omitempty"`
	DecidedAt   time.Time           `json:"decided_at,omitempty"`
	Result      json.RawMessage     `json:"result,omitempty"` // итог исполнения для `status <id>`
	Nonce       string              `json:"-"`                // одноразовый секрет для кнопок
}

// NewApprovalID — идентификатор подтверждения.
func NewApprovalID() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "a_" + hex.EncodeToString(b)
}

// PutApproval ставит запрос в очередь.
func (s *Store) PutApproval(a *Approval) error {
	if a.ID == "" {
		a.ID = NewApprovalID()
	}
	if a.Nonce == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		a.Nonce = hex.EncodeToString(b)
	}
	args, _ := json.Marshal(a.Args)
	_, err := s.db.Exec(`INSERT INTO approvals(id,created,expires,agent,action,class,status,preview,preview_hash,decision,args,provenance,nonce)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Created.UnixNano(), a.Expires.UnixNano(), a.Agent, a.Action, a.Class, a.Status, a.Preview, a.PreviewHash,
		mustJSON(a.Decision), s.seal("approval:"+a.ID, args), mustJSON(a.Provenance), a.Nonce)
	return err
}

// ErrNoApproval — подтверждение не найдено.
var ErrNoApproval = errors.New("approval not found")

// ErrAlreadyDecided — по запросу уже есть решение (запрос одноразовый).
var ErrAlreadyDecided = errors.New("approval already decided")

const approvalCols = `id,created,expires,agent,action,class,status,preview,preview_hash,decision,args,provenance,channel,who,decided_at,result,nonce`

func (s *Store) scanApproval(row interface{ Scan(...any) error }) (*Approval, error) {
	var a Approval
	var created, expires, decided int64
	var dec, prov, result string
	var box []byte
	if err := row.Scan(&a.ID, &created, &expires, &a.Agent, &a.Action, &a.Class, &a.Status, &a.Preview, &a.PreviewHash,
		&dec, &box, &prov, &a.Channel, &a.Who, &decided, &result, &a.Nonce); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoApproval
		}
		return nil, err
	}
	a.Created, a.Expires = time.Unix(0, created), time.Unix(0, expires)
	if decided != 0 {
		a.DecidedAt = time.Unix(0, decided)
	}
	_ = json.Unmarshal([]byte(dec), &a.Decision)
	_ = json.Unmarshal([]byte(prov), &a.Provenance)
	if result != "" {
		a.Result = json.RawMessage(result)
	}
	plain, err := s.open("approval:"+a.ID, box)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(plain, &a.Args); err != nil {
		return nil, err
	}
	return &a, nil
}

// GetApproval читает запрос.
func (s *Store) GetApproval(id string) (*Approval, error) {
	return s.scanApproval(s.db.QueryRow(`SELECT `+approvalCols+` FROM approvals WHERE id=?`, id))
}

// ListApprovals — запросы со статусом status ("" — все), новые первыми.
func (s *Store) ListApprovals(status string, limit int) ([]*Approval, error) {
	q := `SELECT ` + approvalCols + ` FROM approvals`
	var args []any
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Approval
	for rows.Next() {
		a, err := s.scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Decide атомарно переводит pending → status (approved / rejected / timeout).
// Повторное решение возвращает ErrAlreadyDecided.
func (s *Store) Decide(id, status, channel, who string) (*Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE approvals SET status=?, channel=?, who=?, decided_at=? WHERE id=? AND status='pending'`,
		status, channel, who, s.Now().UnixNano(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetApproval(id); err != nil {
			return nil, err
		}
		return nil, ErrAlreadyDecided
	}
	return s.GetApproval(id)
}

// SetResult сохраняет итог исполнения запроса.
func (s *Store) SetResult(id string, result any) error {
	_, err := s.db.Exec(`UPDATE approvals SET result=? WHERE id=?`, mustJSON(result), id)
	return err
}

// MarkDone переводит approved → done.
func (s *Store) MarkDone(id string) error {
	_, err := s.db.Exec(`UPDATE approvals SET status='done' WHERE id=? AND status='approved'`, id)
	return err
}

// Expired — ожидающие запросы с истёкшим сроком.
func (s *Store) Expired() ([]*Approval, error) {
	rows, err := s.db.Query(`SELECT `+approvalCols+` FROM approvals WHERE status='pending' AND expires<=?`, s.Now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Approval
	for rows.Next() {
		a, err := s.scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
