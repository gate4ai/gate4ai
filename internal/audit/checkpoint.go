// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Checkpoint — подписанная отметка головы цепочки.
type Checkpoint struct {
	Seq  int64     `json:"seq"`
	Head string    `json:"head"`
	TS   time.Time `json:"ts"`
	// Prev — хэш подписанных байтов предыдущего чекпоинта: чекпоинты связаны
	// цепочкой, удалить или переставить один из середины незаметно нельзя.
	// Пусто — у первого чекпоинта и у записанных до появления цепочки (v0.7.2 и раньше).
	Prev string `json:"prev,omitempty"`
	Sig  string `json:"sig"` // Ed25519, hex
}

// SignedBytes — канонические байты, которые подписываются.
func (c Checkpoint) SignedBytes() []byte {
	m := map[string]any{"type": "gate4ai-checkpoint", "seq": c.Seq, "head": c.Head, "ts": c.TS.UTC().Format(time.RFC3339Nano)}
	if c.Prev != "" {
		m["prev"] = c.Prev
	}
	b, _ := Canonicalize(m)
	return b
}

// WitnessReader — свидетель, у которого можно спросить последний принятый
// чекпоинт устройства: так видна обрезка хвоста журнала вместе с его
// чекпоинтами. ok=false — свидетель чекпоинтов не видел.
type WitnessReader interface {
	LastWitnessed() (cp Checkpoint, ok bool, err error)
}

// VerifySig проверяет подпись чекпоинта.
func (c Checkpoint) VerifySig(pub ed25519.PublicKey) bool {
	sig, err := hex.DecodeString(c.Sig)
	return err == nil && ed25519.Verify(pub, c.SignedBytes(), sig)
}

// Checkpoint создаёт подписанный чекпоинт текущей головы (если есть новые записи).
func (j *Journal) Checkpoint() (*Checkpoint, error) { return j.checkpoint(false) }

// MaybeDailyCheckpoint — чекпоинт, если с прошлого прошло больше суток.
func (j *Journal) MaybeDailyCheckpoint() (*Checkpoint, error) { return j.checkpoint(true) }

func (j *Journal) checkpoint(daily bool) (*Checkpoint, error) {
	j.mu.Lock()
	if daily && j.now().Sub(j.lastCPAt) < 24*time.Hour {
		j.mu.Unlock()
		return nil, nil
	}
	cp, err := j.checkpointLocked()
	w := j.witness
	j.mu.Unlock()
	j.witnessCP(w, cp)
	return cp, err
}

// checkpointLocked сохраняет чекпоинт (под j.mu); свидетелю его отправляет
// вызывающий после Unlock (witnessCP).
func (j *Journal) checkpointLocked() (*Checkpoint, error) {
	seq, head, err := j.Head()
	if err != nil || seq == 0 {
		return nil, err
	}
	last, err := j.lastCheckpoint()
	if err != nil {
		return nil, err
	}
	if last != nil && last.Seq == seq {
		return nil, nil
	}
	cp := Checkpoint{Seq: seq, Head: head, TS: j.now().UTC().Truncate(time.Microsecond)}
	if last != nil {
		cp.Prev = HashBytes(last.SignedBytes())
	}
	cp.Sig = hex.EncodeToString(ed25519.Sign(j.priv, cp.SignedBytes()))
	if _, err := j.db.Exec(`INSERT INTO checkpoints(seq,head,ts,sig,prev) VALUES(?,?,?,?,?)`, cp.Seq, cp.Head, cp.TS.UnixNano(), cp.Sig, cp.Prev); err != nil {
		return nil, err
	}
	j.lastCPAt = cp.TS
	return &cp, nil
}

func (j *Journal) lastCheckpoint() (*Checkpoint, error) {
	cps, err := j.checkpoints(`WHERE seq=(SELECT MAX(seq) FROM checkpoints)`)
	if err != nil || len(cps) == 0 {
		return nil, err
	}
	return &cps[0], nil
}

// witnessCP отправляет чекпоинт свидетелю (не под j.mu); неудача — повтор
// в RetryWitness.
func (j *Journal) witnessCP(w Witness, cp *Checkpoint) {
	if w == nil || cp == nil {
		return
	}
	if err := w.Witness(*cp); err == nil {
		_, _ = j.db.Exec(`UPDATE checkpoints SET witnessed=1 WHERE seq=?`, cp.Seq)
	}
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
	rows, err := j.db.Query(`SELECT seq,head,ts,sig,prev FROM checkpoints ` + where + ` ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var ts int64
		if err := rows.Scan(&c.Seq, &c.Head, &ts, &c.Sig, &c.Prev); err != nil {
			return nil, err
		}
		c.TS = time.Unix(0, ts).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// VerifyChain проверяет всю цепочку и чекпоинты локального журнала; если
// свидетель умеет отвечать (WitnessReader) — и что журнал не короче
// последнего засвидетельствованного чекпоинта.
func (j *Journal) VerifyChain() error {
	entries, err := j.List(Filter{All: true})
	if err != nil {
		return err
	}
	cps, err := j.Checkpoints()
	if err != nil {
		return err
	}
	if err := verifyChain(entries, cps, j.PublicKey()); err != nil {
		return err
	}
	j.mu.Lock()
	w := j.witness
	j.mu.Unlock()
	if wr, ok := w.(WitnessReader); ok {
		cp, seen, err := wr.LastWitnessed()
		if err != nil || !seen {
			return nil // свидетель недоступен — проверка только локальная
		}
		if cp.Seq > int64(len(entries)) || entries[cp.Seq-1].Hash != cp.Head {
			return fmt.Errorf("%w: witnessed checkpoint %d is missing locally (journal truncated)", ErrChainBroken, cp.Seq)
		}
	}
	return nil
}

// ErrChainBroken — нарушена целостность цепочки.
var ErrChainBroken = errors.New("chain broken")

// Запись о редактировании квитанций по сроку хранения: обычная квитанция
// цепочки (не редактируется), покрытая чекпоинтом.
const (
	RedactAgent  = "gate4ai"
	RedactAction = "journal.redact"
)

// redactRec — отредактированная квитанция в записи journal.redact.
type redactRec struct {
	Seq    int64  `json:"seq"`
	Hash   string `json:"hash"`
	Header string `json:"header"`
}

func redactRecs(r Receipt) []redactRec {
	b, _ := json.Marshal(r.Args["receipts"])
	var out []redactRec
	_ = json.Unmarshal(b, &out)
	return out
}

func verifyChain(entries []Entry, cps []Checkpoint, pub ed25519.PublicKey) error {
	prev := ""
	hashes := map[int64]string{}
	var lastCP int64
	for _, cp := range cps {
		lastCP = max(lastCP, cp.Seq)
	}
	// Отредактированная квитанция законна, только если её перечисляет запись
	// journal.redact, покрытая подписанным чекпоинтом, с тем же хэшем и хэшем
	// заголовка: иначе redacted=1 позволил бы скрыть или подменить любую.
	redacted := map[int64]redactRec{}
	for _, e := range entries {
		r := e.Receipt
		if e.Redacted || r.Agent != RedactAgent || r.Action != RedactAction || r.Seq > lastCP {
			continue
		}
		for _, x := range redactRecs(r) {
			if x.Seq < r.Seq {
				redacted[x.Seq] = x
			}
		}
	}
	for i, e := range entries {
		r := e.Receipt
		if r.Seq != int64(i+1) {
			return fmt.Errorf("%w: expected seq %d, got %d", ErrChainBroken, i+1, r.Seq)
		}
		if r.PrevHash != prev {
			return fmt.Errorf("%w: seq %d prev_hash mismatch", ErrChainBroken, r.Seq)
		}
		if e.Redacted {
			x, ok := redacted[r.Seq]
			switch {
			case !ok:
				return fmt.Errorf("%w: seq %d is redacted without a signed redaction record", ErrChainBroken, r.Seq)
			case x.Hash != e.Hash:
				return fmt.Errorf("%w: seq %d redacted hash mismatch", ErrChainBroken, r.Seq)
			case x.Header != HeaderHash(r):
				return fmt.Errorf("%w: seq %d redacted metadata changed", ErrChainBroken, r.Seq)
			}
		} else {
			if e.Body == nil {
				return fmt.Errorf("%w: seq %d has no receipt document", ErrChainBroken, r.Seq)
			}
			h, err := formatHash(e.Format, e.Body)
			if err != nil {
				return fmt.Errorf("seq %d: %w", r.Seq, err)
			}
			if h != e.Hash {
				return fmt.Errorf("%w: seq %d content hash mismatch", ErrChainBroken, r.Seq)
			}
		}
		prev = e.Hash
		hashes[r.Seq] = e.Hash
	}
	// Чекпоинты журналов v0.7.2 и раньше не связаны prev: несвязанным может
	// быть только начало списка; после первого связанного — все связаны.
	chained := false
	for i, cp := range cps {
		if !cp.VerifySig(pub) {
			return fmt.Errorf("%w: checkpoint %d bad signature", ErrChainBroken, cp.Seq)
		}
		if hashes[cp.Seq] != cp.Head {
			return fmt.Errorf("%w: checkpoint %d head mismatch", ErrChainBroken, cp.Seq)
		}
		switch {
		case cp.Prev != "":
			if i == 0 || HashBytes(cps[i-1].SignedBytes()) != cp.Prev {
				return fmt.Errorf("%w: checkpoint %d does not follow the previous one (checkpoint removed)", ErrChainBroken, cp.Seq)
			}
			chained = true
		case chained:
			return fmt.Errorf("%w: checkpoint %d is not linked to the previous one", ErrChainBroken, cp.Seq)
		}
	}
	return nil
}
