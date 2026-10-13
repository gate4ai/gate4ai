// SPDX-License-Identifier: FSL-1.1-ALv2

package audit

import (
	"encoding/json"
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
func (j *Journal) Purge(now time.Time, ret Retention) (PurgeReport, error) {
	var rep PurgeReport
	type row struct {
		seq   int64
		class string
		ev    string
	}
	rows, err := j.db.Query(`SELECT seq,class,evidence FROM receipts WHERE evidence != '[]' AND ts < ?`,
		now.Add(-minPositive(ret.EvidenceMoney, ret.EvidenceOther)).UnixNano())
	if err != nil {
		return rep, err
	}
	var cand []row
	tsOf := map[int64]int64{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.class, &r.ev); err != nil {
			rows.Close()
			return rep, err
		}
		cand = append(cand, r)
	}
	rows.Close()
	for _, r := range cand {
		var ts int64
		_ = j.db.QueryRow(`SELECT ts FROM receipts WHERE seq=?`, r.seq).Scan(&ts)
		tsOf[r.seq] = ts
		keep := ret.EvidenceOther
		if r.class == "money" {
			keep = ret.EvidenceMoney
		}
		if keep <= 0 || time.Unix(0, ts).After(now.Add(-keep)) {
			continue
		}
		var refs []string
		_ = json.Unmarshal([]byte(r.ev), &refs)
		for _, ref := range refs {
			if err := j.Evidence.Delete(ref); err != nil {
				return rep, err
			}
			rep.EvidenceDeleted++
		}
		if _, err := j.db.Exec(`UPDATE receipts SET evidence='[]' WHERE seq=?`, r.seq); err != nil {
			return rep, err
		}
	}
	redact := func(where string, before time.Time) error {
		res, err := j.db.Exec(`UPDATE receipts SET body=NULL, redacted=1 WHERE redacted=0 AND ts < ? `+where, before.UnixNano())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		rep.Redacted += int(n)
		return nil
	}
	if ret.Denied > 0 {
		if err := redact(`AND effect='deny'`, now.Add(-ret.Denied)); err != nil {
			return rep, err
		}
	}
	if ret.Receipts > 0 {
		if err := redact(``, now.Add(-ret.Receipts)); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func minPositive(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	}
	return b
}
