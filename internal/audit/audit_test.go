// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type fakeWitness struct {
	got  []Checkpoint
	fail bool
}

func (w *fakeWitness) Witness(cp Checkpoint) error {
	if w.fail {
		return sql.ErrConnDone
	}
	w.got = append(w.got, cp)
	return nil
}

func openTest(t *testing.T, ks keys.Store, c *clock, w Witness, every int) (*Journal, string) {
	t.Helper()
	dir := t.TempDir()
	j, err := Open(filepath.Join(dir, "journal.db"), filepath.Join(dir, "evidence"), ks, Options{CheckpointEach: every, Witness: w, Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j, dir
}

func sample(i int, effect, class string) *Receipt {
	return &Receipt{
		Agent: "claude-ai", Action: "email.send", Class: class,
		Args:         map[string]any{"to": []any{"landlord@example.com"}, "n": float64(i), "body": "привет"},
		Decision:     Decision{Effect: effect, RuleIDs: []string{"r1"}, Reason: "because"},
		Execution:    Execution{Status: "ok", ResultRef: "msgid-1"},
		Verification: Verification{Method: "sent_folder", Status: "verified"},
	}
}

func TestCanonicalRFC8785(t *testing.T) {
	b, err := Canonicalize(map[string]any{"b": 1.0, "a": "é", "c": []any{1e21, 0.5}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":"é","b":1,"c":[1e+21,0.5]}` {
		t.Fatalf("got %s", b)
	}
	r := sample(1, "allow", "external")
	r.TS = time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	c, _ := r.Canonical()
	for _, f := range []string{`"id"`, `"ts"`, `"agent"`, `"action"`, `"args"`, `"decision":{"effect"`, `"rule_ids"`, `"reason"`, `"execution":{"result_ref"`, `"verification":{"evidence_refs":[]`, `"prev_hash"`} {
		if !strings.Contains(string(c), f) {
			t.Errorf("canonical receipt lacks %s: %s", f, c)
		}
	}
	if Short("a3f9c21e0011") != "#a3f9c21e" {
		t.Fatal("short")
	}
}

func TestChainEncryptionCheckpoints(t *testing.T) {
	ks := keys.NewMemory()
	c := &clock{time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	w := &fakeWitness{}
	j, dir := openTest(t, ks, c, w, 3)
	var hashes []string
	for i := 1; i <= 7; i++ {
		c.t = c.t.Add(time.Minute)
		r := sample(i, "allow", "external")
		if i == 2 {
			r.Approval = &Approval{Channel: "telegram", Who: "owner", At: c.t, Result: "approved", PreviewHash: "deadbeefcafe0000"}
		}
		h, err := j.Append(r)
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	if err := j.VerifyChain(); err != nil {
		t.Fatal(err)
	}
	es, _ := j.List(Filter{})
	if len(es) != 7 || es[1].Receipt.PrevHash != hashes[0] || es[0].Receipt.PrevHash != "" {
		t.Fatal("chain links")
	}
	cps, _ := j.Checkpoints()
	if len(cps) != 2 || cps[0].Seq != 3 || cps[1].Seq != 6 || len(w.got) != 2 || !cps[0].VerifySig(j.PublicKey()) {
		t.Fatalf("checkpoints: %+v witness %d", cps, len(w.got))
	}
	// Ручной чекпоинт для хвоста; повторный — без новых записей — не создаётся.
	if cp, err := j.Checkpoint(); err != nil || cp == nil || cp.Seq != 7 {
		t.Fatal(cp, err)
	}
	if cp, _ := j.Checkpoint(); cp != nil {
		t.Fatal("duplicate checkpoint")
	}
	// Суточный чекпоинт.
	if cp, _ := j.MaybeDailyCheckpoint(); cp != nil {
		t.Fatal("daily too early")
	}
	j.Append(sample(8, "deny", "external"))
	c.t = c.t.Add(25 * time.Hour)
	if cp, _ := j.MaybeDailyCheckpoint(); cp == nil || cp.Seq != 8 {
		t.Fatal("daily checkpoint missing")
	}

	// Поиск по хэшу, префиксу превью, id.
	e, err := j.Find(Short(hashes[3]))
	if err != nil || e.Receipt.Seq != 4 {
		t.Fatal(e, err)
	}
	if e, err := j.Find("#deadbeef"); err != nil || e.Receipt.Seq != 2 {
		t.Fatal("find by preview", err)
	}
	if e2, _ := j.Find(e.Receipt.ID); e2.Receipt.Seq != 4 {
		t.Fatal("by id")
	}
	if _, err := j.Find("zz"); err == nil {
		t.Fatal("short prefix")
	}

	// Поля зашифрованы: в файле базы нет открытого текста аргументов.
	raw := readAll(t, filepath.Join(dir, "journal.db"))
	walRaw := readAll(t, filepath.Join(dir, "journal.db-wal"))
	if bytes.Contains(raw, []byte("landlord@example.com")) || bytes.Contains(walRaw, []byte("landlord@example.com")) {
		t.Fatal("plaintext args in sqlite")
	}

	// Подмена тела ломает цепочку.
	var id string
	var body []byte
	j.db.QueryRow(`SELECT id, body FROM receipts WHERE seq=5`).Scan(&id, &body)
	plain, _ := j.open(id, body)
	tampered := bytes.Replace(plain, []byte("landlord"), []byte("attacker"), 1)
	box, _ := j.seal(id, tampered)
	j.db.Exec(`UPDATE receipts SET body=? WHERE seq=5`, box)
	if err := j.VerifyChain(); err == nil || !strings.Contains(err.Error(), "seq 5 content hash mismatch") {
		t.Fatalf("tamper not detected: %v", err)
	}
	// Без ключа AEAD тело не расшифровать.
	j.key = make([]byte, 32)
	if _, err := j.List(Filter{}); err == nil {
		t.Fatal("decrypt with wrong key must fail")
	}
}

func readAll(t *testing.T, p string) []byte {
	b, _ := filepathRead(p)
	return b
}

func TestEvidenceAge(t *testing.T) {
	ks := keys.NewMemory()
	ev, err := OpenEvidence(t.TempDir(), ks)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("Message-ID: <x@y>\r\n\r\nsecret body")
	ref, err := ev.Put(data)
	if err != nil || !strings.HasPrefix(ref, "sha256:") {
		t.Fatal(ref, err)
	}
	p, _ := ev.path(ref)
	enc, _ := filepathRead(p)
	if bytes.Contains(enc, []byte("secret body")) || !bytes.HasPrefix(enc, []byte("age-encryption.org/v1")) {
		t.Fatal("evidence not age-encrypted")
	}
	got, err := ev.Get(ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(err)
	}
	ev.Delete(ref)
	if _, err := ev.Get(ref); err != ErrShredded {
		t.Fatal(err)
	}
	if _, err := ev.Get("sha256:../../etc/passwd"); err == nil {
		t.Fatal("path traversal")
	}
}

func TestRetentionAndExportVerify(t *testing.T) {
	ks := keys.NewMemory()
	c := &clock{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	j, _ := openTest(t, ks, c, nil, 2)
	put := func(class, effect string, ev string) {
		r := sample(1, effect, class)
		if ev != "" {
			ref, err := j.Evidence.Put([]byte(ev))
			if err != nil {
				t.Fatal(err)
			}
			r.Verification.EvidenceRefs = []string{ref}
		}
		if _, err := j.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	put("money", "allow", "statement-line") // 1: money evidence
	put("external", "allow", "sent-eml")    // 2: other evidence
	put("external", "deny", "")             // 3: deny
	c.t = c.t.Add(400 * 24 * time.Hour)
	put("external", "allow", "fresh-eml") // 4
	rep, err := j.Purge(c.t, Retention{EvidenceMoney: 3 * 365 * 24 * time.Hour, EvidenceOther: 90 * 24 * time.Hour, Denied: 365 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if rep.EvidenceDeleted != 1 || rep.Redacted != 1 {
		t.Fatalf("purge: %+v", rep)
	}
	es, _ := j.List(Filter{})
	if !es[2].Redacted || es[2].Receipt.Args != nil {
		t.Fatal("deny not redacted")
	}
	if err := j.VerifyChain(); err != nil {
		t.Fatalf("chain must survive purge: %v", err)
	}
	if _, err := j.Evidence.Get(es[0].Receipt.Verification.EvidenceRefs[0]); err != nil {
		t.Fatal("money evidence must be kept 3 years")
	}

	for _, pass := range []string{"", "correct horse"} {
		var buf bytes.Buffer
		if err := j.Export(&buf, pass); err != nil {
			t.Fatal(err)
		}
		vr, err := VerifyArchive(bytes.NewReader(buf.Bytes()), pass, hex.EncodeToString(j.PublicKey()))
		if err != nil || !vr.OK() {
			t.Fatalf("verify: %+v %v", vr, err)
		}
		if vr.Receipts != 5 || vr.Redacted != 1 || vr.EvidenceOK != 2 || vr.EvidenceDeleted != 1 || vr.Checkpoints != 3 {
			t.Fatalf("report: %+v", vr)
		}
		if pass != "" {
			if _, err := VerifyArchive(bytes.NewReader(buf.Bytes()), "wrong", ""); err == nil {
				t.Fatal("wrong passphrase")
			}
		}
		vr, _ = VerifyArchive(bytes.NewReader(buf.Bytes()), pass, strings.Repeat("00", 32))
		if vr.OK() {
			t.Fatal("foreign key must fail")
		}
	}
	// Подделанный архив.
	var buf bytes.Buffer
	j.Export(&buf, "")
	bad := tamperTar(t, buf.Bytes(), "receipts.jsonl", func(b []byte) []byte { return bytes.Replace(b, []byte("landlord"), []byte("mallory!"), 1) })
	vr, _ := VerifyArchive(bytes.NewReader(bad), "", "")
	if vr.OK() {
		t.Fatal("tampered archive verified")
	}
}
