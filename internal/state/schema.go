// SPDX-License-Identifier: AGPL-3.0-only

package state

import (
	"database/sql"

	"github.com/gate4ai/gate4ai/internal/platform/sqlschema"
)

// Схема State Store версионируется PRAGMA user_version (sqlschema). Миграции
// только добавляют; выпущенную миграцию не правят — изменение схемы всегда
// новая миграция в конце списка (и строка в TestMigrationsAppendOnly).
var migrations = []sqlschema.Migration{
	// 1: схема до версионирования. Базы прежних версий создавались
	// CREATE TABLE IF NOT EXISTS, поэтому колонки с DEFAULT, появившиеся
	// позже, в них могут отсутствовать — добавляются здесь.
	func(tx *sql.Tx) error {
		if _, err := tx.Exec(schema + uiSchema + pairSchema); err != nil {
			return err
		}
		for _, c := range []struct{ table, col, def string }{
			{"actions", "amount", "REAL NOT NULL DEFAULT 0"},
			{"approvals", "provenance", "TEXT NOT NULL DEFAULT '{}'"},
			{"approvals", "channel", "TEXT NOT NULL DEFAULT ''"},
			{"approvals", "who", "TEXT NOT NULL DEFAULT ''"},
			{"approvals", "decided_at", "INTEGER NOT NULL DEFAULT 0"},
			{"approvals", "result", "TEXT NOT NULL DEFAULT ''"},
			{"approvals", "nonce", "TEXT NOT NULL DEFAULT ''"},
			{"tokens", "label", "TEXT NOT NULL DEFAULT ''"},
			{"tokens", "revoked", "INTEGER NOT NULL DEFAULT 0"},
			{"tokens", "kind", "TEXT NOT NULL DEFAULT 'agent'"},
			{"tokens", "expires", "INTEGER NOT NULL DEFAULT 0"},
			{"agent_seen", "via", "TEXT NOT NULL DEFAULT ''"},
			{"agent_seen", "calls", "INTEGER NOT NULL DEFAULT 0"},
			{"channel_stats", "last_err_ts", "INTEGER NOT NULL DEFAULT 0"},
			{"channel_stats", "last_err", "TEXT NOT NULL DEFAULT ''"},
			{"push_subs", "label", "TEXT NOT NULL DEFAULT ''"},
			{"pairings", "kind", "TEXT NOT NULL DEFAULT ''"},
			{"pairings", "dir", "TEXT NOT NULL DEFAULT ''"},
			{"pairings", "caller", "TEXT NOT NULL DEFAULT ''"},
			{"pairings", "via", "TEXT NOT NULL DEFAULT ''"},
			{"pairings", "decided_at", "INTEGER NOT NULL DEFAULT 0"},
			{"pairings", "channel", "TEXT NOT NULL DEFAULT ''"},
		} {
			if err := sqlschema.AddColumn(tx, c.table, c.col, c.def); err != nil {
				return err
			}
		}
		return nil
	},
}

// SchemaVersion — версия схемы State Store, которую понимает эта программа.
var SchemaVersion = len(migrations)

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := sqlschema.Apply(tx, migrations); err != nil {
		return err
	}
	return tx.Commit()
}
