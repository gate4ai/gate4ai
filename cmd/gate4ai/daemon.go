// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// emptyConfig — первая версия конфигурации, если не передан --config: агентов
// нет, официальный магазин коннекторов подключён, встроенный коннектор files
// (корень ~/Documents; скрытое и ключи закрыты им самим) виден в профиле
// default с правилом ask на каждое действие. Каждый
// установленный из магазина коннектор daemon добавляет туда же с ask.
const emptyConfig = `# gate4ai: no agents yet. Files are built in, limited to ~/Documents (every action
# asks you first; hidden files and keys are always closed);
# the browser connector is installed from the store and added to profile default
# the same way. Add agents and your rules (see the README), then: gate4ai policy set config.yaml
store:
  registry: https://raw.githubusercontent.com/gate4ai/registry/main/index.json
  registry_keys: ["ed25519:yDcbQuNCGqSqHSt08HdvW5/fNyWRW8FIXtKiL91hqtw="]
connectors:
  files: { roots: ["~/Documents"] }
profiles:
  default:
    connectors: [files]
    rules:
      - { id: files-read-ask, action: files.read, effect: ask }
      - { id: files-list-ask, action: files.list, effect: ask }
      - { id: files-append-ask, action: files.append, effect: ask }
      - { id: files-write-ask, action: files.write, effect: ask }
`

// defaultConnectors — коннекторы, которые свежая установка без --config ставит
// из магазина сама (с согласием на возможности, пока магазин доступен — в фоне).
var defaultConnectors = []string{"browser"}

// runDaemon — `gate4ai daemon`: политики, подтверждения, секреты, журнал.
func runDaemon(args []string, tty bool) int {
	if err := daemonMain(args, tty); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "gate4ai daemon:", err)
		return 1
	}
	return 0
}

// daemonMain; tty — stderr — терминал: человеку печатается, что делать дальше.
func daemonMain(args []string, tty bool) error {
	flag := flag.NewFlagSet("gate4ai daemon", flag.ContinueOnError)
	home := flag.String("home", paths.Home(), "data directory")
	cfgFile := flag.String("config", "", "initial configuration (YAML) if the config store is empty (default: empty policy, no agents)")
	keystore := flag.String("keystore", "auto", "key store: keychain | file | auto")
	ownerSock := flag.String("socket", paths.OwnerSocket(), "owner socket / named pipe")
	agentSock := flag.String("agent-socket", paths.AgentSocket(), "agent socket / named pipe")
	connDir := flag.String("connectors", "", "installed connectors directory (default <home>/connectors)")
	showVersion := flag.Bool("version", false, "print version")
	flag.StringVar(&uiAddr, "ui", uiAddr, "web UI address (loopback only; empty disables)")
	flag.BoolVar(&resetUILogin, "reset-ui-login", false, "remove the Web UI login protection (password, Telegram confirmation) if you are locked out")
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
	// Второй экземпляр и слишком длинный путь сокета — сразу, до Web UI и relay:
	// иначе первой падала бы занятость порта Web UI.
	for _, p := range []string{*ownerSock, *agentSock} {
		if err := ipc.CheckPath(p); err != nil {
			return err
		}
	}
	if ipc.Alive(*ownerSock) {
		return fmt.Errorf("already running (socket %s): open the Web UI with gate4ai ui, stop it with gate4ai daemon stop", *ownerSock)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ks, err := openKeys(*keystore, *home, log)
	if err != nil {
		return err
	}
	// Без --config пустое хранилище получает начальную политику: агентов нет, значит
	// агентам ничего не разрешено; коннекторы по умолчанию ставятся из магазина.
	initial, defaults := []byte(emptyConfig), defaultConnectors
	if *cfgFile != "" {
		if initial, err = os.ReadFile(*cfgFile); err != nil {
			return err
		}
		defaults = nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	d, err := daemon.New(daemon.Options{Home: *home, InitialConfig: initial, DefaultConnectors: defaults, Keys: ks, ConnectorsDir: *connDir, Log: log})
	if err != nil {
		return err
	}
	defer d.Close()
	if err := setup(ctx, d, log); err != nil {
		return err
	}
	go d.InstallDefaults(ctx)
	if tty {
		go banner(ctx, *ownerSock)
	}
	return d.Serve(ctx, *ownerSock, *agentSock)
}

// banner — подсказка человеку, когда daemon начал слушать сокет.
func banner(ctx context.Context, sock string) {
	for i := 0; i < 100 && ctx.Err() == nil; i++ {
		if ipc.Alive(sock) {
			fmt.Fprint(os.Stderr, "\ngate4ai daemon is running in this terminal: keep it open (Ctrl+C stops it).\n"+
				"Open the Web UI from another terminal: gate4ai ui\n"+
				"Run it in the background instead: gate4ai daemon start; at every login: gate4ai service install\n\n")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
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
