// SPDX-License-Identifier: FSL-1.1-ALv2

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// emptyConfig — первая версия конфигурации, если не передан --config.
const emptyConfig = "# gate4ai: no agents yet. Install connectors, then: gate4ai policy set config.yaml\nprofiles:\n  default: { connectors: [] }\n"

// runDaemon — `gate4ai daemon`: политики, подтверждения, секреты, журнал.
func runDaemon(args []string) int {
	if err := daemonMain(args); err != nil {
		fmt.Fprintln(os.Stderr, "gate4ai daemon:", err)
		return 1
	}
	return 0
}

func daemonMain(args []string) error {
	flag := flag.NewFlagSet("gate4ai daemon", flag.ContinueOnError)
	home := flag.String("home", paths.Home(), "data directory")
	cfgFile := flag.String("config", "", "initial configuration (YAML) if the config store is empty (default: empty policy, no agents)")
	keystore := flag.String("keystore", "auto", "key store: keychain | file | auto")
	ownerSock := flag.String("socket", paths.OwnerSocket(), "owner socket / named pipe")
	agentSock := flag.String("agent-socket", paths.AgentSocket(), "agent socket / named pipe")
	connDir := flag.String("connectors", "", "installed connectors directory (default <home>/connectors)")
	showVersion := flag.Bool("version", false, "print version")
	flag.StringVar(&uiAddr, "ui", uiAddr, "web UI address (loopback only; empty disables)")
	flag.StringVar(&telegramAPI, "telegram-api", telegramAPI, "Telegram Bot API base URL")
	flag.StringVar(&public.addr, "public", "", "public address without relay: listen here (HTTPS with --public-cert/--public-key, or loopback HTTP behind a reverse proxy)")
	flag.StringVar(&public.url, "public-url", "", "external URL of --public, e.g. https://gate.example.com")
	flag.StringVar(&public.cert, "public-cert", "", "TLS certificate (PEM) for --public")
	flag.StringVar(&public.key, "public-key", "", "TLS key (PEM) for --public")
	flag.StringVar(&egressAddr, "egress", egressAddr, "egress proxy address for sandboxed agents (authenticated per run)")
	if err := flag.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("gate4ai", version.Version)
		return nil
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ks, err := openKeys(*keystore, *home, log)
	if err != nil {
		return err
	}
	// Без --config пустое хранилище получает пустую политику: агентов нет, значит
	// агентам ничего не разрешено. Правила задаются после установки коннекторов
	// (gate4ai connectors install, затем gate4ai policy set).
	initial := []byte(emptyConfig)
	if *cfgFile != "" {
		if initial, err = os.ReadFile(*cfgFile); err != nil {
			return err
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	d, err := daemon.New(daemon.Options{Home: *home, InitialConfig: initial, Keys: ks, ConnectorsDir: *connDir, Log: log})
	if err != nil {
		return err
	}
	defer d.Close()
	if err := setup(ctx, d, log); err != nil {
		return err
	}
	return d.Serve(ctx, *ownerSock, *agentSock)
}

func openKeys(kind, home string, log *slog.Logger) (keys.Store, error) {
	file := keys.NewFile(filepath.Join(home, "keys.json"))
	switch kind {
	case "keychain":
		return keys.NewKeyring(), nil
	case "file":
		log.Warn("using file key store: secrets are protected only by file permissions", "path", filepath.Join(home, "keys.json"))
		return file, nil
	case "auto":
		k := keys.NewKeyring()
		if err := keys.Probe(k); err != nil {
			log.Warn("system keychain unavailable, falling back to file key store", "err", err)
			return file, nil
		}
		return k, nil
	}
	return nil, fmt.Errorf("unknown keystore %q", kind)
}
