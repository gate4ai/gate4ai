// SPDX-License-Identifier: AGPL-3.0-only

// Команда gate4ai — единственный бинарник ядра gate4.ai: daemon (`gate4ai daemon`),
// MCP-сервер для агентов (`gate4ai mcp`) и CLI для агентов, скриптов и людей.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/gate4ai/gate4ai/internal/front/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	env := cli.DefaultEnv()
	if len(os.Args) >= 2 && os.Args[1] == "daemon" {
		args := os.Args[2:]
		switch {
		case len(args) > 0 && (args[0] == "start" || args[0] == "stop" || args[0] == "restart" || args[0] == "status"):
			code := cli.DaemonCtl(ctx, env, args[0], args[1:])
			cancel()
			os.Exit(code)
		case len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "-help" || args[0] == "help"):
			fmt.Print(cli.DaemonUsage)
			os.Exit(0)
		}
		cancel()
		os.Exit(runDaemon(args, env.StderrTTY))
	}
	register(env)
	code := cli.Run(ctx, env, os.Args[1:])
	cancel()
	os.Exit(code)
}
