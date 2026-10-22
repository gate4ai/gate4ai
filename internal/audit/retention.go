// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"encoding/json"
	"slices"
	"time"
)

// Retention — сроки хранения; 0 — бессрочно.
type Retention struct {
	Receipts      time.Duration // квитанции целиком (по умолчанию бессрочно)
	EvidenceMoney time.Duration // доказательства действий класса money (3 года)
	EvidenceOther time.Duration // прочие доказательства (90 дней)
	Denied        time.Duration // квитанции отказов deny (1 год)
}

// PurgeReport — что удалено.
type PurgeReport struct {
	EvidenceDeleted int `json:"evidence_deleted"`
	Redacted        int `json:"redacted"`
}

// Purge применяет сроки хранения. Квитанции не удаляются из цепочки, а
// «редактируются»: тело (аргументы, причины) стирается, хэш и связь
// prev_hash остаются, поэтому цепочка и чекпоинты продолжают сходиться.
// Что отредактировано, записывает квитанция journal.redact (хэш и хэш
// заголовка каждой) под новым чекпоинтом — без неё пометка redacted при
// проверке считается подделкой.
//
// Доказательства хранятся одним файлом на содержимое (имя — sha256), поэтому
// файл удаляется, только когда на него не ссылается ни одна квитанция с
// неистёкшим сроком (и он не использован только что — Put).
func (j *Journal) Purge(now time.Time, ret Retention) (PurgeReport, error) {
	var rep PurgeReport
	type row struct {
		seq   int64
		class string
		ts    int64
		refs  []string
	}
	rows, err := j.db.Query(`SELECT seq,class,ts,evidence FROM receipts WHERE evidence != '[]'`)
	if err != nil {
		return rep, err
	}
	var expired []row
	live := map[string]bool{}
	for rows.Next() {
		var r row
		var ev string
		if err := rows.Scan(&r.seq, &r.class, &r.ts, &ev); err != nil {
			rows.Close()
			return rep, err
		}
		_ = json.Unmarshal([]byte(ev), &r.refs)
		keep := ret.EvidenceOther
		if r.class == "money" {
			keep = ret.EvidenceMoney
		}
		if keep > 0 && !time.Unix(0, r.ts).After(now.Add(-keep)) {
			expired = append(expired, r)
			continue
		}
		for _, ref := range r.refs {
			live[ref] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rep, err
	}
	rows.Close()
	for _, r := range expired {
		for _, ref := range r.refs {
			if live[ref] || j.Evidence.recentlyUsed(ref, now) {
				continue // доказательство другой, ещё хранимой квитанции
			}
			if err := j.Evidence.Delete(ref); err != nil {
				return rep, err
			}
			rep.EvidenceDeleted++
		}
		if _, err := j.db.Exec(`UPDATE receipts SET evidence='[]' WHERE seq=?`, r.seq); err != nil {
			return rep, err
		}
	}
	var seqs []int64
	pick := func(where string, before time.Time) error {
		found, err := j.seqs(`SELECT seq FROM receipts WHERE redacted=0 AND ts < ? AND action != ? `+where, before.UnixNano(), RedactAction)
		for _, s := range found {
			if !slices.Contains(seqs, s) {
				seqs = append(seqs, s)
			}
		}
		return err
	}
	if ret.Denied > 0 {
		// Отказы политики; отклонения и таймауты человека (есть approval —
		// preview_hash) — квитанции решения человека, хранятся как квитанции.
		if err := pick(`AND effect='deny' AND preview_hash=''`, now.Add(-ret.Denied)); err != nil {
			return rep, err
		}
	}
	if ret.Receipts > 0 {
		if err := pick(``, now.Add(-ret.Receipts)); err != nil {
			return rep, err
		}
	}
	n, err := j.redact(seqs)
	rep.Redacted = n
	return rep, err
}

// redact записывает квитанцию journal.redact со списком квитанций (хэш и хэш
// заголовка), стирает их тела и ставит чекпоинт, покрывающий запись.
func (j *Journal) redact(seqs []int64) (int, error) {
	if len(seqs) == 0 {
		return 0, nil
	}
	slices.Sort(seqs)
	var recs []redactRec
	for _, s := range seqs {
		e, err := j.scan(j.db.QueryRow(`SELECT `+entryCols+` FROM receipts WHERE seq=?`, s))
		if err != nil {
			return 0, err
		}
		recs = append(recs, redactRec{Seq: s, Hash: e.Hash, Header: HeaderHash(e.Receipt)})
	}
	r := &Receipt{Agent: RedactAgent, Action: RedactAction, Class: "write", Args: map[string]any{"receipts": recs},
		Decision:     Decision{Effect: "allow", RuleIDs: []string{"retention"}, Reason: "retention period expired"},
		Execution:    Execution{Status: "ok"},
		Verification: Verification{Status: "not_applicable"}}
	if hash, err := j.Append(r); hash == "" {
		return 0, err
	}
	n := 0
	for _, s := range seqs {
		res, err := j.db.Exec(`UPDATE receipts SET body=NULL, redacted=1 WHERE seq=? AND redacted=0`, s)
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	_, err := j.Checkpoint()
	return n, err
}

// seqs — номера квитанций по запросу SELECT seq.
func (j *Journal) seqs(q string, args ...any) ([]int64, error) {
	rows, err := j.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
