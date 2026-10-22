// SPDX-License-Identifier: AGPL-3.0-only

package state

import (
	"encoding/json"
	"errors"
	"time"
)

// Данные для Web UI: последнее обращение агентов, статистика каналов подтверждений, подписки браузерного push.

const uiSchema = `
CREATE TABLE IF NOT EXISTS agent_seen(
  agent TEXT PRIMARY KEY, ts INTEGER NOT NULL, via TEXT NOT NULL DEFAULT '', calls INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS token_seen(hash TEXT PRIMARY KEY, ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS channel_stats(
  channel TEXT PRIMARY KEY, sent INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0,
  last_ok INTEGER NOT NULL DEFAULT 0, last_err_ts INTEGER NOT NULL DEFAULT 0, last_err TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS push_subs(
  endpoint TEXT PRIMARY KEY, sub BLOB NOT NULL, label TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL);`

// ---- Последнее обращение агента ----

// seenEvery — не чаще одной записи на агента за этот интервал (счётчик — в памяти не копим).
const seenEvery = 10 * time.Second

// Seen отмечает обращение агента (via — канал: socket, agent-socket, https, mcp-http)
// и токена (по хэшу).
func (s *Store) Seen(agent, via, token string) {
	now := s.Now()
	s.mu.Lock()
	if s.seen == nil {
		s.seen = map[string]time.Time{}
	}
	key := agent + "\x00" + via
	last := s.seen[key]
	if now.Sub(last) < seenEvery {
		s.mu.Unlock()
		return
	}
	s.seen[key] = now
	s.mu.Unlock()
	_, _ = s.db.Exec(`INSERT INTO agent_seen(agent,ts,via,calls) VALUES(?,?,?,1)
		ON CONFLICT(agent) DO UPDATE SET ts=excluded.ts, via=excluded.via, calls=calls+1`, agent, now.UnixNano(), via)
	if token != "" {
		_, _ = s.db.Exec(`INSERT INTO token_seen(hash,ts) VALUES(?,?) ON CONFLICT(hash) DO UPDATE SET ts=excluded.ts`, HashToken(token), now.UnixNano())
	}
}

// AgentSeen — последнее обращение агента.
type AgentSeen struct {
	TS    time.Time `json:"ts"`
	Via   string    `json:"via"`
	Calls int       `json:"calls"` // отмеченных обращений (не чаще раза в 10 с)
}

// LastSeen — последнее обращение по агентам.
func (s *Store) LastSeen() (map[string]AgentSeen, error) {
	rows, err := s.db.Query(`SELECT agent, ts, via, calls FROM agent_seen`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AgentSeen{}
	for rows.Next() {
		var a string
		var ts int64
		var v AgentSeen
		if err := rows.Scan(&a, &ts, &v.Via, &v.Calls); err != nil {
			return nil, err
		}
		v.TS = time.Unix(0, ts)
		out[a] = v
	}
	return out, rows.Err()
}

// ---- Статистика каналов подтверждений ----

// ChannelStat — сколько уведомлений канал отправил и с каким результатом.
type ChannelStat struct {
	Sent      int       `json:"sent"`
	Failed    int       `json:"failed"`
	LastOK    time.Time `json:"last_ok,omitempty"`
	LastErrAt time.Time `json:"last_err_at,omitempty"`
	LastErr   string    `json:"last_err,omitempty"`
}

// ChannelResult учитывает отправку уведомления каналом (err == nil — доставлено).
func (s *Store) ChannelResult(channel string, err error) {
	now := s.Now().UnixNano()
	if err == nil {
		_, _ = s.db.Exec(`INSERT INTO channel_stats(channel,sent,last_ok) VALUES(?,1,?)
			ON CONFLICT(channel) DO UPDATE SET sent=sent+1, last_ok=excluded.last_ok`, channel, now)
		return
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	_, _ = s.db.Exec(`INSERT INTO channel_stats(channel,failed,last_err_ts,last_err) VALUES(?,1,?,?)
		ON CONFLICT(channel) DO UPDATE SET failed=failed+1, last_err_ts=excluded.last_err_ts, last_err=excluded.last_err`, channel, now, msg)
}

// ChannelStats — статистика по каналам.
func (s *Store) ChannelStats() (map[string]ChannelStat, error) {
	rows, err := s.db.Query(`SELECT channel,sent,failed,last_ok,last_err_ts,last_err FROM channel_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ChannelStat{}
	for rows.Next() {
		var ch string
		var st ChannelStat
		var ok, et int64
		if err := rows.Scan(&ch, &st.Sent, &st.Failed, &ok, &et, &st.LastErr); err != nil {
			return nil, err
		}
		if ok != 0 {
			st.LastOK = time.Unix(0, ok)
		}
		if et != 0 {
			st.LastErrAt = time.Unix(0, et)
		}
		out[ch] = st
	}
	return out, rows.Err()
}

// DecidedByChannel — сколько запросов решено через каждый канал.
func (s *Store) DecidedByChannel() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT channel, COUNT(*) FROM approvals WHERE channel<>'' GROUP BY channel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ch string
		var n int
		if err := rows.Scan(&ch, &n); err != nil {
			return nil, err
		}
		out[ch] = n
	}
	return out, rows.Err()
}

// ---- Подписки браузерного push ----

// PushSub — подписка Web Push (PushSubscription.toJSON()).
type PushSub struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Label   string    `json:"label,omitempty"`
	Created time.Time `json:"created"`
}

// AddPushSub сохраняет подписку (зашифрованно: ключи подписки — секрет браузера).
func (s *Store) AddPushSub(p PushSub) error {
	if p.Endpoint == "" || p.Keys.P256dh == "" || p.Keys.Auth == "" {
		return errors.New("subscription needs endpoint and keys")
	}
	p.Created = s.Now()
	b, _ := json.Marshal(p)
	_, err := s.db.Exec(`INSERT INTO push_subs(endpoint,sub,label,created) VALUES(?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET sub=excluded.sub, label=excluded.label`, p.Endpoint, s.aead.Seal("push", b), p.Label, p.Created.UnixNano())
	return err
}

// RemovePushSub удаляет подписку.
func (s *Store) RemovePushSub(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subs WHERE endpoint=?`, endpoint)
	return err
}

// PushSubs — все подписки.
func (s *Store) PushSubs() ([]PushSub, error) {
	rows, err := s.db.Query(`SELECT sub FROM push_subs ORDER BY created`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		var box []byte
		if err := rows.Scan(&box); err != nil {
			return nil, err
		}
		b, err := s.aead.Open("push", box)
		if err != nil {
			continue
		}
		var p PushSub
		if json.Unmarshal(b, &p) == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}
