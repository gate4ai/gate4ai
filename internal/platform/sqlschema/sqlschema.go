// SPDX-License-Identifier: AGPL-3.0-only

// Package sqlschema — версионирование схемы SQLite через PRAGMA user_version:
// упорядоченный список миграций, которые только добавляют (таблицы, индексы,
// колонки с DEFAULT). Общее для журнала и State Store.
package sqlschema

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrTooNew — база записана более новой версией gate4ai.
var ErrTooNew = errors.New("database was written by a newer gate4ai")

// Migration — один шаг схемы в транзакции.
type Migration func(tx *sql.Tx) error

// Querier — *sql.DB или *sql.Tx.
type Querier interface {
	QueryRow(string, ...any) *sql.Row
}

// Version — текущая PRAGMA user_version.
func Version(q Querier) (int, error) {
	var v int
	err := q.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v, err
}

// AddColumn добавляет колонку, если её ещё нет.
func AddColumn(tx *sql.Tx, table, col, def string) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, col).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + def)
	return err
}

// Apply проводит в tx миграции начиная с текущей версии и записывает
// len(migrations) в user_version. База новее программы — ErrTooNew.
func Apply(tx *sql.Tx, migrations []Migration) error {
	cur, err := Version(tx)
	if err != nil {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("%w: schema version %d, this gate4ai supports up to %d; update gate4ai", ErrTooNew, cur, len(migrations))
	}
	for v := cur; v < len(migrations); v++ {
		if err := migrations[v](tx); err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
	}
	_, err = tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, len(migrations)))
	return err
}
