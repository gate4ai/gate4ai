// SPDX-License-Identifier: AGPL-3.0-only

// Package daemon — ядро gate4ai (`gate4ai daemon`): Config Store, коннекторы, Policy Engine,
// Action Pipeline, журнал, State Store, Vault и локальные API по сокетам.
package daemon

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/connectors/builtin/files"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
	"github.com/gate4ai/gate4ai/internal/front/oauth"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/secrets/vault"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs"
)

// Options — параметры daemon.
type Options struct {
	Home          string
	InitialConfig []byte
	Keys          keys.Store
	// ConnectorsDir — установленные коннекторы ("" — <Home>/connectors).
	ConnectorsDir string
	// DefaultConnectors — коннекторы из магазина, которые ставятся на свежей
	// установке (хранилище конфигурации пусто): см. InstallDefaults.
	DefaultConnectors []string
	// Connectors — дополнительные коннекторы в том же процессе (тесты).
	Connectors []connectors.Client
	// UserHome — каталог для "~" в путях коннекторов ("" — домашний каталог).
	UserHome string
	// WasmDial и WasmRootCAs — сеть коннекторов (тесты).
	WasmDial    func(ctx context.Context, network, addr string) (net.Conn, error)
	WasmRootCAs *x509.CertPool
	Notifier    pipeline.Notifier
	Log         *slog.Logger
	Now         func() time.Time
}

// Daemon — собранное ядро.
type Daemon struct {
	Home     string
	Config   *config.Store
	Registry *connectors.Registry
	State    *state.Store
	Journal  *audit.Journal
	Vault    *vault.Vault
	Pipeline *pipeline.Pipeline
	Keys     keys.Store
	Log      *slog.Logger
	// ConnLogs — последние строки лога по коннектору (`gate4ai connectors logs`).
	ConnLogs *ConnectorLogs
	// Wasm — среда исполнения коннекторов; ConnectorsDir — установленные коннекторы.
	Wasm          *wasm.Engine
	ConnectorsDir string

	mu        sync.Mutex
	installMu sync.Mutex    // установка коннекторов: владелец и коннекторы по умолчанию
	stop      chan struct{} // закрывается RequestStop
	stopOnce  sync.Once
	notifiers *MultiNotifier
	questions questions
	servers   []*http.Server
	extra     []func(*http.ServeMux) // доп. маршруты владельца (MCP HTTP, Web UI)
	oauth     *oauth.Server

	// Публичный адрес устройства (relay, прямой HTTPS): маршруты по путям, адрес.
	public     []publicRoute
	publicURLs []func() string
	publicMux  http.Handler
}

// New собирает daemon.
func New(opt Options) (*Daemon, error) {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	logs := &ConnectorLogs{}
	opt.Log = slog.New(logs.Handler(opt.Log.Handler()))
	if opt.Keys == nil {
		return nil, errors.New("daemon: key store required")
	}
	if err := os.MkdirAll(opt.Home, 0o700); err != nil {
		return nil, err
	}
	d := &Daemon{Home: opt.Home, Keys: opt.Keys, Log: opt.Log, Registry: connectors.NewRegistry(), notifiers: &MultiNotifier{},
		ConnectorsDir: opt.ConnectorsDir, ConnLogs: logs}
	if d.ConnectorsDir == "" {
		d.ConnectorsDir = filepath.Join(opt.Home, "connectors")
	}
	var err error
	if d.Vault, err = vault.Open(opt.Keys); err != nil {
		return nil, err
	}
	if err := d.startWasm(opt); err != nil {
		return nil, err
	}
	// Каталог действий — из манифестов установленных пакетов (компиляция — в фоне);
	// отзыв версий проверяется после чтения конфигурации (ключи registry — в ней).
	d.retireBuiltinPackages()
	for _, c := range (store.Options{Dir: d.ConnectorsDir}).LoadAll(context.Background(), d.Wasm, d.Log) {
		d.Registry.Add(c)
	}
	if fc, err := d.Wasm.Builtin(files.Manifest(), files.Files{}); err == nil {
		d.Registry.Add(fc)
	} else {
		return nil, err
	}
	for _, c := range opt.Connectors {
		d.Registry.Add(c)
	}
	if eg, err := connectors.NewInProcess(egressConnector{}); err == nil {
		d.Registry.Add(eg)
	}
	if sc, err := connectors.NewInProcess(selfConnector{d: d}); err == nil {
		d.Registry.Add(sc)
	}
	// Каталог меняется при установке и удалении коннекторов — проверка по текущему.
	validator := func(c *config.Config) config.Errors {
		return policy.Validator(policy.Catalog(d.Registry.Catalog()))(c)
	}
	_, err = os.Stat(filepath.Join(opt.Home, "config", "current"))
	fresh := errors.Is(err, os.ErrNotExist)
	d.Config, err = config.OpenStore(filepath.Join(opt.Home, "config"), opt.InitialConfig, validator)
	if err != nil {
		return nil, err
	}
	if fresh {
		if err := d.savePendingDefaults(opt.DefaultConnectors); err != nil {
			return nil, err
		}
	}
	d.dropRevoked()
	d.AddRoutes(d.connectorRoutes)
	eng, err := policy.New(d.Config.Current(), policy.Catalog(d.Registry.Catalog()))
	if err != nil {
		return nil, err
	}
	if d.State, err = state.Open(filepath.Join(opt.Home, "state.db"), opt.Keys); err != nil {
		return nil, err
	}
	cfg := d.Config.Current()
	if d.Journal, err = audit.Open(filepath.Join(opt.Home, "journal.db"), filepath.Join(opt.Home, "evidence"), opt.Keys,
		audit.Options{CheckpointEach: cfg.Retention.CheckpointEach, Now: opt.Now}); err != nil {
		return nil, err
	}
	if opt.Notifier != nil {
		d.notifiers.Add(opt.Notifier)
	}
	d.Pipeline = pipeline.New(d.Registry, d.State, d.Journal, d.Vault, d.notifiers, eng)
	d.Pipeline.Log = opt.Log
	d.Pipeline.Describe = map[string]func(string, map[string]any) (string, error){SelfConnector: d.describeSelf}
	d.Pipeline.EndCall = d.endCall
	d.Pipeline.ConnectorLog = d.devLog
	d.Pipeline.AlwaysAsk = map[string]func(map[string]any) string{SelfConnector: alwaysAskSelf}
	d.Pipeline.Vault = d.Vault
	d.Pipeline.Ask = d.ask
	if opt.Now != nil {
		d.Pipeline.Now = opt.Now
		d.State.Now = opt.Now
	}
	d.Config.Subscribe(func(c *config.Config) {
		e, err := policy.New(c, policy.Catalog(d.Registry.Catalog()))
		if err != nil {
			d.Log.Error("policy compile after config change", "err", err)
			return
		}
		d.Pipeline.SetEngine(e)
		d.Log.Info("policy updated")
	})
	d.migrateBrowserSection()
	d.updateWasmBrowser()
	return d, nil
}

// AddNotifier подключает канал подтверждений/уведомлений.
func (d *Daemon) AddNotifier(n pipeline.Notifier) { d.notifiers.Add(n) }

// AddRoutes добавляет маршруты к API владельца (до Serve).
func (d *Daemon) AddRoutes(f func(*http.ServeMux)) { d.extra = append(d.extra, f) }

// Tick — периодические задачи: таймауты подтверждений, суточный чекпоинт,
// повтор свидетелю, сроки хранения.
func (d *Daemon) Tick(ctx context.Context) {
	d.Pipeline.ExpireDue(ctx)
	if _, err := d.Journal.MaybeDailyCheckpoint(); err != nil {
		d.Log.Warn("checkpoint", "err", err)
	}
	_ = d.Journal.RetryWitness()
	r := d.Config.Current().Retention
	if _, err := d.Journal.Purge(time.Now(), audit.Retention{Receipts: r.Receipts.D(), EvidenceMoney: r.EvidenceMoney.D(),
		EvidenceOther: r.EvidenceOther.D(), Denied: r.Denied.D()}); err != nil {
		d.Log.Warn("purge", "err", err)
	}
}

// Serve слушает сокет владельца и сокет агентов до отмены ctx.
func (d *Daemon) Serve(ctx context.Context, ownerSock, agentSock string) error {
	ol, err := ipc.Listen(ownerSock)
	if err != nil {
		return fmt.Errorf("owner socket: %w", err)
	}
	al, err := ipc.Listen(agentSock)
	if err != nil {
		ol.Close()
		return fmt.Errorf("agent socket: %w", err)
	}
	errc := make(chan error, 2)
	d.ServeListener(ol, true, errc)
	d.ServeListener(al, false, errc)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				d.Tick(ctx)
			}
		}
	}()
	d.Log.Info("gate4ai daemon listening", "owner", ownerSock, "agent", agentSock)
	select {
	case <-ctx.Done():
	case <-d.stopCh():
		d.Log.Info("stop requested by the owner")
	case err = <-errc:
	}
	d.Shutdown()
	return err
}

// RequestStop просит Serve завершиться (POST /v1/shutdown).
func (d *Daemon) RequestStop() { d.stopOnce.Do(func() { close(d.stopCh()) }) }

func (d *Daemon) stopCh() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop == nil {
		d.stop = make(chan struct{})
	}
	return d.stop
}

// ServeListener обслуживает API на слушателе: owner — сокет владельца.
func (d *Daemon) ServeListener(l net.Listener, owner bool, errc chan<- error) *http.Server {
	srv := &http.Server{Handler: d.Handler(owner), ConnContext: ipc.ConnContext, ReadHeaderTimeout: 10 * time.Second}
	d.mu.Lock()
	d.servers = append(d.servers, srv)
	d.mu.Unlock()
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) && errc != nil {
			errc <- err
		}
	}()
	return srv
}

// Shutdown останавливает серверы и коннекторы.
func (d *Daemon) Shutdown() {
	d.mu.Lock()
	srvs := d.servers
	d.servers = nil
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range srvs {
		_ = s.Shutdown(ctx)
	}
	d.Pipeline.Wait()
}

// Close освобождает ресурсы.
func (d *Daemon) Close() {
	d.Shutdown()
	d.Registry.Close()
	if d.Wasm != nil {
		_ = d.Wasm.Close()
	}
	d.State.Close()
	d.Journal.Close()
}

// vaultSecrets — значения секретов для функций хоста.
type vaultSecrets struct{ v *vault.Vault }

func (s vaultSecrets) Value(alias string) (string, error) { return s.v.Get(alias) }

// startWasm — среда исполнения коннекторов и службы хоста для них.
func (d *Daemon) startWasm(opt Options) error {
	home := opt.UserHome
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	kv := wasm.KVDir(filepath.Join(d.ConnectorsDir, "data"))
	e, err := wasm.NewEngine(filepath.Join(opt.Home, "cache", "wasm"), wasm.Services{
		Secrets: vaultSecrets{d.Vault},
		KV:      func(name string) hostfuncs.KV { return kv(name) },
		Router:  d, Log: d.Log, Home: home, Dial: opt.WasmDial, RootCAs: opt.WasmRootCAs})
	if err != nil {
		return err
	}
	d.Wasm = e
	return nil
}

// MultiNotifier рассылает события во все каналы.
type MultiNotifier struct {
	mu sync.RWMutex
	ns []pipeline.Notifier
}

// Add добавляет канал.
func (m *MultiNotifier) Add(n pipeline.Notifier) {
	m.mu.Lock()
	m.ns = append(m.ns, n)
	m.mu.Unlock()
}

func (m *MultiNotifier) each(f func(pipeline.Notifier)) {
	m.mu.RLock()
	ns := append([]pipeline.Notifier{}, m.ns...)
	m.mu.RUnlock()
	for _, n := range ns {
		f(n)
	}
}

func (m *MultiNotifier) Notify(a *state.Approval) { m.each(func(n pipeline.Notifier) { n.Notify(a) }) }
func (m *MultiNotifier) Resolved(a *state.Approval) {
	m.each(func(n pipeline.Notifier) { n.Resolved(a) })
}
func (m *MultiNotifier) Denied(agent, action, reason string) {
	m.each(func(n pipeline.Notifier) { n.Denied(agent, action, reason) })
}
