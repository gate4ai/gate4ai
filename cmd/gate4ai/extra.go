// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"

	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/mcp"
	"github.com/gate4ai/gate4ai/internal/front/mcpcli"
	"github.com/gate4ai/gate4ai/internal/sandbox"
)

// register подключает команду mcp (MCP-сервер живёт вне пакета cli) и
// скрытую точку входа контейнера `gate4ai run --container` (sandbox.Shim).
func register(env *cli.Env) {
	env.Extra[sandbox.ShimCommand] = func(ctx context.Context, e *cli.Env, args []string) int {
		return sandbox.Shim(ctx, args, e.Stdin, e.Stdout, e.Stderr)
	}
	env.Extra["mcp"] = func(ctx context.Context, e *cli.Env, args []string) int {
		if !e.AgentMode() {
			fmt.Fprintln(e.Stderr, "gate4ai mcp: name the agent: GATE4AI_AGENT=<agent> in the server's env (it connects itself: the first call shows a code to approve in the Web UI), or GATE4AI_TOKEN=<token>")
			return cli.ExitUsage
		}
		c := e.Client()
		c.Kind = "mcp"
		srv := mcp.NewServer(mcpcli.ClientBackend{C: c})
		if err := srv.ServeStdio(ctx, e.Stdin, e.Stdout); err != nil {
			fmt.Fprintln(e.Stderr, "gate4ai mcp:", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
}
