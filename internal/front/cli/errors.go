// SPDX-License-Identifier: FSL-1.1-ALv2

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
			p.Hint = "the agent token is invalid or revoked: the owner issues a new one with gate4ai agents token <agent>"
		case "owner_only":
			p.Hint = "this command is for the owner: run it without GATE4AI_TOKEN on the computer with the daemon"
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
	code := ExitError
	var ae *APIError
	switch {
	case errors.Is(err, ErrUnavailable):
		code = ExitUnavailable
	case errors.As(err, &ae):
		if ae.Status == 400 || ae.Status == 404 {
			code = ExitUsage
		}
	}
	p := Explain(err, e.Getenv("GATE4AI_TOKEN") != "")
	e.problem(p)
	return code
}

// problem печатает Problem: JSON в stdout, человеку — в stderr.
func (e *Env) problem(p Problem) {
	out := map[string]any{"status": "error", "code": p.Code, "message": p.Message}
	if p.Hint != "" {
		out["hint"] = p.Hint
	}
	if p.Detail != "" {
		out["detail"] = p.Detail
	}
	e.printJSON(out)
	if e.StderrTTY {
		msg := p.Message
		if !strings.HasPrefix(msg, "gate4ai ") {
			msg = "gate4ai: " + msg
		}
		fmt.Fprintf(e.Stderr, "\n%s.\n", msg)
		if p.Hint != "" {
			fmt.Fprintln(e.Stderr, upperFirst(p.Hint)+".")
		}
	}
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
