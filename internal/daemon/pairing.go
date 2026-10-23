// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/state"
)

// Спаривание агента (gate4ai pair): агент сам создаёт ключ и присылает его
// хэш, daemon отвечает кодом, агент показывает код человеку в чате, владелец
// сверяет его и подтверждает подключение — только в Web UI или Telegram (не
// через CLI и не кнопкой трея: агент того же пользователя ОС не должен
// одобрять себя сам).

// DirHeader — каталог агента, который сообщает удалённый CLI (локально daemon
// узнаёт его сам по собеседнику сокета).
const DirHeader = "X-Gate4ai-Dir"

// Pairer — канал, который сообщает владельцу о запросе на подключение агента.
type Pairer interface {
	PairRequested(p *state.Pairing)
}

var (
	agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	keyHashRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// PairRequest — запрос агента на подключение.
type PairRequest struct {
	Agent   string `json:"agent"`    // имя агента
	Kind    string `json:"kind"`     // тип агента (claude-code…), "" — неизвестен
	KeyHash string `json:"key_hash"` // sha256 ключа, hex: сам ключ daemon не получает
	Here    bool   `json:"here"`     // ключ только для агента из его каталога запуска
}

// pairRoutes — POST /v1/pair и GET /v1/pair/{id}: без токена (у агента его ещё
// нет); число и срок запросов ограничены, решение — только у владельца.
// local — сокет агентов (каталог и процессы daemon знает сам), иначе —
// публичный адрес.
func (d *Daemon) pairRoutes(mux *http.ServeMux, local bool) {
	mux.HandleFunc("POST /v1/pair", func(w http.ResponseWriter, r *http.Request) {
		var req PairRequest
		if err := readJSON(r, &req); err != nil {
			WriteError(w, 400, "bad_request", err.Error())
			return
		}
		if !agentNameRe.MatchString(req.Agent) || (req.Kind != "" && !agentNameRe.MatchString(req.Kind)) {
			WriteError(w, 400, "bad_request", "agent name: lowercase letters, digits, '.', '-', '_' (up to 64)")
			return
		}
		if !keyHashRe.MatchString(req.KeyHash) {
			WriteError(w, 400, "bad_request", "key_hash: sha256 of the key, hex")
			return
		}
		p := &state.Pairing{Agent: req.Agent, Kind: req.Kind, KeyHash: req.KeyHash, Via: "https"}
		if local {
			p.Via = "agent-socket"
			who, known := d.peerCaller(r)
			if c := callerOf(who, known); c != nil {
				p.Caller, _ = json.Marshal(c)
				if p.Kind == "" {
					p.Kind = who.Agent
				}
			}
			if req.Here {
				if !known || who.Dir == "" {
					WriteError(w, 400, "dir_unknown", "gate4ai cannot see the directory of this agent on this OS: pair without --here")
					return
				}
				p.Dir = who.Dir
			}
		} else if req.Here {
			WriteError(w, 400, "bad_request", "a key bound to a directory is only for an agent on the computer with gate4ai: pair without --here")
			return
		}
		p, err := d.State.PutPairing(p)
		if errors.Is(err, state.ErrTooManyPairings) {
			WriteError(w, 429, "too_many_requests", err.Error())
			return
		}
		if err != nil {
			WriteError(w, 400, "pair", err.Error())
			return
		}
		d.notifyPairing(p)
		WriteJSON(w, 200, pairView(p))
	})
	mux.HandleFunc("GET /v1/pair/{id}", func(w http.ResponseWriter, r *http.Request) {
		wait, _ := config.ParseDuration(r.URL.Query().Get("wait"))
		wait = min(wait, maxPublicWait)
		deadline := time.Now().Add(wait)
		for {
			p, err := d.State.GetPairing(r.PathValue("id"))
			if err != nil {
				WriteError(w, 404, "not_found", "unknown connection request")
				return
			}
			if p.Status != state.Pending || !time.Now().Before(deadline) {
				WriteJSON(w, 200, pairView(p))
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	})
}

// pairView — ответ агенту: без каталога и процессов (их видит владелец).
func pairView(p *state.Pairing) map[string]any {
	v := map[string]any{"id": p.ID, "agent": p.Agent, "code": p.Code, "status": p.Status, "expires": p.Expires}
	switch p.Status {
	case state.Pending:
		v["message"] = fmt.Sprintf("show this code to the user: %s — ask them to approve the connection of %s in gate4.ai (Web UI → Agents, or the Telegram bot) if the code matches, then run the command again", p.Code, p.Agent)
	case state.Approved:
		v["message"] = "connected: " + p.Agent + " now calls gate4ai with its own key"
	case state.Rejected:
		v["message"] = "the user rejected the connection: do not retry, tell the user"
	default:
		v["message"] = "the request expired without a decision: run the command again for a new code"
	}
	return v
}

// notifyPairing — владельцу: новый запрос на подключение.
func (d *Daemon) notifyPairing(p *state.Pairing) {
	d.notifiers.each(func(n pipeline.Notifier) {
		if c, ok := n.(Pairer); ok {
			c.PairRequested(p)
		}
	})
}

// DecidePairing — решение владельца по запросу на подключение (Web UI,
// Telegram). Одобрение добавляет агента в конфигурацию, если его там нет
// (профиль — profile, иначе по умолчанию: профиль агента того же типа или
// новый пустой), и делает ключ агента его токеном.
func (d *Daemon) DecidePairing(ctx context.Context, id string, approve bool, profile, channel string) (*state.Pairing, error) {
	p, err := d.State.GetPairing(id)
	if err != nil {
		return nil, err
	}
	if p.Status != state.Pending {
		if p.Status == state.Timeout {
			return nil, state.ErrExpired
		}
		return nil, state.ErrAlreadyDecided
	}
	if approve {
		cur, exists := d.Config.Current().Agents[p.Agent]
		if profile == "" && !exists {
			profile = d.defaultPairProfile(p)
		}
		if !exists || (profile != "" && profile != cur.Profile) {
			desc := cur.Description
			if !exists {
				desc = "connected by pairing"
				if p.Dir != "" {
					desc += " from " + p.Dir
				}
			}
			if _, err := d.Config.Edit(auth.OwnerVia(channel), "connect agent "+p.Agent, func(c *config.Config) error {
				if _, ok := c.Profiles[profile]; !ok {
					if err := c.EnsureProfile(profile); err != nil {
						return err
					}
				}
				return c.SetAgent(p.Agent, profile, desc)
			}); err != nil {
				return nil, err
			}
		}
	}
	return d.State.DecidePairing(id, approve, channel)
}

// defaultPairProfile — профиль нового агента: как у агента того же типа
// (ключ для каталога наследует права claude-code), иначе свой новый — пустой:
// владелец откроет ему коннекторы сам.
func (d *Daemon) defaultPairProfile(p *state.Pairing) string {
	cfg := d.Config.Current()
	if a, ok := cfg.Agents[p.Kind]; ok && p.Kind != "" {
		return a.Profile
	}
	return p.Agent
}
