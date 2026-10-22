// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// Золотые журналы testdata/journal-v* записаны настоящим кодом выпущенных
// версий (журнал, доказательства, ключи, архив экспорта). Их не пересоздают:
// тест падает, если формат хэша, подписи чекпоинтов или схема изменились не
// миграцией. Новый формат журнала — новый золотой журнал рядом.
var goldenJournals = []string{"journal-v0.7.2", "journal-v0.7.5"}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func goldenKeys(t *testing.T, dir string) keys.Store {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ks := keys.NewMemory()
	for k, v := range m {
		ks.Set(k, v)
	}
	return ks
}

// dumpRows — все колонки таблицы, кроме перечисленных, по строкам (seq → значения).
func dumpRows(t *testing.T, dbPath, table string, cols string) map[int64]string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT seq, quote(` + strings.ReplaceAll(cols, ",", ")||'|'||quote(") + `) FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var seq int64
		var v string
		if err := rows.Scan(&seq, &v); err != nil {
			t.Fatal(err)
		}
		out[seq] = v
	}
	return out
}

const (
	goldenReceiptCols    = "seq,id,ts,agent,action,class,effect,hash,prev_hash,preview_hash,evidence,body,redacted"
	goldenCheckpointCols = "seq,head,ts,sig,witnessed"
)

// Журнал прошлой версии открывается без ручных действий: схема мигрирует
// (с резервной копией), записи и чекпоинты побитно те же, цепочка
// проверяется, новые записи дописываются, экспорт проверяется.
func TestGoldenJournals(t *testing.T) {
	for _, name := range goldenJournals {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join("testdata", name)
			dir := t.TempDir()
			copyDir(t, src, dir)
			dbPath := filepath.Join(dir, "journal.db")
			ks := goldenKeys(t, dir)
			before := dumpRows(t, dbPath, "receipts", goldenReceiptCols)
			beforeCP := dumpRows(t, dbPath, "checkpoints", goldenCheckpointCols)
			if len(before) == 0 || len(beforeCP) == 0 {
				t.Fatal("golden journal is empty")
			}

			c := &clock{time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)}
			open := func() *Journal {
				j, err := Open(dbPath, filepath.Join(dir, "evidence"), ks, Options{CheckpointEach: 4, Now: c.now})
				if err != nil {
					t.Fatal(err)
				}
				return j
			}
			j := open()
			if err := j.VerifyChain(); err != nil {
				t.Fatal("after migration:", err)
			}
			if v, _ := schemaVersion(j.db); v != SchemaVersion {
				t.Fatalf("user_version %d, want %d", v, SchemaVersion)
			}
			baks, _ := filepath.Glob(dbPath + ".v0-*.bak")
			if len(baks) != 1 {
				t.Fatalf("backup before migration: %v", baks)
			}
			if got := dumpRows(t, baks[0], "receipts", goldenReceiptCols); fmt.Sprint(got) != fmt.Sprint(before) {
				t.Fatal("backup differs from the original journal")
			}

			es, err := j.List(Filter{All: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range es {
				if !e.Redacted && (e.Receipt.Agent == "" || e.Format != 1) {
					t.Fatalf("seq %d read wrong: %+v", e.Receipt.Seq, e)
				}
			}
			r := sample(99, "allow", "external")
			r.Parent = "r_x"
			if _, err := j.Append(r); err != nil {
				t.Fatal(err)
			}
			if _, err := j.Checkpoint(); err != nil {
				t.Fatal(err)
			}
			if err := j.VerifyChain(); err != nil {
				t.Fatal("after append:", err)
			}
			var buf bytes.Buffer
			if err := j.Export(&buf, ""); err != nil {
				t.Fatal(err)
			}
			if rep, err := VerifyArchive(&buf, "", ""); err != nil || !rep.OK() {
				t.Fatal("export of migrated journal:", rep.Errors, err)
			}
			j.Close()

			// Повторное открытие: миграции нет, новой копии нет.
			j = open()
			if err := j.VerifyChain(); err != nil {
				t.Fatal("reopen:", err)
			}
			j.Close()
			if baks, _ := filepath.Glob(dbPath + ".v*.bak"); len(baks) != 1 {
				t.Fatalf("second backup on reopen: %v", baks)
			}

			// Прежние записи и чекпоинты побитно те же (новые — только дописаны).
			after := dumpRows(t, dbPath, "receipts", goldenReceiptCols)
			for seq, v := range before {
				if after[seq] != v {
					t.Fatalf("receipt seq %d changed:\n%s\n%s", seq, v, after[seq])
				}
			}
			afterCP := dumpRows(t, dbPath, "checkpoints", goldenCheckpointCols)
			for seq, v := range beforeCP {
				if afterCP[seq] != v {
					t.Fatalf("checkpoint seq %d changed:\n%s\n%s", seq, v, afterCP[seq])
				}
			}

			// Архив экспорта, записанный той версией, проверяется новой.
			f, err := os.Open(filepath.Join(src, "export.tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if rep, err := VerifyArchive(f, "", ""); err != nil || !rep.OK() {
				t.Fatal("old export:", rep.Errors, err)
			}
		})
	}
}

// Подделку старого журнала видно и после миграции: тело квитанции,
// удалённый чекпоинт.
func TestGoldenJournalTamperDetected(t *testing.T) {
	for _, name := range goldenJournals {
		t.Run(name, func(t *testing.T) {
			for _, tamper := range []string{
				`UPDATE receipts SET hash='00'||substr(hash,3) WHERE seq=1`,
				`UPDATE receipts SET body=(SELECT body FROM receipts WHERE seq=2) WHERE seq=1`,
				`DELETE FROM checkpoints WHERE seq=(SELECT MIN(seq) FROM checkpoints WHERE prev!='')`,
			} {
				dir := t.TempDir()
				copyDir(t, filepath.Join("testdata", name), dir)
				dbPath := filepath.Join(dir, "journal.db")
				j, err := Open(dbPath, filepath.Join(dir, "evidence"), goldenKeys(t, dir), Options{})
				if err != nil {
					t.Fatal(err)
				}
				res, err := j.db.Exec(tamper)
				if err != nil {
					t.Fatal(err)
				}
				if n, _ := res.RowsAffected(); n == 0 {
					continue // в журнале нет связанных чекпоинтов
				}
				if err := j.VerifyChain(); err == nil {
					t.Fatalf("tamper not detected: %s", tamper)
				}
				j.Close()
			}
		})
	}
}

// Журнал более новой версии не открывается и не меняется.
func TestSchemaTooNew(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "journal.db")
	j, err := Open(dbPath, filepath.Join(dir, "evidence"), keys.NewMemory(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	j.Close()
	_, err = Open(dbPath, filepath.Join(dir, "evidence"), keys.NewMemory(), Options{})
	if !errors.Is(err, ErrSchemaTooNew) || !strings.Contains(err.Error(), "update gate4ai") {
		t.Fatalf("newer journal: %v", err)
	}
	db, _ := sql.Open("sqlite", dbPath)
	defer db.Close()
	if v, _ := schemaVersion(db); v != SchemaVersion+1 {
		t.Fatalf("newer journal changed: user_version %d", v)
	}
}

// Выпущенные миграции не правят: схема на каждой версии зафиксирована.
// Изменение схемы — новая миграция в конце списка и новая строка здесь.
func TestMigrationsAppendOnly(t *testing.T) {
	want := []string{
		1: `checkpoints(seq INTEGER PRIMARY KEY,head TEXT NOT NULL,ts INTEGER NOT NULL,sig TEXT NOT NULL,witnessed INTEGER NOT NULL DEFAULT 0,prev TEXT NOT NULL DEFAULT '');` +
			`receipts(seq INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE,ts INTEGER NOT NULL,agent TEXT NOT NULL,action TEXT NOT NULL,class TEXT NOT NULL,effect TEXT NOT NULL,hash TEXT NOT NULL UNIQUE,prev_hash TEXT NOT NULL,preview_hash TEXT NOT NULL DEFAULT '',evidence TEXT NOT NULL DEFAULT '[]',body BLOB,redacted INTEGER NOT NULL DEFAULT 0,kind TEXT NOT NULL DEFAULT '',intent TEXT NOT NULL DEFAULT '');` +
			`index receipts_action(action,ts);index receipts_agent(agent,ts);index receipts_kind(kind);index receipts_ts(ts)`,
		2: `checkpoints(seq INTEGER PRIMARY KEY,head TEXT NOT NULL,ts INTEGER NOT NULL,sig TEXT NOT NULL,witnessed INTEGER NOT NULL DEFAULT 0,prev TEXT NOT NULL DEFAULT '');` +
			`receipts(seq INTEGER PRIMARY KEY,id TEXT NOT NULL UNIQUE,ts INTEGER NOT NULL,agent TEXT NOT NULL,action TEXT NOT NULL,class TEXT NOT NULL,effect TEXT NOT NULL,hash TEXT NOT NULL UNIQUE,prev_hash TEXT NOT NULL,preview_hash TEXT NOT NULL DEFAULT '',evidence TEXT NOT NULL DEFAULT '[]',body BLOB,redacted INTEGER NOT NULL DEFAULT 0,kind TEXT NOT NULL DEFAULT '',intent TEXT NOT NULL DEFAULT '',format INTEGER NOT NULL DEFAULT 1);` +
			`index receipts_action(action,ts);index receipts_agent(agent,ts);index receipts_kind(kind);index receipts_ts(ts)`,
	}
	if len(want)-1 != SchemaVersion {
		t.Fatalf("schema version %d has no fixed schema here: add it to want", SchemaVersion)
	}
	for v := 1; v <= SchemaVersion; v++ {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "j.db"))
		if err != nil {
			t.Fatal(err)
		}
		tx, _ := db.Begin()
		for i := range v {
			if err := migrations[i](tx); err != nil {
				t.Fatal(err)
			}
		}
		tx.Commit()
		if got := schemaDump(t, db); got != want[v] {
			t.Errorf("schema at version %d changed (released migrations are append-only):\n got %s\nwant %s", v, got, want[v])
		}
		db.Close()
	}
}

// schemaDump — таблицы (колонки с типом и ограничениями) и индексы по порядку имён.
func schemaDump(t *testing.T, db *sql.DB) string {
	t.Helper()
	var parts []string
	q := func(query string, args ...any) []string {
		rows, err := db.Query(query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	for _, tbl := range q(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`) {
		var cols []string
		rows, err := db.Query(`SELECT c.name, c.type, c."notnull", c.dflt_value, c.pk,
			EXISTS(SELECT 1 FROM pragma_index_list(?) l JOIN pragma_index_info(l.name) i WHERE l."unique" AND l.origin='u' AND i.name=c.name)
			FROM pragma_table_info(?) c ORDER BY c.cid`, tbl, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, typ string
			var notnull, pk, unique int
			var def sql.NullString
			rows.Scan(&name, &typ, &notnull, &def, &pk, &unique)
			s := name + " " + typ
			if pk == 1 {
				s += " PRIMARY KEY"
			}
			if notnull == 1 {
				s += " NOT NULL"
			}
			if unique == 1 {
				s += " UNIQUE"
			}
			if def.Valid {
				s += " DEFAULT " + def.String
			}
			cols = append(cols, s)
		}
		rows.Close()
		parts = append(parts, tbl+"("+strings.Join(cols, ",")+")")
	}
	var idx []string
	for _, name := range q(`SELECT name FROM sqlite_master WHERE type='index' AND sql IS NOT NULL ORDER BY name`) {
		cols := q(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, name)
		idx = append(idx, "index "+name+"("+strings.Join(cols, ",")+")")
	}
	return strings.Join(parts, ";") + ";" + strings.Join(idx, ";")
}

// Формат квитанции 1 и подписываемые байты чекпоинта зафиксированы: тот же
// документ даёт тот же хэш в любой версии.
func TestReceiptFormat1Fixed(t *testing.T) {
	r := &Receipt{Seq: 3, ID: "r_fixed", TS: time.Date(2026, 3, 1, 9, 0, 0, 123456000, time.UTC), Agent: "claude-ai",
		Action: "email.send", Class: "external", Args: map[string]any{"to": "a@example.com", "n": 1e21, "body": "привет <b>&</b>"},
		Decision:     Decision{Effect: "allow", RuleIDs: []string{"r1"}, Reason: "because"},
		Execution:    Execution{Status: "ok"},
		Verification: Verification{Status: "verified"}, PrevHash: "ab"}
	canon, err := r.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	const wantDoc = `{"action":"email.send","agent":"claude-ai","args":{"body":"привет <b>&</b>","n":1e+21,"to":"a@example.com"},"class":"external","decision":{"effect":"allow","reason":"because","rule_ids":["r1"]},"execution":{"status":"ok"},"id":"r_fixed","prev_hash":"ab","seq":3,"ts":"2026-03-01T09:00:00.123456Z","verification":{"evidence_refs":[],"status":"verified"}}`
	if string(canon) != wantDoc {
		t.Fatalf("canonical receipt changed:\n got %s\nwant %s", canon, wantDoc)
	}
	h, err := formatHash(1, canon)
	if err != nil || h != HashBytes([]byte(wantDoc)) {
		t.Fatal(h, err)
	}
	// Тот же документ в другой записи JSON (экранирование, порядок ключей) — тот же хэш.
	esc, _ := json.Marshal(json.RawMessage(canon))
	if h2, _ := formatHash(1, esc); h2 != h {
		t.Fatal("format 1 hash depends on JSON spelling")
	}
	if _, err := formatHash(ReceiptFormat+1, canon); err == nil {
		t.Fatal("unknown receipt format accepted")
	}
	cp := Checkpoint{Seq: 4, Head: "cafe", TS: time.Date(2026, 3, 1, 9, 0, 0, 123456000, time.UTC), Prev: "beef"}
	const wantCP = `{"head":"cafe","prev":"beef","seq":4,"ts":"2026-03-01T09:00:00.123456Z","type":"gate4ai-checkpoint"}`
	if string(cp.SignedBytes()) != wantCP {
		t.Fatalf("checkpoint signed bytes changed:\n got %s\nwant %s", cp.SignedBytes(), wantCP)
	}
}
