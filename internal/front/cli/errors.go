// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
)

// UnavailableError — daemon недоступен: адрес и причина (errors.Is(err, ErrUnavailable)).
type UnavailableError struct {
	Addr   string // путь сокета / pipe или адрес устройства
	Remote bool   // удалённый daemon (GATE4AI_URL)
	Err    error
}

func (e *UnavailableError) Error() string   { return ErrUnavailable.Error() + ": " + e.Err.Error() }
func (e *UnavailableError) Is(t error) bool { return t == ErrUnavailable }
func (e *UnavailableError) Unwrap() error   { return e.Err }

// Problem — ошибка для человека и агента: код, короткое сообщение, что делать
// (hint) и подробности (detail).
type Problem struct {
	Code    string
	Message string
	Hint    string
	Detail  string
}

// Подсказки, как запустить daemon.
const (
	hintStart      = "start it: gate4ai daemon start (in the background) or gate4ai service install (also at every login); gate4ai daemon runs it in this terminal"
	hintAgentStart = "ask the owner of this computer to start gate4ai (gate4ai daemon start)"
)

// Explain разбирает ошибку CLI; agent — вызов с токеном агента (команды
// владельца ему не подсказываются).
func Explain(err error, agent bool) Problem {
	var ue *UnavailableError
	var ae *APIError
	switch {
	case errors.As(err, &ue):
		return explainUnavailable(ue, agent)
	case errors.Is(err, ErrUnavailable):
		return Problem{Code: "unavailable", Message: "gate4ai daemon is not running", Hint: pick(agent, hintAgentStart, hintStart), Detail: err.Error()}
	case errors.As(err, &ae):
		p := Problem{Code: ae.Code, Message: ae.Message}
		if p.Message == "" {
			p.Message = ae.Code
		}
		switch ae.Code {
		case "unauthorized":
			p.Hint = "the agent token is invalid or revoked: the owner connects the agent again (Web UI → Agents) or issues a new token with gate4ai agents token <agent>"
		case "owner_only":
			p.Hint = "this command is for the owner: run it without GATE4AI_TOKEN on the computer with the daemon"
		case "agent_required":
			p.Hint = "an agent calls gate4ai with a key of its own (it connects itself: the command shows a code, the owner approves it in the Web UI) or a token (GATE4AI_TOKEN); the owner runs an action with an agent's profile: --as <agent>"
		}
		return p
	}
	return Problem{Code: "error", Message: err.Error()}
}

func explainUnavailable(ue *UnavailableError, agent bool) Problem {
	p := Problem{Code: "unavailable", Detail: ue.Err.Error()}
	if ue.Remote {
		p.Message = "the device is offline or unreachable"
		p.Hint = "the computer with gate4ai may be asleep or offline; retry later"
		p.Detail = ue.Addr + ": " + ue.Err.Error()
		return p
	}
	start := pick(agent, hintAgentStart, hintStart)
	switch max := ipc.MaxPath(); {
	case max > 0 && len(ue.Addr) > max:
		p.Message = "the daemon socket path is too long"
		p.Hint = "set GATE4AI_SOCKET and GATE4AI_AGENT_SOCKET to shorter paths (or a shorter GATE4AI_HOME) for both the daemon and the CLI"
		p.Detail = fmt.Sprintf("%s is %d bytes, the limit is %d", ue.Addr, len(ue.Addr), max)
	case errors.Is(ue.Err, syscall.ECONNREFUSED):
		p.Message = "gate4ai daemon is not running (it stopped or crashed, the socket is left over)"
		p.Hint = start
		p.Detail = "connection refused at " + ue.Addr + "; log of a background daemon: " + paths.DaemonLog()
	case errors.Is(ue.Err, os.ErrNotExist):
		p.Message = "gate4ai daemon is not running"
		p.Hint = start
		p.Detail = "no socket at " + ue.Addr
	case errors.Is(ue.Err, os.ErrPermission):
		p.Message = "no access to the daemon socket"
		p.Hint = "run gate4ai as the same user as the daemon"
		p.Detail = ue.Addr + ": " + ue.Err.Error()
	default:
		p.Message = "gate4ai daemon is not reachable"
		p.Hint = start
		p.Detail = ue.Addr + ": " + ue.Err.Error()
	}
	return p
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// fail печатает ошибку JSON-ом (stdout) и, если stderr — терминал, подсказку
// для человека; возвращает код выхода.
func (e *Env) fail(err error) int {
	var pe *PairingError
	if errors.As(err, &pe) {
		return e.failPairing(pe)
	}
	code := ExitError
	var ae *APIError
	switch {
	case errors.Is(err, ErrUnavailable):
		code = ExitUnavailable
	case errors.As(err, &ae):
		switch {
		case authCodes[ae.Code]:
			code = ExitAuth
		case ae.Status == 400 || ae.Status == 404:
			code = ExitUsage
		}
	}
	p := Explain(err, e.AgentMode())
	switch {
	case (p.Code == "owner_only" || p.Code == "agent_required" && e.ownerCmd) && len(e.cmdline) > 0:
		// Готовая команда для владельца: агент передаёт её человеку как есть.
		p.Hint = "this command is for the owner: ask the owner to run it in their own terminal: gate4ai " + shellJoin(e.cmdline)
		if e.Getenv("GATE4AI_TOKEN") != "" {
			p.Hint = "this command is for the owner: ask the owner to run it in their own terminal: env -u GATE4AI_TOKEN gate4ai " + shellJoin(e.cmdline)
		}
	case p.Code == "unauthorized" && e.usedKey != nil:
		p.Hint = fmt.Sprintf("the key of %s (%s) is revoked, unknown or does not fit this directory: tell the user; a new connection: gate4ai pair --name %s after removing that file",
			e.usedKey.Agent, e.keyPath(e.usedKey.Agent), e.usedKey.Agent)
	case p.Code == "agent_required" && e.AgentMode():
		p.Hint = "gate4ai sees an agent: the agent connects with a key of its own — run the command from the agent's shell (gate4ai pairs it and shows a code), or name the agent: GATE4AI_AGENT=<agent>"
	}
	e.problem(p)
	return code
}

// problem печатает Problem: JSON в stdout и подсказку в stderr терминала; если
// stdout — терминал (human), только текст в stderr.
func (e *Env) problem(p Problem) {
	msg := p.Message
	if !strings.HasPrefix(msg, "gate4ai ") {
		msg = "gate4ai: " + msg
	}
	if e.human() {
		// Человеку в терминале — только текст: что случилось, что делать, подробности.
		fmt.Fprintln(e.Stderr, sentence(msg))
		if p.Hint != "" {
			fmt.Fprintln(e.Stderr, sentence(upperFirst(p.Hint)))
		}
		if p.Detail != "" {
			fmt.Fprintln(e.Stderr, "("+p.Detail+")")
		}
		return
	}
	ee := map[string]any{"code": p.Code, "message": p.Message}
	if p.Hint != "" {
		ee["hint"] = p.Hint
	}
	if p.Detail != "" {
		ee["detail"] = p.Detail
	}
	e.printJSON(map[string]any{"status": "error", "error": ee})
	if e.StderrTTY {
		fmt.Fprintf(e.Stderr, "\n%s\n", sentence(msg))
		if p.Hint != "" {
			fmt.Fprintln(e.Stderr, sentence(upperFirst(p.Hint)))
		}
	}
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// sentence — точка в конце, если её нет (и нет ? или !).
func sentence(s string) string {
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "!") {
		return s
	}
	return s + "."
}

// shellJoin — аргументы для копирования в shell (кавычки там, где нужны).
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"$`\\|&;<>()*?#~") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}
