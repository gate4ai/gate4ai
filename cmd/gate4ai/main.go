// SPDX-License-Identifier: FSL-1.1-ALv2

// Команда gate4ai — единственный бинарник ядра gate4.ai: daemon (`gate4ai daemon`),
// MCP-сервер для агентов (`gate4ai mcp`) и CLI для агентов, скриптов и людей.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/gate4ai/gate4ai/internal/front/cli"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "daemon" {
		os.Exit(runDaemon(os.Args[2:]))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	env := cli.DefaultEnv()
	register(env)
	code := cli.Run(ctx, env, os.Args[1:])
	cancel()
	os.Exit(code)
}
