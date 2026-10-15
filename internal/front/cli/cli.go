// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/front/skill"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/sdk-go/manifest"
)

// Env — окружение CLI (подменяется в тестах).
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	// Connect возвращает клиента: с токеном агента — к сокету агентов, без — к сокету владельца.
	Connect func(token string) *Client
	// Extra — дополнительные команды (mcp, run, doctor, ui), регистрируются в cmd/gate4ai.
	Extra map[string]func(ctx context.Context, e *Env, args []string) int
}

// DefaultEnv — реальное окружение процесса.
func DefaultEnv() *Env {
	return &Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv,
		Connect: func(token string) *Client {
			if token != "" {
				// Удалённый агент: daemon на другом компьютере, доступ по HTTPS.
				if base := os.Getenv("GATE4AI_URL"); base != "" {
					c, err := NewRemoteClient(base, token, os.Getenv("GATE4AI_CA_FILE"))
					if err != nil {
						return &Client{HTTP: &http.Client{Transport: failing{err}}, Token: token}
					}
					return c
				}
				return NewSocketClient(paths.AgentSocket(), token)
			}
			return NewSocketClient(paths.OwnerSocket(), "")
		}, Extra: map[string]func(context.Context, *Env, []string) int{}}
}

// failing — транспорт, который возвращает ошибку настройки (неверный GATE4AI_URL).
type failing struct{ err error }

func (f failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// Client — клиент к daemon с токеном из GATE4AI_TOKEN.
func (e *Env) Client() *Client { return e.Connect(e.Getenv("GATE4AI_TOKEN")) }

func (e *Env) printJSON(v any) {
	enc := json.NewEncoder(e.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// fail печатает ошибку JSON-ом и возвращает код выхода.
func (e *Env) fail(err error) int {
	code := ExitError
	c := "error"
	var ae *APIError
	switch {
	case errors.Is(err, ErrUnavailable):
		code, c = ExitUnavailable, "unavailable"
	case errors.As(err, &ae):
		c = ae.Code
		if ae.Status == 400 || ae.Status == 404 {
			code = ExitUsage
		}
	}
	e.printJSON(map[string]any{"status": "error", "code": c, "message": err.Error()})
	return code
}

const usage = `gate4ai — actions for AI agents under your policies

Agent commands (token in GATE4AI_TOKEN; daemon on another computer: GATE4AI_URL=https://<device address>):
  gate4ai tools [--json]                 actions available to this agent
  gate4ai <connector> <action> [--arg v] run an action (see --help on the action)
  gate4ai status <id> [--wait 10m]       result of an action pending approval
  gate4ai skill                          print SKILL.md for this agent
  gate4ai mcp                            MCP server on stdio

Daemon:
  gate4ai daemon [--config file.yaml] [--home dir] [--keystore auto|keychain|file] [--ui addr]

Owner commands (local socket):
  gate4ai log [--agent a] [--action x] [--effect deny] [--since 24h] [--limit 50]
  gate4ai show <#hash|id>                one receipt
  gate4ai export -o file.tar.gz [--passphrase-env VAR]
  gate4ai verify file.tar.gz [--pubkey hex] [--passphrase-env VAR]   (offline)
  gate4ai journal verify
  gate4ai agents list | token <agent> [--label l] | connect <agent> | revoke <agent>
  gate4ai policy show | set <file> [-m comment] | versions | rollback <n> | test --agent a --action x [--args json] [--file draft.yaml]
  gate4ai approvals [list] | approve <id> | reject <id>
  gate4ai vault list | set <alias> (value from stdin) | rm <alias>
  gate4ai relay register --url https://relay.gate4.ai | status
  gate4ai connectors list | install <file.g4c | name[@version]> [--dev] [--yes] | update <name> [--yes] | remove <name> | refresh
  gate4ai connectors pack <dir> [--wasm file] [--key file] [-o file.g4c] | verify <dir> <file.g4c> | keygen [-o key]   (publisher / registry review, offline)
  gate4ai registry check <index.json> [--key ed25519:…] [--fetch] | sign <index.json> --key file | add <index.json> <pack.json> --publisher name …   (registry)
  gate4ai ui [--print] [--remote]        open the web UI (one-time login link; --remote: link for the public address)
  gate4ai run <agent> [--workspace dir] [--container|--microvm] [--dry-run] [-- cmd]   isolated agent
  gate4ai doctor                         look for ways around gate4ai
  gate4ai egress                         network connections of sandboxed agents
`

// Run — точка входа CLI.
func Run(ctx context.Context, e *Env, args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(e.Stdout, usage)
		return ExitOK
	}
	cmd, rest := args[0], args[1:]
	if f, ok := e.Extra[cmd]; ok {
		return f(ctx, e, rest)
	}
	switch cmd {
	case "version", "--version":
		fmt.Fprintln(e.Stdout, "gate4ai", version.Version)
		return ExitOK
	case "tools":
		return e.tools(ctx, rest)
	case "status":
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
	case "ui":
		return e.ui(ctx, rest)
	case "run":
		return e.run(ctx, rest)
	case "doctor":
		return e.doctor(ctx)
	case "egress":
		var res any
		if err := e.Client().Do(ctx, "GET", "/v1/egress/log?limit=200", nil, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	}
	return e.action(ctx, cmd, rest)
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

// connectorAliases — привычные имена коннекторов в CLI.
var connectorAliases = map[string]string{"mail": "email"}

// action — `gate4ai <connector> <action> --arg value ...`.
func (e *Env) action(ctx context.Context, conn string, args []string) int {
	if a, ok := connectorAliases[conn]; ok {
		conn = a
	}
	tools, err := e.Tools(ctx, "")
	if err != nil {
		return e.fail(err)
	}
	var mine []skill.Tool
	for _, t := range tools {
		if t.Connector == conn {
			mine = append(mine, t)
		}
	}
	if len(mine) == 0 {
		e.printJSON(map[string]any{"status": "error", "code": "unknown_action", "message": fmt.Sprintf("unknown command or connector %q; run `gate4ai tools`", conn)})
		return ExitUsage
	}
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
		e.printJSON(map[string]any{"status": "error", "code": "unknown_action", "message": fmt.Sprintf("unknown action %s.%s", conn, args[0])})
		return ExitUsage
	}
	vals, opts, err := parseArgs(*tool, args[1:])
	if err != nil {
		e.printJSON(map[string]any{"status": "error", "code": "invalid_arguments", "message": err.Error()})
		return ExitUsage
	}
	if opts.help {
		e.actionHelp(*tool)
		return ExitOK
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
	fmt.Fprintf(e.Stdout, "\nOptions:\n  --wait 10m        wait for approval if needed\n  --json '{...}'    all arguments as a JSON object\n  --as <agent>      owner only: run with this agent's profile\n\nExample:\n  %s\n", skill.Example(t))
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

type callOpts struct {
	wait string
	as   string
	help bool
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
		switch name {
		case "wait", "as":
			v, err := next()
			if err != nil {
				return nil, o, err
			}
			if name == "wait" {
				o.wait = v
			} else {
				o.as = v
			}
			continue
		case "json":
			v, err := next()
			if err != nil {
				return nil, o, err
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(v), &m); err != nil {
				return nil, o, fmt.Errorf("--json: %v", err)
			}
			for k, x := range m {
				vals[k] = x
			}
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
	asJSON := fs.Bool("json", false, "")
	if fs.Parse(args) != nil {
		return ExitUsage
	}
	q := fmt.Sprintf("/v1/log?agent=%s&action=%s&effect=%s&since=%s&limit=%d", *agent, *action, *effect, *since, *limit)
	var es []audit.Entry
	if err := e.Client().Do(ctx, "GET", q, nil, &es); err != nil {
		return e.fail(err)
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
	var res any
	var err error
	switch sub {
	case "list":
		err = c.Do(ctx, "GET", "/v1/agents", nil, &res)
	case "token":
		fs := flag.NewFlagSet("token", flag.ContinueOnError)
		label := fs.String("label", "", "")
		name, rest := firstPositional(args)
		if fs.Parse(rest) != nil || name == "" {
			fmt.Fprintln(e.Stderr, "usage: gate4ai agents token <agent> [--label l]")
			return ExitUsage
		}
		err = c.Do(ctx, "POST", "/v1/agents/"+name+"/token", map[string]string{"label": *label}, &res)
	case "connect":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, "usage: gate4ai agents connect <agent>   (one-time code for a remote MCP client)")
			return ExitUsage
		}
		err = c.Do(ctx, "POST", "/v1/agents/"+args[0]+"/connect", nil, &res)
	case "revoke":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, "usage: gate4ai agents revoke <agent>")
			return ExitUsage
		}
		err = c.Do(ctx, "POST", "/v1/agents/"+args[0]+"/revoke", nil, &res)
	default:
		fmt.Fprintln(e.Stderr, "usage: gate4ai agents list | token <agent> | revoke <agent>")
		return ExitUsage
	}
	if err != nil {
		return e.fail(err)
	}
	e.printJSON(res)
	return ExitOK
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
		var res any
		if err := c.Do(ctx, "PUT", "/v1/policy", map[string]string{"yaml": string(b), "comment": *msg}, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	case "versions":
		var res any
		if err := c.Do(ctx, "GET", "/v1/policy/versions", nil, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	case "rollback":
		n, err := strconv.Atoi(strings.Join(args, ""))
		if err != nil {
			fmt.Fprintln(e.Stderr, "usage: gate4ai policy rollback <version>")
			return ExitUsage
		}
		var res any
		if err := c.Do(ctx, "POST", "/v1/policy/rollback", map[string]int{"version": n}, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
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
		var res any
		if err := c.Do(ctx, "POST", "/v1/policy/test", body, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
		return ExitOK
	}
	fmt.Fprintln(e.Stderr, "usage: gate4ai policy show | set | versions | rollback | test")
	return ExitUsage
}

func (e *Env) approvals(ctx context.Context, args []string) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	c := e.Client()
	switch sub {
	case "list":
		var res []map[string]any
		if err := c.Do(ctx, "GET", "/v1/approvals?status=pending", nil, &res); err != nil {
			return e.fail(err)
		}
		e.printJSON(res)
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
	fmt.Fprintln(e.Stderr, "usage: gate4ai approvals [list] | approve <id> | reject <id>")
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
		e.printJSON(res)
		return ExitOK
	case "set":
		if len(args) != 1 {
			fmt.Fprintln(e.Stderr, "usage: echo -n SECRET | gate4ai vault set <alias>")
			return ExitUsage
		}
		b, err := io.ReadAll(io.LimitReader(e.Stdin, 1<<20))
		if err != nil {
			return e.fail(err)
		}
		val := strings.TrimRight(string(b), "\r\n")
		if err := c.Do(ctx, "PUT", "/v1/vault/"+args[0], map[string]string{"value": val}, nil); err != nil {
			return e.fail(err)
		}
		e.printJSON(map[string]any{"status": "ok", "alias": "@" + strings.TrimPrefix(args[0], "@")})
		return ExitOK
	case "rm":
		if len(args) != 1 {
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
	var res any
	switch sub {
	case "status":
		if err := e.Client().Do(ctx, "GET", "/v1/relay/status", nil, &res); err != nil {
			return e.fail(err)
		}
	case "register":
		fs := flag.NewFlagSet("register", flag.ContinueOnError)
		fs.SetOutput(e.Stderr)
		u := fs.String("url", "https://relay.gate4.ai", "relay URL")
		ca := fs.String("ca-file", "", "CA of a self-hosted relay (PEM)")
		self := fs.Bool("self-signed", false, "development: device certificate without ACME")
		if fs.Parse(args) != nil {
			return ExitUsage
		}
		if err := e.Client().Do(ctx, "POST", "/v1/relay/register", map[string]any{"url": *u, "ca_file": *ca, "self_signed": *self}, &res); err != nil {
			return e.fail(err)
		}
	default:
		fmt.Fprintln(e.Stderr, "usage: gate4ai relay register --url https://relay.gate4.ai | status")
		return ExitUsage
	}
	e.printJSON(res)
	return ExitOK
}

// OpenURL открывает ссылку в браузере (подменяется в тестах).
var OpenURL = func(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func (e *Env) ui(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	printOnly := fs.Bool("print", false, "only print the login link")
	remote := fs.Bool("remote", false, "link for the public address (relay or --public), to open on another device")
	if fs.Parse(args) != nil {
		return ExitUsage
	}
	var res struct {
		URL string `json:"url"`
	}
	if err := e.Client().Do(ctx, "POST", "/v1/ui/session", map[string]bool{"remote": *remote}, &res); err != nil {
		return e.fail(err)
	}
	if !*printOnly && !*remote {
		if err := OpenURL(res.URL); err != nil {
			fmt.Fprintln(e.Stderr, "could not open a browser:", err)
		}
	}
	fmt.Fprintln(e.Stdout, res.URL)
	return ExitOK
}
