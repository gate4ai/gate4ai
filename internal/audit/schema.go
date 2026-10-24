// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/sqlschema"
)

// Схема журнала версионируется PRAGMA user_version; миграции упорядочены и
// только добавляют (таблицы, колонки с DEFAULT, индексы): существующие
// квитанции, их хэши, prev_hash и подписи чекпоинтов никогда не меняются.
// Правила — docs/architecture.md («Журнал: что можно менять»). Выпущенную
// миграцию не правят: изменение схемы — новая миграция в конце списка.
var migrations = []func(tx *sql.Tx) error{
	// 1: схема до версионирования. Журналы v0.7.2 и раньше — без receipts.kind,
	// receipts.intent и checkpoints.prev; v0.7.3–v0.7.5 — уже с ними.
	func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
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
);`); err != nil {
			return err
		}
		for _, c := range []struct{ table, col, def string }{
			{"receipts", "kind", "TEXT NOT NULL DEFAULT ''"},
			{"receipts", "intent", "TEXT NOT NULL DEFAULT ''"},
			{"checkpoints", "prev", "TEXT NOT NULL DEFAULT ''"},
		} {
			if err := addColumn(tx, c.table, c.col, c.def); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS receipts_kind ON receipts(kind)`)
		return err
	},
	// 2: формат квитанции (правила хэша), у всех прежних записей — 1.
	func(tx *sql.Tx) error {
		return addColumn(tx, "receipts", "format", "INTEGER NOT NULL DEFAULT 1")
	},
}

// SchemaVersion — версия схемы журнала, которую понимает эта программа.
var SchemaVersion = len(migrations)

// ErrSchemaTooNew — журнал записан более новой версией gate4ai.
var ErrSchemaTooNew = sqlschema.ErrTooNew

func addColumn(tx *sql.Tx, table, col, def string) error {
	return sqlschema.AddColumn(tx, table, col, def)
}

func schemaVersion(q sqlschema.Querier) (int, error) { return sqlschema.Version(q) }

// migrate приводит схему к SchemaVersion и возвращает версию, с которой
// начинали. Непустой журнал перед миграцией копируется рядом
// (<db>.v<N>-<время>.bak); все шаги — одна транзакция.
func migrate(db *sql.DB, dbPath string) (int, error) {
	from, err := schemaVersion(db)
	if err != nil {
		return 0, err
	}
	if from > SchemaVersion {
		return from, fmt.Errorf("%w: schema version %d, this gate4ai supports up to %d; update gate4ai", ErrSchemaTooNew, from, SchemaVersion)
	}
	if from == SchemaVersion {
		return from, nil
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='receipts'`).Scan(&tables); err != nil {
		return from, err
	}
	if tables > 0 {
		bak := fmt.Sprintf("%s.v%d-%s.bak", dbPath, from, time.Now().UTC().Format("20060102T150405"))
		if _, err := os.Stat(bak); err == nil {
			return from, fmt.Errorf("backup %s already exists", bak)
		}
		if _, err := db.Exec(`VACUUM INTO ?`, bak); err != nil {
			return from, fmt.Errorf("backup before migration: %w", err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return from, err
	}
	defer tx.Rollback()
	// Другой процесс мог мигрировать журнал, пока делалась копия.
	cur, err := schemaVersion(tx)
	if err != nil {
		return from, err
	}
	if cur > SchemaVersion {
		return from, fmt.Errorf("%w: schema version %d, this gate4ai supports up to %d; update gate4ai", ErrSchemaTooNew, cur, SchemaVersion)
	}
	for v := cur; v < SchemaVersion; v++ {
		if err := migrations[v](tx); err != nil {
			return from, fmt.Errorf("migration %d: %w", v+1, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, SchemaVersion)); err != nil {
		return from, err
	}
	return from, tx.Commit()
}
