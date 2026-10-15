// SPDX-License-Identifier: FSL-1.1-ALv2

package main

import (
	"context"
	"fmt"

	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/mcp"
	"github.com/gate4ai/gate4ai/internal/front/mcpcli"
)

// register подключает команду mcp (MCP-сервер живёт вне пакета cli).
func register(env *cli.Env) {
	env.Extra["mcp"] = func(ctx context.Context, e *cli.Env, args []string) int {
		if e.Getenv("GATE4AI_TOKEN") == "" {
			fmt.Fprintln(e.Stderr, "gate4ai mcp: set GATE4AI_TOKEN to the agent token (gate4ai agents token <agent>)")
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
