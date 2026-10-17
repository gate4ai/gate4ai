// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Checkpoint — подписанная отметка головы цепочки.
type Checkpoint struct {
	Seq  int64     `json:"seq"`
	Head string    `json:"head"`
	TS   time.Time `json:"ts"`
	Sig  string    `json:"sig"` // Ed25519, hex
}

// SignedBytes — канонические байты, которые подписываются.
func (c Checkpoint) SignedBytes() []byte {
	b, _ := Canonicalize(map[string]any{"type": "gate4ai-checkpoint", "seq": c.Seq, "head": c.Head, "ts": c.TS.UTC().Format(time.RFC3339Nano)})
	return b
}

// VerifySig проверяет подпись чекпоинта.
func (c Checkpoint) VerifySig(pub ed25519.PublicKey) bool {
	sig, err := hex.DecodeString(c.Sig)
	return err == nil && ed25519.Verify(pub, c.SignedBytes(), sig)
}

// Checkpoint создаёт подписанный чекпоинт текущей головы (если есть новые записи).
func (j *Journal) Checkpoint() (*Checkpoint, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.checkpointLocked()
}

// MaybeDailyCheckpoint — чекпоинт, если с прошлого прошло больше суток.
func (j *Journal) MaybeDailyCheckpoint() (*Checkpoint, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.now().Sub(j.lastCPAt) < 24*time.Hour {
		return nil, nil
	}
	return j.checkpointLocked()
}

func (j *Journal) checkpointLocked() (*Checkpoint, error) {
	seq, head, err := j.Head()
	if err != nil || seq == 0 {
		return nil, err
	}
	var last int64
	_ = j.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM checkpoints`).Scan(&last)
	if last == seq {
		return nil, nil
	}
	cp := Checkpoint{Seq: seq, Head: head, TS: j.now().UTC().Truncate(time.Microsecond)}
	cp.Sig = hex.EncodeToString(ed25519.Sign(j.priv, cp.SignedBytes()))
	if _, err := j.db.Exec(`INSERT INTO checkpoints(seq,head,ts,sig) VALUES(?,?,?,?)`, cp.Seq, cp.Head, cp.TS.UnixNano(), cp.Sig); err != nil {
		return nil, err
	}
	j.lastCPAt = cp.TS
	if j.witness != nil {
		if err := j.witness.Witness(cp); err == nil {
			_, _ = j.db.Exec(`UPDATE checkpoints SET witnessed=1 WHERE seq=?`, cp.Seq)
		}
	}
	return &cp, nil
}

// RetryWitness отправляет свидетелю чекпоинты, которые он ещё не подтвердил.
func (j *Journal) RetryWitness() error {
	j.mu.Lock()
	w := j.witness
	j.mu.Unlock()
	if w == nil {
		return nil
	}
	cps, err := j.checkpoints(`WHERE witnessed=0`)
	if err != nil {
		return err
	}
	for _, cp := range cps {
		if err := w.Witness(cp); err != nil {
			return err
		}
		_, _ = j.db.Exec(`UPDATE checkpoints SET witnessed=1 WHERE seq=?`, cp.Seq)
	}
	return nil
}

// Checkpoints — все чекпоинты по порядку.
func (j *Journal) Checkpoints() ([]Checkpoint, error) { return j.checkpoints("") }

func (j *Journal) checkpoints(where string) ([]Checkpoint, error) {
	rows, err := j.db.Query(`SELECT seq,head,ts,sig FROM checkpoints ` + where + ` ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var ts int64
		if err := rows.Scan(&c.Seq, &c.Head, &ts, &c.Sig); err != nil {
			return nil, err
		}
		c.TS = time.Unix(0, ts).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// VerifyChain проверяет всю цепочку и чекпоинты локального журнала.
func (j *Journal) VerifyChain() error {
	entries, err := j.List(Filter{})
	if err != nil {
		return err
	}
	cps, err := j.Checkpoints()
	if err != nil {
		return err
	}
	return verifyChain(entries, cps, j.PublicKey())
}

// ErrChainBroken — нарушена целостность цепочки.
var ErrChainBroken = errors.New("chain broken")

func verifyChain(entries []Entry, cps []Checkpoint, pub ed25519.PublicKey) error {
	prev := ""
	hashes := map[int64]string{}
	for i, e := range entries {
		r := e.Receipt
		if r.Seq != int64(i+1) {
			return fmt.Errorf("%w: expected seq %d, got %d", ErrChainBroken, i+1, r.Seq)
		}
		if r.PrevHash != prev {
			return fmt.Errorf("%w: seq %d prev_hash mismatch", ErrChainBroken, r.Seq)
		}
		if !e.Redacted {
			h, err := r.Hash()
			if err != nil {
				return err
			}
			if h != e.Hash {
				return fmt.Errorf("%w: seq %d content hash mismatch", ErrChainBroken, r.Seq)
			}
		}
		prev = e.Hash
		hashes[r.Seq] = e.Hash
	}
	for _, cp := range cps {
		if !cp.VerifySig(pub) {
			return fmt.Errorf("%w: checkpoint %d bad signature", ErrChainBroken, cp.Seq)
		}
		if hashes[cp.Seq] != cp.Head {
			return fmt.Errorf("%w: checkpoint %d head mismatch", ErrChainBroken, cp.Seq)
		}
	}
	return nil
}
