// SPDX-License-Identifier: FSL-1.1-ALv2

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gate4ai/gate4ai/internal/sandbox"
)

// Exec запускает подготовленную команду (подменяется в тестах).
var Exec = func(ctx context.Context, e *Env, argv, env []string) (int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = e.Stdin, e.Stdout, e.Stderr
	cmd.Env = env
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 127, err
	}
	return 0, nil
}

var secretEnvRe = regexp.MustCompile(`(?i)(TOKEN|API_?KEY|SECRET|PASSWORD|PASSWD|CREDENTIAL|PRIVATE_KEY|ACCESS_KEY)`)

// cleanEnv — окружение хоста без секретов (агенту они не нужны: ключи
// подставляет Egress Proxy, действия идут через gate4ai).
func cleanEnv(environ, extra []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if secretEnvRe.MatchString(k) || strings.HasPrefix(k, "GATE4AI_") || strings.EqualFold(k, "HTTP_PROXY") || strings.EqualFold(k, "HTTPS_PROXY") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

func (e *Env) run(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	ws := fs.String("workspace", ".", "the only host folder the agent may write to")
	container := fs.Bool("container", false, "level 2: Docker/Podman container")
	microvm := fs.Bool("microvm", false, "level 3: microVM via Docker Sandboxes")
	image := fs.String("image", "", "container image (default from the agent template)")
	engine := fs.String("engine", "docker", "docker | podman")
	dry := fs.Bool("dry-run", false, "print the command instead of running it")
	var cmdArgs []string
	for i, a := range args {
		if a == "--" {
			cmdArgs = args[i+1:]
			args = args[:i]
			break
		}
	}
	agent, rest := firstPositional(args)
	if fs.Parse(rest) != nil || agent == "" {
		fmt.Fprintln(e.Stderr, "usage: gate4ai run <agent> [--workspace dir] [--container|--microvm] [--image img] [--dry-run] [-- command ...]")
		return ExitUsage
	}
	mode := sandbox.ModeSandbox
	if *container {
		mode = sandbox.ModeContainer
	}
	if *microvm {
		mode = sandbox.ModeMicroVM
	}
	wsAbs, err := filepath.Abs(*ws)
	if err != nil {
		return e.fail(err)
	}
	c := e.Client()
	var run sandbox.Run
	if err := c.Do(ctx, "POST", "/v1/runs", map[string]string{"agent": agent}, &run); err != nil {
		return e.fail(err)
	}
	defer c.Do(context.Background(), "DELETE", "/v1/runs/"+run.ID, nil, nil)
	tmp, err := os.MkdirTemp("", "gate4ai-run-")
	if err != nil {
		return e.fail(err)
	}
	defer os.RemoveAll(tmp)
	gate, _ := os.Executable()
	home, _ := os.UserHomeDir()
	spec, err := sandbox.Build(run, sandbox.Options{Mode: mode, Workspace: wsAbs, Command: cmdArgs, Image: *image, Engine: *engine,
		GateBin: gate, Home: home, TmpDir: tmp})
	if err != nil {
		return e.fail(err)
	}
	if *dry {
		mask := func(s string) string { return strings.ReplaceAll(s, run.Token, "***") }
		var argv, env []string
		for _, a := range spec.Argv {
			argv = append(argv, mask(a))
		}
		for _, a := range spec.Env {
			env = append(env, mask(a))
		}
		files := map[string]string{}
		for p, body := range spec.Files {
			files[p] = mask(body)
		}
		e.printJSON(map[string]any{"run": run.ID, "agent": agent, "mode": mode, "argv": argv, "env": env, "files": files})
		return ExitOK
	}
	for p, body := range spec.Files {
		if strings.HasSuffix(p, ".mcp.json") {
			if _, err := os.Stat(p); err == nil {
				fmt.Fprintf(e.Stderr, "gate4ai: %s exists, not overwritten; add the gate4ai server (command: gate4ai, args: [mcp]) yourself\n", p)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return e.fail(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return e.fail(err)
		}
	}
	fmt.Fprintf(e.Stderr, "gate4ai: run %s — agent %s in %s mode, workspace %s\n", run.ID, agent, mode, wsAbs)
	code, err := Exec(ctx, e, spec.Argv, cleanEnv(os.Environ(), spec.Env))
	if err != nil {
		return e.fail(fmt.Errorf("%s: %w (is it installed? see `gate4ai doctor`)", spec.Argv[0], err))
	}
	return code
}

func (e *Env) doctor(ctx context.Context) int {
	checks := sandbox.Doctor(sandbox.RealDoctorEnv(func() error {
		var h map[string]any
		return NewSocketClientFrom(e).Do(ctx, "GET", "/v1/health", nil, &h)
	}))
	e.printJSON(checks)
	for _, c := range checks {
		if c.Status == "fail" {
			return ExitError
		}
	}
	return ExitOK
}

// NewSocketClientFrom — клиент владельца из окружения.
func NewSocketClientFrom(e *Env) *Client { return e.Connect("") }
