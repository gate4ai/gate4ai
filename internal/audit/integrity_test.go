// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

const day = 24 * time.Hour

var testRetention = Retention{EvidenceMoney: 3 * 365 * day, EvidenceOther: 90 * day, Denied: 365 * day}

// Одно содержимое у квитанций money и external: очистка 90-дневного
// доказательства не удаляет файл, на который ссылается money-квитанция.
func TestPurgeKeepsSharedEvidence(t *testing.T) {
	c := &clock{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	j, _ := openTest(t, keys.NewMemory(), c, nil, 100)
	ref, err := j.Evidence.Put([]byte("same server answer"))
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"money", "external"} {
		r := sample(1, "allow", class)
		r.Verification.EvidenceRefs = []string{ref}
		if _, err := j.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	c.t = c.t.Add(100 * day)
	rep, err := j.Purge(c.t, testRetention)
	if err != nil {
		t.Fatal(err)
	}
	if rep.EvidenceDeleted != 0 {
		t.Fatalf("shared evidence deleted: %+v", rep)
	}
	if _, err := j.Evidence.Get(ref); err != nil {
		t.Fatal("money evidence must be kept 3 years:", err)
	}
	es, _ := j.List(Filter{})
	if len(es[1].Receipt.Verification.EvidenceRefs) != 1 {
		t.Fatal("index of the expired receipt must be cleared")
	}
	// Срок money тоже истёк — файл удаляется.
	c.t = c.t.Add(4 * 365 * day)
	if rep, _ := j.Purge(c.t, testRetention); rep.EvidenceDeleted != 1 {
		t.Fatalf("purge after both expired: %+v", rep)
	}
	if err := j.VerifyChain(); err != nil {
		t.Fatal(err)
	}
}

// Пометка redacted без подписанной записи journal.redact или с подменёнными
// метаданными — подделка, её замечают VerifyChain и verify архива.
func TestRedactedForgeryDetected(t *testing.T) {
	c := &clock{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	j, _ := openTest(t, keys.NewMemory(), c, nil, 100)
	for i := 1; i <= 3; i++ {
		j.Append(sample(i, "deny", "external"))
	}
	j.Append(sample(4, "allow", "external"))
	c.t = c.t.Add(400 * day)
	if rep, err := j.Purge(c.t, testRetention); err != nil || rep.Redacted != 3 {
		t.Fatal(rep, err)
	}
	if err := j.VerifyChain(); err != nil {
		t.Fatal("legitimate redaction:", err)
	}
	verifyExport := func() VerifyReport {
		var buf bytes.Buffer
		if err := j.Export(&buf, ""); err != nil {
			t.Fatal(err)
		}
		vr, err := VerifyArchive(&buf, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return vr
	}
	if vr := verifyExport(); !vr.OK() {
		t.Fatal(vr.Errors)
	}

	// Скрыть неудобную квитанцию: redacted=1 без записи о редактировании.
	j.db.Exec(`UPDATE receipts SET redacted=1, body=NULL WHERE seq=4`)
	if err := j.VerifyChain(); err == nil || !strings.Contains(err.Error(), "seq 4 is redacted without") {
		t.Fatalf("hiding a receipt not detected: %v", err)
	}
	if vr := verifyExport(); vr.OK() {
		t.Fatal("export verify must fail")
	}

	// Подменить метаданные законно отредактированной квитанции (другая база).
	j2, _ := openTest(t, keys.NewMemory(), c, nil, 100)
	j2.Append(sample(1, "deny", "external"))
	j2.Append(sample(2, "allow", "external"))
	c.t = c.t.Add(400 * day)
	j2.Purge(c.t, testRetention)
	j2.db.Exec(`UPDATE receipts SET agent='someone-else', action='files.read' WHERE seq=1`)
	if err := j2.VerifyChain(); err == nil || !strings.Contains(err.Error(), "metadata changed") {
		t.Fatalf("metadata forgery not detected: %v", err)
	}
}

// Отклонение и таймаут человека — не отказ политики: через год не
// редактируются.
func TestHumanRejectionNotRedactedAsDeny(t *testing.T) {
	c := &clock{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	j, _ := openTest(t, keys.NewMemory(), c, nil, 100)
	r := sample(1, "deny", "external")
	r.Approval = &Approval{Channel: "telegram", Who: "owner", At: c.t, Result: "rejected", PreviewHash: "cafe"}
	j.Append(r)
	j.Append(sample(2, "deny", "external"))
	c.t = c.t.Add(400 * day)
	rep, _ := j.Purge(c.t, testRetention)
	es, _ := j.List(Filter{})
	if rep.Redacted != 1 || es[0].Redacted || !es[1].Redacted {
		t.Fatalf("%+v %v %v", rep, es[0].Redacted, es[1].Redacted)
	}
}

// Чекпоинты связаны цепочкой: удаление чекпоинта из середины видно;
// свидетель, который знает последний чекпоинт, видит обрезку хвоста.
func TestCheckpointChainAndTruncation(t *testing.T) {
	c := &clock{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	w := &readerWitness{}
	j, _ := openTest(t, keys.NewMemory(), c, w, 2)
	for i := 1; i <= 6; i++ {
		j.Append(sample(i, "allow", "external"))
	}
	cps, _ := j.Checkpoints()
	if len(cps) != 3 || cps[0].Prev != "" || cps[1].Prev == "" || cps[2].Prev != HashBytes(cps[1].SignedBytes()) {
		t.Fatalf("%+v", cps)
	}
	if err := j.VerifyChain(); err != nil {
		t.Fatal(err)
	}
	j.db.Exec(`DELETE FROM checkpoints WHERE seq=4`)
	if err := j.VerifyChain(); err == nil || !strings.Contains(err.Error(), "checkpoint removed") {
		t.Fatalf("removed checkpoint not detected: %v", err)
	}
	// Обрезка хвоста вместе с его чекпоинтом: локально не видна, свидетель видит.
	j.db.Exec(`DELETE FROM checkpoints WHERE seq=6`)
	j.db.Exec(`DELETE FROM receipts WHERE seq>4`)
	if err := j.VerifyChain(); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncation not detected via witness: %v", err)
	}
}

type readerWitness struct{ last *Checkpoint }

func (w *readerWitness) Witness(cp Checkpoint) error { w.last = &cp; return nil }
func (w *readerWitness) LastWitnessed() (Checkpoint, bool, error) {
	if w.last == nil {
		return Checkpoint{}, false, nil
	}
	return *w.last, true, nil
}

// Свидетель вызывается вне мьютекса журнала: медленный relay не
// останавливает запись квитанций, а вызов журнала из свидетеля не виснет.
func TestWitnessOutsideJournalLock(t *testing.T) {
	c := &clock{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	var j *Journal
	w := witnessFunc(func(cp Checkpoint) error {
		j.SetWitness(nil) // берёт j.mu: под мьютексом — взаимная блокировка
		return nil
	})
	j, err := Open(filepath.Join(dir, "j.db"), filepath.Join(dir, "ev"), keys.NewMemory(), Options{CheckpointEach: 1, Witness: w, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	done := make(chan struct{})
	go func() { j.Append(sample(1, "allow", "read")); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("witness called under the journal lock")
	}
}

type witnessFunc func(Checkpoint) error

func (f witnessFunc) Witness(cp Checkpoint) error { return f(cp) }

// Квитанции-намерения: в выборке по умолчанию их нет, в проверке цепочки —
// есть; намерение без итога — OpenIntents.
func TestIntentReceipts(t *testing.T) {
	c := &clock{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	j, _ := openTest(t, keys.NewMemory(), c, nil, 100)
	in := sample(1, "allow", "external")
	in.Execution = Execution{Status: ExecStarted}
	j.Append(in)
	fin := sample(1, "allow", "external")
	fin.Intent = in.ID
	j.Append(fin)
	orphan := sample(2, "allow", "external")
	orphan.Execution = Execution{Status: ExecStarted}
	j.Append(orphan)
	if es, _ := j.List(Filter{}); len(es) != 1 || es[0].Receipt.Intent != in.ID {
		t.Fatalf("default list must hide intents: %d", len(es))
	}
	if es, _ := j.List(Filter{All: true}); len(es) != 3 {
		t.Fatal(len(es))
	}
	open, err := j.OpenIntents()
	if err != nil || len(open) != 1 || open[0].Receipt.ID != orphan.ID {
		t.Fatal(open, err)
	}
	if err := j.VerifyChain(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := j.Export(&buf, ""); err != nil {
		t.Fatal(err)
	}
	if vr, err := VerifyArchive(&buf, "", ""); err != nil || !vr.OK() || vr.Receipts != 3 {
		t.Fatal(vr, err)
	}
}
