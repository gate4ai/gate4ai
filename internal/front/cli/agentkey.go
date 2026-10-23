// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/agentid"
	"github.com/gate4ai/gate4ai/internal/platform/agentkeys"
	"github.com/gate4ai/gate4ai/internal/state"
)

// Ключи агентов (gate4ai pair). Агент без GATE4AI_TOKEN подключается сам:
// CLI узнаёт, что его вызвал агент (GATE4AI_AGENT, признаки в окружении —
// CLAUDECODE=1 и т.п., процессы-предки), создаёт ключ, сохраняет его в
// каталоге ключей (paths.AgentKeys, по файлу на агента) и просит daemon о
// подключении; daemon отвечает кодом, агент показывает его человеку, владелец
// подтверждает в Web UI или Telegram. Дальше CLI сам находит ключ этого агента
// — перезапускать агента не нужно.

// AgentKey — ключ агента в каталоге ключей.
type AgentKey = agentkeys.Key

// PairingError — агент ещё не подключён: ждёт решения владельца (код для
// сверки) или владелец отклонил подключение.
type PairingError struct {
	Agent, Code, Status, Message string
	File                         string // где лежит ключ
}

func (e *PairingError) Error() string { return e.Message }

func agentName(s string) string { return agentkeys.Name(s) }

func (e *Env) keyPath(agent string) string { return agentkeys.Path(e.KeyDir, agent) }
func (e *Env) loadKeys() []AgentKey        { return agentkeys.Load(e.KeyDir) }
func (e *Env) saveKey(k AgentKey) error    { return agentkeys.Save(e.KeyDir, k) }
func (e *Env) removeKey(agent string)      { agentkeys.Remove(e.KeyDir, agent) }

// PickKey — ключ для вызывающего (agentkeys.Pick).
func PickKey(keys []AgentKey, name, kind, dir string) (AgentKey, bool) {
	return agentkeys.Pick(keys, name, kind, dir)
}

// self — кто этот процесс (агент, каталог); без Self — только окружение.
func (e *Env) self() agentid.Caller {
	if !e.selfDone {
		e.selfDone = true
		if agentid.CheckOff(e.Getenv) {
			// Стенд, сам запущенный агентом (сборка с тегом gate4ai_devstand):
			// агента не ищем — daemon с той же переменной его тоже не отличает.
			return e.selfC
		}
		if e.Self != nil {
			e.selfC = e.Self()
		}
		if e.selfC.Agent == "" {
			e.selfC.Agent = agentid.FromEnv(e.Getenv)
		}
	}
	return e.selfC
}

// agentSelf — CLI вызван агентом: имя (GATE4AI_AGENT) и тип агента.
func (e *Env) agentSelf() (name, kind string, ok bool) {
	name, kind = e.Getenv("GATE4AI_AGENT"), e.self().Agent
	return name, kind, name != "" || kind != ""
}

// AgentMode — команды идут от агента: токен в GATE4AI_TOKEN или CLI узнал агента.
func (e *Env) AgentMode() bool {
	_, _, ok := e.agentSelf()
	return ok || e.Getenv("GATE4AI_TOKEN") != ""
}

// findKey — ключ этого агента в каталоге ключей.
func (e *Env) findKey() (AgentKey, bool) {
	name, kind, ok := e.agentSelf()
	if !ok {
		return AgentKey{}, false
	}
	return PickKey(e.loadKeys(), name, kind, e.self().Dir)
}

// keyClient — клиент с ключом агента: к сокету агентов или к адресу устройства.
func (e *Env) keyClient(k AgentKey) *Client {
	var c *Client
	if base := e.Getenv("GATE4AI_URL"); base == "" && k.URL != "" && e.Remote != nil {
		c = e.Remote(k.URL, k.Key)
	} else {
		c = e.Connect(k.Key)
	}
	c.Dir = e.self().Dir
	e.usedKey = &k
	return c
}

// pairClient — клиент без токена для запроса на подключение.
func (e *Env) pairClient(base string) *Client {
	if e.Pair == nil {
		return &Client{Err: errors.New("pairing is not available here")}
	}
	return e.Pair(base)
}

// lazyKeyClient — клиент, который при каждом запросе проверяет, подключён ли
// агент (и при необходимости просит daemon о подключении), пока владелец не
// подтвердит.
func (e *Env) lazyKeyClient() *Client {
	var mu sync.Mutex
	var got *Client
	return &Client{Lazy: func() (*Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if got != nil {
			return got, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		k, err := e.ensureKey(ctx, "", false)
		if err != nil {
			return nil, err
		}
		got = e.keyClient(k)
		return got, nil
	}}
}

// ensureKey — действующий ключ агента: найденный, подтверждённый с прошлого
// раза или новый запрос на подключение (тогда PairingError с кодом). name и
// here — явное имя и привязка к каталогу (gate4ai pair).
func (e *Env) ensureKey(ctx context.Context, name string, here bool) (AgentKey, error) {
	envName, kind, _ := e.agentSelf()
	if name == "" {
		name = envName
	}
	var k AgentKey
	var found bool
	switch {
	case name != "":
		k, found = PickKey(e.loadKeys(), name, "", "")
	case here:
		// Ключ этого каталога, если он уже есть (общий ключ типа не подходит).
		for _, x := range e.loadKeys() {
			if x.Kind == kind && x.Dir != "" && agentkeys.WithinDir(e.self().Dir, x.Dir) && (!found || len(x.Dir) > len(k.Dir)) {
				k, found = x, true
			}
		}
	default:
		k, found = PickKey(e.loadKeys(), "", kind, e.self().Dir)
	}
	if found && k.Pairing == "" {
		return k, nil
	}
	if found {
		var st struct {
			Status, Code, Message string
		}
		err := e.pairClient(k.URL).Do(ctx, "GET", "/v1/pair/"+k.Pairing, nil, &st)
		var ae *APIError
		switch {
		case err == nil && st.Status == state.Approved:
			k.Pairing, k.Code = "", ""
			if err := e.saveKey(k); err != nil {
				return k, err
			}
			return k, nil
		case err == nil && st.Status == state.Pending:
			return k, &PairingError{Agent: k.Agent, Code: k.Code, Status: state.Pending, Message: st.Message, File: e.keyPath(k.Agent)}
		case err == nil && st.Status == state.Rejected:
			e.removeKey(k.Agent)
			return k, &PairingError{Agent: k.Agent, Status: state.Rejected, Message: st.Message}
		case err == nil || errors.As(err, &ae) && ae.Status == 404:
			e.removeKey(k.Agent) // срок вышел: новый запрос
		default:
			return k, err
		}
		name = k.Agent
	}
	return e.requestPairing(ctx, name, kind, here)
}

// requestPairing — новый ключ и запрос на подключение.
func (e *Env) requestPairing(ctx context.Context, name, kind string, here bool) (AgentKey, error) {
	if name == "" {
		name = kind
		if here {
			if d := e.self().Dir; d != "" {
				name = agentName(kind + "-" + filepath.Base(d))
			}
		}
	}
	if name == "" {
		return AgentKey{}, errors.New("name the agent: gate4ai pair --name <agent> (or GATE4AI_AGENT=<agent>)")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return AgentKey{}, err
	}
	k := AgentKey{Agent: name, Kind: kind, Key: "g4a_" + base64.RawURLEncoding.EncodeToString(b), URL: e.Getenv("GATE4AI_URL"), Created: time.Now().UTC()}
	if here {
		k.Dir = e.self().Dir
	}
	var res struct {
		ID, Code, Status, Message string
	}
	if err := e.pairClient(k.URL).Do(ctx, "POST", "/v1/pair", map[string]any{"agent": name, "kind": kind, "key_hash": state.HashToken(k.Key), "here": here}, &res); err != nil {
		return AgentKey{}, err
	}
	k.Pairing, k.Code = res.ID, res.Code
	if err := e.saveKey(k); err != nil {
		return AgentKey{}, err
	}
	return k, &PairingError{Agent: name, Code: res.Code, Status: state.Pending, Message: res.Message, File: e.keyPath(name)}
}

// failPairing — ответ агенту о подключении: ждёт решения (exit 3) или отклонено (exit 7).
func (e *Env) failPairing(pe *PairingError) int {
	if pe.Status == state.Pending {
		msg := pe.Message
		if msg == "" {
			msg = fmt.Sprintf("show this code to the user: %s — ask them to approve the connection of %s in gate4.ai (Web UI → Agents, or the Telegram bot) if the code matches, then run the command again", pe.Code, pe.Agent)
		}
		hint := "tell the user the code " + pe.Code + " and wait for their approval; gate4ai pair --wait 5m waits for it"
		if e.human() {
			fmt.Fprintf(e.Stderr, "gate4ai: %s is not connected yet. Code: %s\nApprove the connection in the Web UI (gate4ai ui → Agents) or in the Telegram bot if the code matches; then run the command again.\n", pe.Agent, pe.Code)
			return ExitPending
		}
		e.printJSON(map[string]any{"status": "pending", "agent": pe.Agent, "code": pe.Code,
			"error": map[string]any{"code": "pairing_required", "message": msg, "hint": hint}})
		return ExitPending
	}
	e.problem(Problem{Code: "pairing_rejected", Message: pe.Message, Hint: "the user rejected the connection of " + pe.Agent + ": do not retry; tell the user"})
	return ExitAuth
}

// pair — gate4ai pair [--name agent] [--here] [--wait 5m]: подключить агента
// (новый ключ или проверить, подтверждён ли запрос).
func (e *Env) pair(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	name := fs.String("name", "", "agent name (default: the agent type, with --here: type-directory)")
	here := fs.Bool("here", false, "a key only for this agent's directory (where the agent was started)")
	wait := fs.Duration("wait", 0, "wait for the owner's decision, e.g. 5m")
	if fs.Parse(args) != nil {
		return ExitUsage
	}
	if e.Getenv("GATE4AI_TOKEN") != "" {
		e.problem(Problem{Code: "invalid_arguments", Message: "GATE4AI_TOKEN is set: this agent already calls gate4ai with that token", Hint: "unset GATE4AI_TOKEN to connect with a key of its own"})
		return ExitUsage
	}
	if _, kind, ok := e.agentSelf(); !ok && *name == "" || *here && kind == "" {
		e.problem(Problem{Code: "invalid_arguments", Message: "gate4ai does not see an agent here",
			Hint: "an agent pairs itself: run this from the agent's shell; or name it: gate4ai pair --name <agent>"})
		return ExitUsage
	}
	if *here && e.self().Dir == "" {
		e.problem(Problem{Code: "invalid_arguments", Message: "the directory of this agent is unknown on this OS", Hint: "pair without --here"})
		return ExitUsage
	}
	deadline := time.Now().Add(*wait)
	for {
		k, err := e.ensureKey(ctx, *name, *here)
		var pe *PairingError
		switch {
		case err == nil:
			e.out(map[string]any{"status": "ok", "agent": k.Agent, "dir": k.Dir, "key_file": e.keyPath(k.Agent)},
				func(w io.Writer) { fmt.Fprintf(w, "%s is connected (key: %s)\n", k.Agent, e.keyPath(k.Agent)) })
			return ExitOK
		case errors.As(err, &pe) && pe.Status == state.Pending && time.Now().Before(deadline):
			if e.StderrTTY {
				fmt.Fprintf(e.Stderr, "gate4ai: code %s — waiting for the owner's approval…\n", pe.Code)
			}
			// Ждём решения на стороне daemon (долгий опрос), не чаще раза в 2 с.
			left := time.Until(deadline).Round(time.Second)
			var st map[string]any
			k, _ := PickKey(e.loadKeys(), pe.Agent, "", "")
			_ = e.pairClient(k.URL).Do(ctx, "GET", "/v1/pair/"+k.Pairing+"?wait="+min(left, 2*time.Minute).String(), nil, &st)
			select {
			case <-ctx.Done():
				return e.failPairing(pe)
			case <-time.After(2 * time.Second):
			}
			*here = false // запрос уже есть: дальше — по имени
			*name = pe.Agent
		case errors.As(err, &pe):
			return e.failPairing(pe)
		default:
			return e.fail(err)
		}
	}
}
