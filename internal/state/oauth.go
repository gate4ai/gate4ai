// SPDX-License-Identifier: AGPL-3.0-only

package state

import (
	"database/sql"
	"errors"
	"time"
)

// ---- OAuth: ротация refresh и клиенты динамической регистрации ----

// ErrTokenReused — refresh-токен уже использован (или отозван): признак кражи.
var ErrTokenReused = errors.New("refresh token reuse")

// ConsumeRefreshToken атомарно гасит refresh-токен OAuth (одно UPDATE с
// условием revoked=0): из параллельных запросов с одним токеном проходит один.
// Повторное использование погашенного токена — ErrTokenReused с агентом и
// меткой (client_id), чтобы отозвать все токены клиента.
func (s *Store) ConsumeRefreshToken(tok string) (agent, label string, err error) {
	h := HashToken(tok)
	res, err := s.db.Exec(`UPDATE tokens SET revoked=1 WHERE hash=? AND kind='refresh' AND revoked=0 AND (expires=0 OR expires>?)`,
		h, s.Now().UnixNano())
	if err != nil {
		return "", "", err
	}
	n, _ := res.RowsAffected()
	var kind string
	var revoked int
	qerr := s.db.QueryRow(`SELECT agent, kind, label, revoked FROM tokens WHERE hash=?`, h).Scan(&agent, &kind, &label, &revoked)
	switch {
	case errors.Is(qerr, sql.ErrNoRows) || kind != "refresh":
		return "", "", ErrBadToken
	case qerr != nil:
		return "", "", qerr
	case n == 1:
		return agent, label, nil
	case revoked != 0:
		return agent, label, ErrTokenReused
	}
	return "", "", ErrBadToken // истёк
}

// CountKV — число ключей kv с префиксом.
func (s *Store) CountKV(prefix string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM kv WHERE substr(k, 1, ?) = ?`, len(prefix), prefix).Scan(&n)
	return n, err
}

// PruneOAuthClients удаляет клиентов динамической регистрации (kv
// "oauth.client.<id>"), зарегистрированных раньше before, которым не выдано ни
// одного токена. Возвращает число удалённых.
func (s *Store) PruneOAuthClients(before time.Time) (int, error) {
	const p = "oauth.client."
	res, err := s.db.Exec(`DELETE FROM kv WHERE substr(k, 1, ?) = ?
  AND COALESCE(json_extract(v, '$.client_id_issued_at'), 0) < ?
  AND NOT EXISTS (SELECT 1 FROM tokens WHERE tokens.label = substr(kv.k, ?))`, len(p), p, before.Unix(), len(p)+1)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
