// SPDX-License-Identifier: AGPL-3.0-only

package state

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gate4ai/gate4ai/internal/platform/sqlschema"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// База прежней версии (без user_version, approvals без nonce/result/channel,
// tokens без kind/expires) открывается, колонки добавляются, версия выставлена.
func TestOpenMigratesLegacyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE approvals(
  id TEXT PRIMARY KEY, created INTEGER NOT NULL, expires INTEGER NOT NULL,
  agent TEXT NOT NULL, action TEXT NOT NULL, class TEXT NOT NULL,
  status TEXT NOT NULL, preview TEXT NOT NULL, preview_hash TEXT NOT NULL,
  decision TEXT NOT NULL, args BLOB NOT NULL);
CREATE TABLE tokens(hash TEXT PRIMARY KEY, agent TEXT NOT NULL, created INTEGER NOT NULL);
INSERT INTO tokens(hash, agent, created) VALUES('h1', 'bot', 1);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path, keys.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := sqlschema.Version(s.db)
	if err != nil || v != SchemaVersion {
		t.Fatal(v, err)
	}
	var kind string
	var nonce int
	if err := s.db.QueryRow(`SELECT kind FROM tokens WHERE hash='h1'`).Scan(&kind); err != nil || kind != "agent" {
		t.Fatal(kind, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(nonce) FROM approvals`).Scan(&nonce); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRefusesNewerState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, _ := sql.Open("sqlite", path)
	if _, err := db.Exec(`PRAGMA user_version=999`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path, keys.NewMemory()); !errors.Is(err, sqlschema.ErrTooNew) {
		t.Fatal(err)
	}
}

// Выпущенные миграции не меняются: только новые в конец списка.
func TestMigrationsAppendOnly(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("schema version %d: add a line here for every released migration", SchemaVersion)
	}
}
