// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/front/skill"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"golang.org/x/term"
)

// Env — окружение CLI (подменяется в тестах).
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// Connect возвращает клиента: с токеном агента — к сокету агентов, без — к сокету владельца.
	Connect func(token string) *Client
	// Extra — дополнительные команды (mcp), регистрируются в cmd/gate4ai.
	Extra map[string]func(ctx context.Context, e *Env, args []string) int
	// Interactive — stdin и stderr — терминал: команду ввёл человек (можно
	// спрашивать и запускать daemon в фоне). StderrTTY — подсказки для человека в stderr.
	Interactive, StderrTTY bool
	// StdoutTTY — stdout — терминал: списки таблицей, ошибки текстом (output.go);
	// ForceJSON — --json у табличной команды.
	StdoutTTY, ForceJSON bool
	// cmdline — аргументы команды (подсказка owner_only — готовая команда).
	cmdline []string
	// ForceTable — таблица и вне терминала (--table у approvals list).
	ForceTable bool
	// StartBinary — бинарник gate4ai для запуска daemon в фоне ("" — этот же).
	StartBinary string
}

// DefaultEnv — реальное окружение процесса.
func DefaultEnv() *Env {
	tty := func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
	errTTY := tty(os.Stderr)
	return &Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Interactive: errTTY && tty(os.Stdin), StderrTTY: errTTY, StdoutTTY: tty(os.Stdout),
		Connect: func(token string) *Client {
			if token != "" {
				// Удалённый агент: daemon на другом компьютере, доступ по HTTPS.
				if base := os.Getenv("GATE4AI_URL"); base != "" {
					c, err := NewRemoteClient(base, token, os.Getenv("GATE4AI_CA_FILE"))
					if err != nil {
						return &Client{Token: token, Err: err}
					}
					return c
				}
				return NewSocketClient(paths.FindAgentSocket(), token)
			}
			return NewSocketClient(paths.FindOwnerSocket(), "")
		}, Extra: map[string]func(context.Context, *Env, []string) int{}}
}

// Client — клиент к daemon с токеном из GATE4AI_TOKEN.
func (e *Env) Client() *Client { return e.Connect(e.Getenv("GATE4AI_TOKEN")) }

// call — запрос к daemon с выводом ответа JSON-ом.
func (e *Env) call(ctx context.Context, c *Client, method, path string, body any) int {
	var res any
	if err := c.Do(ctx, method, path, body, &res); err != nil {
		return e.fail(err)
	}
	e.printJSON(res)
	return ExitOK
}

func (e *Env) printJSON(v any) {
	enc := json.NewEncoder(e.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

const usage = `gate4ai — actions for AI agents under your policies

Start here (on the computer you protect):
  gate4ai service install                 run the daemon now and at every login (or: gate4ai daemon start)
  gate4ai ui [--print] [--remote] [--start|--no-start]   open the web UI (one-time link, 2 minutes; starts the daemon at a terminal)
  gate4ai status                          is the daemon running

Agent commands (token in GATE4AI_TOKEN; daemon on another computer: GATE4AI_URL=https://<device address>):
  gate4ai tools [--json] [--as agent]     actions available to this agent (--as: owner, another agent's catalog)
  gate4ai <connector> <action> [--arg v] [--wait 10m] [--json '{...}'] [--as agent]   run an action (--help on the action)
  gate4ai status <id> [--wait 10m]        result of an action pending approval
  gate4ai skill                           print SKILL.md for this agent
  gate4ai mcp                             MCP server on stdio

Daemon:
  gate4ai daemon [flags]                  run in this terminal (flags: gate4ai daemon --help)
  gate4ai daemon start [flags] | stop | restart | status
  gate4ai service install [--no-tray] [-- daemon flags] | uninstall | status   autostart at login

Owner commands (local socket):
  gate4ai log [--agent a] [--action x] [--effect allow|ask|deny] [--since 24h] [--parent #hash] [--limit 50] [--json]
  gate4ai show <#hash|id>                 one receipt
  gate4ai export -o file.tar.gz [--passphrase-env VAR]
  gate4ai verify file.tar.gz [--pubkey hex] [--passphrase-env VAR]   (offline)
  gate4ai journal verify
  gate4ai agents list | token <agent> [--label l] | connect <agent> | revoke <agent>
  gate4ai policy show | set <file> [-m comment] | versions | rollback <n> | test --agent a --action x [--args json] [--file draft.yaml]
  gate4ai approvals [list] [--table|--json] | approve <id> | reject <id> | questions | answer <id> (value from stdin)
  gate4ai vault list | set <alias> [--from-env VAR] (asks with hidden input; or the value from stdin) | rm <alias>
  gate4ai sites [allow | remove <site>...]   sites agents may open in the browser
  gate4ai relay register [--url https://relay.gate4.ai] [--ca-file pem] [--self-signed] | status
  gate4ai connectors list | install <file.g4c | name[@version] | source folder> [--dev] [--yes] [--watch] | update <name> [--yes] [--watch] | remove <name> | logs <name> [--limit n] [--since 10m] | refresh
  gate4ai connectors new <name> --site <domain> [-o folder]   a connector over the browser to start from (offline)
  gate4ai connectors pack <dir> [--wasm file] [--key file] [-o file.g4c] | verify <dir> <file.g4c> | keygen [-o key]   (publisher / registry review, offline)
  gate4ai registry check <index.json> [--key ed25519:…] [--fetch] | sign <index.json> --key file | add <index.json> <pack.json> --publisher name …   (registry)
  gate4ai fixture scrub <file.json> [-o out] [--replace "real=fake"]... [--auto]   personal data out of a recorded browser session
  gate4ai contract gen <manifest.yaml> [-o client.go] [--package name] | diff <old manifest> <new manifest>   (calls between connectors)
  gate4ai run <agent> [--workspace dir] [--container|--microvm] [--image img] [--engine docker|podman] [--dry-run] [-- cmd]   isolated agent
  gate4ai doctor                          look for ways around gate4ai
  gate4ai egress                          network connections of sandboxed agents

Output: JSON. At a terminal (stdout is a terminal) lists and status are tables and errors are text;
  --json or GATE4AI_OUTPUT=json gives JSON there too, GATE4AI_OUTPUT=table gives tables anywhere.
Exit codes: 0 ok, 1 error, 2 denied by policy, 3 pending approval, 4 verification failed, 5 usage, 6 daemon unavailable.
`

// commands — встроенные команды (остальное первое слово — имя коннектора).
var commands = []string{"help", "version", "tools", "status", "skill", "mcp", "daemon", "service", "log", "show", "export",
	"verify", "journal", "agents", "policy", "approvals", "vault", "sites", "relay", "connectors", "registry", "contract", "fixture", "ui", "run", "doctor", "egress"}

// Run — точка входа CLI.
func Run(ctx context.Context, e *Env, args []string) int {
	e.cmdline = args
	// `gate4ai --json <команда> …` — то же, что --json после команды.
	if len(args) > 1 && (args[0] == "--json" || args[0] == "-json") {
		e.ForceJSON, args = true, args[1:]
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 && isCommand(args[1]) {
			return e.commandHelp(args[1])
		}
		fmt.Fprint(e.Stdout, usage)
		return ExitOK
	}
	cmd, rest := args[0], args[1:]
	if isCommand(cmd) && wantsHelp(rest) {
		return e.commandHelp(cmd)
	}
	if f, ok := e.Extra[cmd]; ok {
		return f(ctx, e, rest)
	}
	if tableCommands[cmd] {
		var j bool
		if rest, j = stripJSONFlag(rest); j {
			e.ForceJSON = true
		}
	}
	switch cmd {
	case "version", "--version":
		fmt.Fprintln(e.Stdout, "gate4ai", version.Version)
		return ExitOK
	case "daemon":
		if len(rest) > 0 {
			return DaemonCtl(ctx, e, rest[0], rest[1:])
		}
		fmt.Fprint(e.Stderr, DaemonUsage)
		return ExitUsage
	case "service":
		return e.service(ctx, rest)
	case "tools":
		return e.tools(ctx, rest)
	case "status":
		if len(rest) == 0 {
			return e.daemonStatus(ctx)
		}
		return e.status(ctx, rest)
	case "skill":
		return e.skill(ctx)
	case "log":
		return e.log(ctx, rest)
	case "show":
		return e.show(ctx, rest)
	case "export":
		return e.export(ctx, rest)
	case "verify":
		return e.verify(rest)
	case "journal":
		return e.journal(ctx, rest)
	case "agents":
		return e.agents(ctx, rest)
	case "policy":
		return e.policy(ctx, rest)
	case "approvals":
		return e.approvals(ctx, rest)
	case "vault":
		return e.vault(ctx, rest)
	case "relay":
		return e.relay(ctx, rest)
	case "connectors":
		return e.connectors(ctx, rest)
	case "registry":
		return e.registry(ctx, rest)
	case "contract":
		return e.contract(rest)
	case "fixture":
		return e.fixture(rest)
	case "sites":
		return e.sites(ctx, rest)
	case "ui":
		return e.ui(ctx, rest)
	case "run":
		return e.run(ctx, rest)
	case "doctor":
		return e.doctor(ctx)
	case "egress":
		return e.call(ctx, e.Client(), "GET", "/v1/egress/log?limit=200", nil)
	}
	return e.action(ctx, cmd, rest)
}

func isCommand(s string) bool { return slices.Contains(commands, s) }

// wantsHelp — среди аргументов встроенной команды есть --help / -h (до `--`:
// после него — команда агента в `gate4ai run`).
func wantsHelp(rest []string) bool {
	for _, a := range rest {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" || a == "-help" {
			return true
		}
	}
	return false
}

// commandHelp печатает строки общей справки об одной команде.
func (e *Env) commandHelp(cmd string) int {
	if cmd == "daemon" {
		fmt.Fprint(e.Stdout, DaemonUsage)
		return ExitOK
	}
	if cmd == "service" {
		fmt.Fprintln(e.Stdout, serviceUsage)
		return ExitOK
	}
	var lines []string
	for _, l := range strings.Split(usage, "\n") {
		t := strings.TrimSpace(l)
		if t == "gate4ai "+cmd || strings.HasPrefix(t, "gate4ai "+cmd+" ") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		fmt.Fprint(e.Stdout, usage)
		return ExitOK
	}
	fmt.Fprintln(e.Stdout, "usage:")
	fmt.Fprintln(e.Stdout, strings.Join(lines, "\n"))
	return ExitOK
}

// suggest — ближайшая встроенная команда или коннектор для опечатки ("" — нет похожих).
func suggest(word string, names []string) string {
	best, bestD := "", 3
	if len(word) <= 4 {
		bestD = 2 // у коротких слов расстояние 2 — почти любое слово
	}
	for _, n := range names {
		if d := distance(word, n); d < bestD && d < len(n) {
			best, bestD = n, d
		}
	}
	return best
}

// distance — расстояние Дамерау–Левенштейна (перестановка соседних букв — одна правка).
func distance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

// Tools запрашивает каталог.
func (e *Env) Tools(ctx context.Context, as string) ([]skill.Tool, error) {
	var tools []skill.Tool
	path := "/v1/tools"
	if as != "" {
		path += "?agent=" + as
	}
	err := e.Client().Do(ctx, "GET", path, nil, &tools)
	return tools, err
}

func (e *Env) tools(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	asJSON := fs.Bool("json", false, "JSON output")
	as := fs.String("as", "", "owner: show the catalog of this agent")
	if fs.Parse(args) != nil {
		return ExitUsage
	}
	tools, err := e.Tools(ctx, *as)
	if err != nil {
		return e.fail(err)
	}
	if len(tools) == 0 {
		if e.Getenv("GATE4AI_TOKEN") != "" {
			fmt.Fprintln(e.Stderr, "gate4ai: no actions are allowed for this agent; the owner allows them in the agent's profile (gate4ai policy set)")
		} else {
			fmt.Fprintln(e.Stderr, "gate4ai: no actions yet: allow connectors in a profile (gate4ai policy set config.yaml); more connectors: gate4ai connectors install <name>")
		}
	}
	if *asJSON {
		e.printJSON(tools)
		return ExitOK
	}
	for _, t := range tools {
		fmt.Fprintf(e.Stdout, "%-28s %-9s %s\n", t.Connector+" "+t.Action, t.Class, t.Description)
	}
	return ExitOK
}

func (e *Env) skill(ctx context.Context) int {
	tools, err := e.Tools(ctx, "")
	if err != nil {
		return e.fail(err)
	}
	fmt.Fprint(e.Stdout, skill.Generate(tools))
	return ExitOK
}

// knownConnectors — коннекторы ядра и магазина по умолчанию: их имена — не
// опечатки встроенных команд (files ≠ sites), даже когда daemon не отвечает.
var knownConnectors = map[string]bool{"files": true, "browser": true, "email": true, "gate4ai": true}

// connectorAliases — привычные имена коннекторов в CLI.
var connectorAliases = map[string]string{"mail": "email"}

// action — `gate4ai <connector> <action> --arg value ...`.
func (e *Env) action(ctx context.Context, conn string, args []string) int {
	if a, ok := connectorAliases[conn]; ok {
		conn = a
	}
	tools, err := e.Tools(ctx, "")
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			// Опечатку во встроенной команде видно и без daemon.
			if s := suggest(conn, commands); s != "" && !knownConnectors[conn] {
				e.problem(Problem{Code: "unknown_command", Message: fmt.Sprintf("unknown command %q", conn), Hint: "did you mean gate4ai " + s + "?"})
				return ExitUsage
			}
			p := Explain(err, e.Getenv("GATE4AI_TOKEN") != "")
			p.Detail = fmt.Sprintf("%q is not a built-in command, and connector names come from the daemon; %s", conn, p.Detail)
			e.problem(p)
			return ExitUnavailable
		}
		return e.fail(err)
	}
	var mine []skill.Tool
	names := append([]string{}, commands...)
	for _, t := range tools {
		if t.Connector == conn {
			mine = append(mine, t)
		}
		names = append(names, t.Connector)
	}
	if len(mine) == 0 {
		p := Problem{Code: "unknown_action", Message: fmt.Sprintf("unknown command or connector %q", conn), Hint: "see gate4ai --help and gate4ai tools"}
		if s := suggest(conn, names); s != "" {
			p.Hint = "did you mean gate4ai " + s + "?"
		}
		e.problem(p)
		return ExitUsage
	}
	args, pre := hoistCallOpts(args)
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(e.Stdout, "gate4ai %s <action>\n\n", conn)
		for _, t := range mine {
			fmt.Fprintf(e.Stdout, "  %-12s %-9s %s\n", t.Action, t.Class, t.Description)
		}
		return ExitOK
	}
	var tool *skill.Tool
	for i := range mine {
		if mine[i].Action == args[0] {
			tool = &mine[i]
		}
	}
	if tool == nil {
		var acts []string
		for _, t := range mine {
			acts = append(acts, t.Action)
		}
		p := Problem{Code: "unknown_action", Message: fmt.Sprintf("unknown action %s.%s", conn, args[0]), Hint: "actions: " + strings.Join(acts, ", ")}
		if s := suggest(args[0], acts); s != "" {
			p.Hint = "did you mean gate4ai " + conn + " " + s + "?"
		}
		e.problem(p)
		return ExitUsage
	}
	vals, opts, err := parseArgs(*tool, append(append([]string{}, args[1:]...), pre...))
	if err != nil {
		e.printJSON(map[string]any{"status": "error", "code": "invalid_arguments", "message": err.Error()})
		return ExitUsage
	}
	if opts.help {
		e.actionHelp(*tool)
		return ExitOK
	}
	if opts.json {
		e.ForceJSON = true
	}
	body := map[string]any{"action": tool.Name, "args": vals, "wait": opts.wait, "agent": opts.as}
	var res map[string]any
	if err := e.Client().Do(ctx, "POST", "/v1/call", body, &res); err != nil {
		return e.fail(err)
	}
	e.printJSON(res)
	return ExitForResult(res)
}

// ExitForResult — код выхода по результату pipeline.
func ExitForResult(res map[string]any) int {
	code, _ := res["code"].(string)
	switch res["status"] {
	case "ok":
		return ExitOK
	case "denied":
		return ExitDenied
	case "pending":
		return ExitPending
	case "verification_failed":
		return ExitVerification
	}
	switch code {
	case "invalid_arguments", "unknown_action":
		return ExitUsage
	case "connector_unavailable":
		return ExitUnavailable
	}
	return ExitError
}

func (e *Env) actionHelp(t skill.Tool) {
	fmt.Fprintf(e.Stdout, "gate4ai %s %s — %s\nEffect class: %s\n\nArguments:\n", t.Connector, t.Action, t.Description, t.Class)
	for _, p := range t.Schema.Properties {
		req := ""
		if t.Schema.IsRequired(p.Name) {
			req = " (required)"
		}
		fmt.Fprintf(e.Stdout, "  --%-14s %s%s  %s\n", p.Name, flagType(p.Schema), req, p.Schema.Description)
	}
	fmt.Fprintf(e.Stdout, "\nOptions:\n  --wait 10m        wait for approval if needed\n  --json '{...}'    all arguments as a JSON object (bare --json: JSON output, as always)\n  --as <agent>      owner only: run with this agent's profile\n\nExample:\n  %s\n", skill.Example(t))
}

// flagType — тип флага для --help.
func flagType(s *manifest.Schema) string {
	switch {
	case jsonFlag(s):
		return "JSON " + s.Type
	case s.Type == manifest.TArray:
		return s.Items.Type + ", repeatable"
	}
	return s.Type
}

// jsonFlag — значение флага задаётся JSON: объекты и массивы объектов или массивов.
func jsonFlag(s *manifest.Schema) bool {
	return s.Type == manifest.TObject || s.Type == manifest.TArray && (s.Items.Type == manifest.TObject || s.Items.Type == manifest.TArray)
}

// scalar разбирает значение флага скалярного типа.
func scalar(name, typ, v string) (any, error) {
	switch typ {
	case manifest.TNumber, manifest.TInteger:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s must be a number", name)
		}
		return f, nil
	case manifest.TBoolean:
		return v == "true" || v == "1" || v == "yes", nil
	}
	return v, nil
}

// hoistCallOpts снимает опции вызова (--json, --wait v, --as v), стоящие перед
// именем действия: `gate4ai files --json list …`; они разбираются вместе с
// аргументами действия.
func hoistCallOpts(args []string) (rest, opts []string) {
	for len(args) > 0 {
		a := args[0]
		name, _, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case !strings.HasPrefix(a, "--"):
			return args, opts
		case name == "json" && !hasVal:
			opts, args = append(opts, a), args[1:]
		case (name == "wait" || name == "as") && hasVal:
			opts, args = append(opts, a), args[1:]
		case (name == "wait" || name == "as") && len(args) > 1:
			opts, args = append(opts, args[0], args[1]), args[2:]
		default:
			return args, opts
		}
	}
	return args, opts
}

type callOpts struct {
	wait string
	as   string
	help bool
	json bool // --json без значения: вывод JSON
}

// parseArgs разбирает --name value / --name=value по схеме действия.
func parseArgs(t skill.Tool, args []string) (map[string]any, callOpts, error) {
	var o callOpts
	vals := map[string]any{}
	types := map[string]*manifest.Schema{}
	for _, p := range t.Schema.Properties {
		types[p.Name] = p.Schema
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--help" || a == "-h" {
			o.help = true
			continue
		}
		if !strings.HasPrefix(a, "--") {
			return nil, o, fmt.Errorf("unexpected argument %q (use --name value)", a)
		}
		name, val, hasVal := strings.Cut(a[2:], "=")
		name = strings.ReplaceAll(name, "-", "_")
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("--%s needs a value", name)
			}
			i++
			return args[i], nil
		}
		// --wait, --as и --json — опции вызова, если у действия нет
		// аргумента с таким именем.
		_, isArg := types[name]
		switch {
		case isArg:
		case name == "wait" || name == "as":
			v, err := next()
			if err != nil {
				if name == "wait" {
					return nil, o, errors.New("--wait needs a duration, e.g. --wait 10m")
				}
				return nil, o, err
			}
			if name == "wait" {
				o.wait = v
			} else {
				o.as = v
			}
			continue
		case name == "json" && !hasVal && (i+1 >= len(args) || strings.HasPrefix(args[i+1], "-")):
			// --json без значения — вывод JSON (вывод действия и так JSON).
			o.json = true
			continue
		case name == "json":
			v, err := next()
			if err != nil {
				return nil, o, err
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(v), &m); err != nil {
				return nil, o, fmt.Errorf("--json: %v", err)
			}
			maps.Copy(vals, m)
			continue
		}
		sch, ok := types[name]
		if !ok {
			return nil, o, fmt.Errorf("unknown argument --%s for %s (see --help)", name, t.Name)
		}
		if sch.Type == manifest.TBoolean && !hasVal {
			if i+1 < len(args) && (args[i+1] == "true" || args[i+1] == "false") {
				i++
				vals[name] = args[i] == "true"
			} else {
				vals[name] = true
			}
			continue
		}
		v, err := next()
		if err != nil {
			return nil, o, err
		}
		switch {
		case jsonFlag(sch):
			var x any
			if err := json.Unmarshal([]byte(v), &x); err != nil {
				return nil, o, fmt.Errorf("--%s must be JSON: %v", name, err)
			}
			vals[name] = x
		case sch.Type == manifest.TArray:
			l, _ := vals[name].([]any)
			for _, part := range strings.Split(v, ",") {
				if p := strings.TrimSpace(part); p != "" {
					x, err := scalar(name, sch.Items.Type, p)
					if err != nil {
						return nil, o, err
					}
					l = append(l, x)
				}
			}
			vals[name] = l
		default:
			x, err := scalar(name, sch.Type, v)
			if err != nil {
				return nil, o, err
			}
			vals[name] = x
		}
	}
	return vals, o, nil
}

func (e *Env) status(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	wait := fs.String("wait", "", "wait for the decision, e.g. 10m")
	id, rest := firstPositional(args)
	if fs.Parse(rest) != nil || id == "" {
		fmt.Fprintln(e.Stderr, "usage: gate4ai status <id> [--wait 10m]")
		return ExitUsage
	}
	if strings.HasPrefix(id, "#") {
		// #хэш — квитанция журнала, а не id ожидающего действия.
		return e.fail(fmt.Errorf("%s is a journal receipt, not an action id: gate4ai show '%s' (owner), its calls: gate4ai log --parent '%s'", id, id, id))
	}
	path := "/v1/status/" + id
	if *wait != "" {
		path += "?wait=" + *wait
	}
	var res map[string]any
	if err := e.Client().Do(ctx, "GET", path, nil, &res); err != nil {
		return e.fail(err)
	}
	e.printJSON(res)
	return ExitForResult(res)
}

// firstPositional отделяет первый позиционный аргумент от флагов (в любом порядке).
func firstPositional(args []string) (string, []string) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			if i > 0 && strings.HasPrefix(args[i-1], "--") && !strings.Contains(args[i-1], "=") && !isBoolFlag(args[i-1]) {
				continue
			}
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return "", args
}

func isBoolFlag(f string) bool { return f == "--json" || f == "--container" || f == "--microvm" }

func (e *Env) log(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	agent := fs.String("agent", "", "")
	action := fs.String("action", "", "")
	effect := fs.String("effect", "", "allow | ask | deny")
	since := fs.String("since", "", "e.g. 24h, 7d")
	limit := fs.Int("limit", 50, "")
	parent := fs.String("parent", "", "only the calls (service_call) of this action: receipt #hash or id")
	asJSON := fs.Bool("json", false, "an array of {receipt: {id, ts, agent, action, parent, args, decision, execution, verification}, hash}")
	if fs.Parse(args) != nil {
		return ExitUsage
	}
	q := "/v1/log?" + url.Values{"agent": {*agent}, "action": {*action}, "effect": {*effect}, "since": {*since},
		"limit": {fmt.Sprint(*limit)}, "parent": {*parent}}.Encode()
	var es []audit.Entry
	if err := e.Client().Do(ctx, "GET", q, nil, &es); err != nil {
		return e.fail(err)
	}
	if len(es) == 0 {
		if *agent+*action+*effect+*since+*parent == "" {
			fmt.Fprintln(e.Stderr, "gate4ai: the journal is empty: no agent actions yet")
		} else {
			fmt.Fprintln(e.Stderr, "gate4ai: no journal entries match the filters")
		}
	}
	if *asJSON {
		e.printJSON(es)
		return ExitOK
	}
	for _, x := range es {
		r := x.Receipt
		v := r.Verification.Status
		fmt.Fprintf(e.Stdout, "%s  %s  %-14s %-22s %-5s %-9s %s\n", audit.Short(x.Hash), r.TS.Local().Format("2006-01-02 15:04:05"),
			r.Agent, r.Action, r.Decision.Effect, r.Execution.Status, v)
	}
	return ExitOK
}

func (e *Env) show(ctx context.Context, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(e.Stderr, "usage: gate4ai show <#hash|id>")
		return ExitUsage
	}
	var ent audit.Entry
	if err := e.Client().Do(ctx, "GET", "/v1/show/"+strings.TrimPrefix(args[0], "#"), nil, &ent); err != nil {
		return e.fail(err)
	}
	e.printJSON(ent)
	return ExitOK
}

func (e *Env) export(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	out := fs.String("o", "", "output file")
	passEnv := fs.String("passphrase-env", "", "encrypt the archive with the passphrase from this environment variable")
	if fs.Parse(args) != nil || *out == "" {
		fmt.Fprintln(e.Stderr, "usage: gate4ai export -o file.tar.gz [--passphrase-env VAR]")
		return ExitUsage
	}
	pass := ""
	if *passEnv != "" {
		pass = e.Getenv(*passEnv)
	}
	var b []byte
	if err := e.Client().Do(ctx, "POST", "/v1/export", map[string]string{"passphrase": pass}, &b); err != nil {
		return e.fail(err)
	}
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		return e.fail(err)
	}
	e.printJSON(map[string]any{"status": "ok", "file": *out, "bytes": len(b), "encrypted": pass != ""})
	return ExitOK
}

// verify — офлайн-проверка архива, daemon не нужен.
func (e *Env) verify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	pub := fs.String("pubkey", "", "expected device public key (hex)")
	passEnv := fs.String("passphrase-env", "", "")
	file, rest := firstPositional(args)
	if fs.Parse(rest) != nil || file == "" {
		fmt.Fprintln(e.Stderr, "usage: gate4ai verify file.tar.gz [--pubkey hex] [--passphrase-env VAR]")
		return ExitUsage
	}
	f, err := os.Open(file)
	if err != nil {
		return e.fail(err)
	}
	defer f.Close()
	pass := ""
	if *passEnv != "" {
		pass = e.Getenv(*passEnv)
	}
	if *pub != "" {
		if _, err := hex.DecodeString(*pub); err != nil {
			return e.fail(fmt.Errorf("--pubkey: %v", err))
		}
	}
	rep, err := audit.VerifyArchive(f, pass, *pub)
	if err != nil {
		return e.fail(err)
	}
	e.printJSON(map[string]any{"ok": rep.OK(), "report": rep})
	if !rep.OK() {
		return ExitVerification
	}
	return ExitOK
}

func (e *Env) journal(ctx context.Context, args []string) int {
	if len(args) != 1 || args[0] != "verify" {
		fmt.Fprintln(e.Stderr, "usage: gate4ai journal verify")
		return ExitUsage
	}
	var res map[string]any
	if err := e.Client().Do(ctx, "POST", "/v1/journal/verify", nil, &res); err != nil {
		return e.fail(err)
	}
	e.printJSON(res)
	if res["ok"] != true {
		return ExitVerification
	}
	return ExitOK
}

func (e *Env) agents(ctx context.Context, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	c := e.Client()
	switch sub {
	case "list":
		var res any
		var l struct {
			Agents map[string]any `json:"agents"`
		}
		if err := c.Do(ctx, "GET", "/v1/agents", nil, &res); err != nil {
			return e.fail(err)
		}
		if b, _ := json.Marshal(res); json.Unmarshal(b, &l) == nil && len(l.Agents) == 0 {
			fmt.Fprintln(e.Stderr, "gate4ai: no agents yet: add them under agents: in the policy (gate4ai policy show, then gate4ai policy set config.yaml)")
		}
		e.out(res, agentsTable(res))
		return ExitOK
	case "token":
		fs := flag.NewFlagSet("token", flag.ContinueOnError)
		label := fs.String("label", "", "")
		name, rest := firstPositional(args)
		if fs.Parse(rest) != nil || name == "" {
			fmt.Fprintln(e.Stderr, "usage: gate4ai agents token <agent> [--label l]")
			return ExitUsage
		}
		return e.call(ctx, c, "POST", "/v1/agents/"+name+"/token", map[string]string{"label": *label})
	case "connect":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, "usage: gate4ai agents connect <agent>   (one-time code for a remote MCP client)")
			return ExitUsage
		}
		return e.call(ctx, c, "POST", "/v1/agents/"+args[0]+"/connect", nil)
	case "revoke":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, "usage: gate4ai agents revoke <agent>")
			return ExitUsage
		}
		return e.call(ctx, c, "POST", "/v1/agents/"+args[0]+"/revoke", nil)
	default:
		fmt.Fprintln(e.Stderr, "usage: gate4ai agents list | token <agent> [--label l] | connect <agent> | revoke <agent>")
		return ExitUsage
	}
}

func (e *Env) policy(ctx context.Context, args []string) int {
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	c := e.Client()
	switch sub {
	case "show":
		var res struct {
			Version int    `json:"version"`
			YAML    string `json:"yaml"`
		}
		if err := c.Do(ctx, "GET", "/v1/policy", nil, &res); err != nil {
			return e.fail(err)
		}
		fmt.Fprintf(e.Stdout, "# version %d\n%s", res.Version, res.YAML)
		return ExitOK
	case "set":
		fs := flag.NewFlagSet("set", flag.ContinueOnError)
		msg := fs.String("m", "", "comment")
		file, rest := firstPositional(args)
		if fs.Parse(rest) != nil || file == "" {
			fmt.Fprintln(e.Stderr, "usage: gate4ai policy set <file.yaml> [-m comment]")
			return ExitUsage
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return e.fail(err)
		}
		return e.call(ctx, c, "PUT", "/v1/policy", map[string]string{"yaml": string(b), "comment": *msg})
	case "versions":
		var res any
		if err := c.Do(ctx, "GET", "/v1/policy/versions", nil, &res); err != nil {
			return e.fail(err)
		}
		e.out(res, versionsTable(res))
		return ExitOK
	case "rollback":
		n, err := strconv.Atoi(strings.Join(args, ""))
		if err != nil {
			fmt.Fprintln(e.Stderr, "usage: gate4ai policy rollback <version>")
			return ExitUsage
		}
		return e.call(ctx, c, "POST", "/v1/policy/rollback", map[string]int{"version": n})
	case "test":
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		agent := fs.String("agent", "", "")
		action := fs.String("action", "", "")
		argsJSON := fs.String("args", "{}", "")
		file := fs.String("file", "", "draft policy to test instead of the current one")
		if fs.Parse(args) != nil || *agent == "" || *action == "" {
			fmt.Fprintln(e.Stderr, "usage: gate4ai policy test --agent a --action email.send [--args '{...}'] [--file draft.yaml]")
			return ExitUsage
		}
		var a map[string]any
		if err := json.Unmarshal([]byte(*argsJSON), &a); err != nil {
			return e.fail(fmt.Errorf("--args: %v", err))
		}
		body := map[string]any{"agent": *agent, "action": *action, "args": a}
		if *file != "" {
			b, err := os.ReadFile(*file)
			if err != nil {
				return e.fail(err)
			}
			body["yaml"] = string(b)
		}
		return e.call(ctx, c, "POST", "/v1/policy/test", body)
	}
	fmt.Fprintln(e.Stderr, "usage: gate4ai policy show | set | versions | rollback | test")
	return ExitUsage
}

func (e *Env) approvals(ctx context.Context, args []string) int {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	c := e.Client()
	switch sub {
	case "list":
		table := false
		for _, a := range args {
			if a != "--table" && a != "-table" {
				fmt.Fprintln(e.Stderr, "usage: gate4ai approvals [list] [--table] | approve <id> | reject <id>")
				return ExitUsage
			}
			table = true
		}
		var res []map[string]any
		if err := c.Do(ctx, "GET", "/v1/approvals?status=pending", nil, &res); err != nil {
			return e.fail(err)
		}
		if len(res) == 0 {
			fmt.Fprintln(e.Stderr, "gate4ai: no approvals are waiting")
		}
		e.ForceTable = e.ForceTable || table
		e.out(res, approvalsTable(res))
		return ExitOK
	case "approve", "reject":
		if len(args) != 1 {
			fmt.Fprintf(e.Stderr, "usage: gate4ai approvals %s <id>\n", sub)
			return ExitUsage
		}
		var res map[string]any
		if err := c.Do(ctx, "POST", "/v1/approvals/"+args[0]+"/decide", map[string]any{"approve": sub == "approve", "channel": "cli"}, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	}
	switch sub {
	case "questions":
		// Вопросы с вводом: код из SMS для входа на сайт (browser.fill_secret, source: human).
		var res []map[string]any
		if err := c.Do(ctx, "GET", "/v1/questions", nil, &res); err != nil {
			return e.fail(err)
		}
		if len(res) == 0 {
			fmt.Fprintln(e.Stderr, "gate4ai: no questions are waiting")
		}
		e.printJSON(res)
		return ExitOK
	case "answer":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, "usage: echo -n CODE | gate4ai approvals answer <id>   (empty — decline)")
			return ExitUsage
		}
		b, err := io.ReadAll(io.LimitReader(e.Stdin, 4096))
		if err != nil {
			return e.fail(err)
		}
		if err := c.Do(ctx, "POST", "/v1/questions/"+args[0]+"/answer", map[string]string{"value": strings.TrimRight(string(b), "\r\n")}, nil); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok"})
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, "usage: gate4ai approvals [list] | approve <id> | reject <id> | questions | answer <id>")
	return ExitUsage
}

func (e *Env) vault(ctx context.Context, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	c := e.Client()
	switch sub {
	case "list":
		var res []string
		if err := c.Do(ctx, "GET", "/v1/vault", nil, &res); err != nil {
			return e.fail(err)
		}
		sort.Strings(res)
		if len(res) == 0 {
			fmt.Fprintln(e.Stderr, "gate4ai: the vault is empty: echo -n SECRET | gate4ai vault set <alias>")
		}
		e.out(res, func(w io.Writer) {
			for _, a := range res {
				fmt.Fprintln(w, "@"+strings.TrimPrefix(a, "@"))
			}
		})
		return ExitOK
	case "set":
		pos, fl := splitFlags(args, map[string]bool{"from-env": true})
		fs := flag.NewFlagSet("vault set", flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		fromEnv := fs.String("from-env", "", "take the value from this environment variable (scripts)")
		if fs.Parse(fl) != nil || len(pos) != 1 {
			_, _ = io.WriteString(e.Stderr, vaultSetUsage+"\n")
			return ExitUsage
		}
		val, err := e.readSecret(pos[0], *fromEnv)
		if err != nil {
			return e.fail(err)
		}
		args = pos
		if err := c.Do(ctx, "PUT", "/v1/vault/"+args[0], map[string]string{"value": val}, nil); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok", "alias": "@" + strings.TrimPrefix(args[0], "@")})
		return ExitOK
	case "rm":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, "usage: gate4ai vault rm <alias>")
			return ExitUsage
		}
		if err := c.Do(ctx, "DELETE", "/v1/vault/"+args[0], nil, nil); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok"})
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, "usage: gate4ai vault list | set <alias> | rm <alias>")
	return ExitUsage
}

func (e *Env) relay(ctx context.Context, args []string) int {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "status":
		var st map[string]any
		if err := e.Client().Do(ctx, "GET", "/v1/relay/status", nil, &st); err != nil {
			return e.fail(err)
		}
		e.out(st, kvTable(st))
		return ExitOK
	case "register":
		fs := flag.NewFlagSet("register", flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		u := fs.String("url", "https://relay.gate4.ai", "relay URL")
		ca := fs.String("ca-file", "", "CA of a self-hosted relay (PEM)")
		self := fs.Bool("self-signed", false, "development: device certificate without ACME")
		if fs.Parse(args) != nil {
			return ExitUsage
		}
		return e.call(ctx, e.Client(), "POST", "/v1/relay/register", map[string]any{"url": *u, "ca_file": *ca, "self_signed": *self})
	}
	fmt.Fprintln(e.Stderr, "usage: gate4ai relay register --url https://relay.gate4.ai | status")
	return ExitUsage
}

func (e *Env) ui(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("gate4ai ui", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	printOnly := fs.Bool("print", false, "only print the login link")
	remote := fs.Bool("remote", false, "link for the public address (relay or --public), to open on another device")
	start := fs.Bool("start", false, "start the daemon in the background if it is not running (default when you are at a terminal)")
	noStart := fs.Bool("no-start", false, "never start the daemon")
	if fs.Parse(args) != nil {
		return ExitUsage
	}
	var res struct {
		URL          string   `json:"url"`
		Requires     []string `json:"requires"`
		LinkRequired *bool    `json:"link_required"`
	}
	c := e.Client()
	session := func() error {
		return c.Do(ctx, "POST", "/v1/ui/session", map[string]bool{"remote": *remote}, &res)
	}
	err := session()
	if NotRunning(err) && e.Getenv("GATE4AI_TOKEN") == "" && !*noStart && (*start || e.Interactive && !*printOnly) {
		if err := e.startDaemon(ctx, nil); err != nil {
			return e.fail(err)
		}
		err = session()
	}
	if err != nil {
		return e.fail(err)
	}
	if !*printOnly && !*remote {
		if headless() {
			fmt.Fprintln(e.Stderr, "gate4ai: no graphical session here, so no browser was opened. Open the link in a browser on this computer, or forward the port: "+sshHint(res.URL)+"; from another device: gate4ai ui --remote (needs relay or --public)")
		} else if err := OpenLoginURL(res.URL); err != nil {
			fmt.Fprintln(e.Stderr, "gate4ai: could not open a browser ("+err.Error()+"): open the link yourself")
		}
	}
	e.say("One-time login link, valid for 2 minutes:")
	fmt.Fprintln(e.Stdout, res.URL)
	if len(res.Requires) > 0 {
		need := map[string]string{"password": "the Web UI password", "telegram": "confirmation in your Telegram bot"}
		var parts []string
		for _, r := range res.Requires {
			if need[r] == "" {
				need[r] = r
			}
			parts = append(parts, need[r])
		}
		if res.LinkRequired != nil && !*res.LinkRequired {
			if pu, err := url.Parse(res.URL); err == nil {
				fmt.Fprintln(e.Stderr, "gate4ai: no link needed: open "+pu.Scheme+"://"+pu.Host+"/ and log in with "+strings.Join(parts, " and "))
			}
		} else {
			fmt.Fprintln(e.Stderr, "gate4ai: login protection is on: the link alone does not log in, it also needs "+strings.Join(parts, " and "))
		}
	}
	return ExitOK
}

// NotRunning — daemon не запущен (нет сокета или остался старый): его можно запустить.
func NotRunning(err error) bool {
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Remote {
		return false
	}
	if max := ipc.MaxPath(); max > 0 && len(ue.Addr) > max {
		return false
	}
	return errors.Is(ue.Err, os.ErrNotExist) || errors.Is(ue.Err, syscall.ECONNREFUSED)
}

// headless — Linux без графического сеанса (ssh, сервер): браузер не откроется.
func headless() bool {
	return runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

// sshHint — проброс порта Web UI с этого компьютера.
func sshHint(link string) string {
	port := "7431"
	if u, err := url.Parse(link); err == nil && u.Port() != "" {
		port = u.Port()
	}
	return "ssh -L " + port + ":127.0.0.1:" + port + " <this computer>, then open the link"
}
