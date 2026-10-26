// SPDX-License-Identifier: AGPL-3.0-only

// Package daemon — ядро gate4ai (`gate4ai daemon`): Config Store, коннекторы, Policy Engine,
// Action Pipeline, журнал, State Store, Vault и локальные API по сокетам.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser"
	"github.com/gate4ai/gate4ai/internal/connectors/builtin/files"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
	"github.com/gate4ai/gate4ai/internal/front/oauth"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/agentid"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/secrets/vault"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/connector"
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// Options — параметры daemon.
type Options struct {
	Home          string
	InitialConfig []byte
	Keys          keys.Store
	// ConnectorsDir — установленные коннекторы ("" — <Home>/connectors).
	ConnectorsDir string
	// Connectors — дополнительные коннекторы в том же процессе (тесты).
	Connectors []connectors.Client
	// UserHome — каталог для "~" в путях коннекторов ("" — домашний каталог).
	UserHome string
	Notifier pipeline.Notifier
	Log      *slog.Logger
	Now      func() time.Time
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
	// Callers — кто стоит за процессом-собеседником локального сокета (агент,
	// его каталог); nil — не проверяется: тогда сокет владельца не отличает
	// агента от человека и ключи с привязкой к каталогу не принимаются.
	Callers func(pid int) agentid.Caller

	mu        sync.Mutex
	installMu sync.Mutex    // установка коннекторов
	userHome  string        // домашний каталог для "~" в путях
	stop      chan struct{} // закрывается RequestStop
	stopOnce  sync.Once
	notifiers *MultiNotifier
	questions questions
	handoffMu sync.Mutex
	handoffs  map[string]HandoffNote // browser.handoff, которые ждут человека
	bviews    browserViews           // зрители сеансов браузера
	localUI   string                 // адрес Web UI на этом компьютере (SetLocalUI)
	servers   []*http.Server
	extra     []func(*http.ServeMux) // доп. маршруты владельца (MCP HTTP, Web UI)
	oauth     *oauth.Server

	// Публичный адрес устройства (relay, прямой HTTPS): маршруты по путям, адрес.
	public     []publicRoute
	publicURLs []func() string
	publicMux  http.Handler

	// strict — новые версии конфигурации проверяются по каталогу строго; до
	// конца открытия хранилища сохранённая версия проверяется мягко (startErrs —
	// её ошибки, которые терпятся и в новых версиях, пока владелец их не уберёт).
	strict    atomic.Bool
	startErrs map[string]bool
	// policyWarn — политика скомпилирована без проверки имён аргументов:
	// правила ссылаются на действия незагруженных коннекторов.
	policyWarn string
	// ticker — горутина периодических задач Serve (Close ждёт её).
	ticker sync.WaitGroup
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
	d.removeLegacyPackages()
	// Каталог действий — из манифестов установленных пакетов (компиляция — в фоне);
	// отзыв версий проверяется после чтения конфигурации (ключи registry — в ней).
	for _, c := range (store.Options{Dir: d.ConnectorsDir}).LoadAll(context.Background(), d.Wasm, d.Log) {
		d.Registry.Add(c)
	}
	for _, b := range []struct {
		m manifest.Manifest
		c connector.Connector
	}{{files.Manifest(), files.Files{}}, {browser.Manifest(), browser.New()}} {
		bc, err := d.Wasm.Builtin(b.m, b.c)
		if err != nil {
			return nil, err
		}
		d.Registry.Add(bc)
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
	_, err = os.Stat(filepath.Join(opt.Home, "config", "current"))
	fresh := errors.Is(err, os.ErrNotExist)
	d.Config, err = config.OpenStore(filepath.Join(opt.Home, "config"), opt.InitialConfig, d.validate)
	if err != nil {
		return nil, err
	}
	if !fresh {
		d.migrateBuiltins()
	}
	d.strict.Store(true)
	d.dropRevoked()
	d.AddRoutes(d.connectorRoutes)
	d.AddRoutes(d.knowledgeRoutes)
	d.AddRoutes(d.browserRoutes)
	eng, err := d.compilePolicy(d.Config.Current())
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
	d.Pipeline.Home = d.userHome
	d.Pipeline.Describe = map[string]func(string, map[string]any) (string, error){SelfConnector: d.describeSelf}
	d.Pipeline.EndCall = d.endCall
	d.Pipeline.Adopt = d.adoptCall
	d.Pipeline.RunHook = d.recordRun
	d.Pipeline.ConnectorLog = d.devLog
	d.Pipeline.AlwaysAsk = map[string]func(map[string]any) string{SelfConnector: d.alwaysAskSelf}
	d.Pipeline.Vault = d.Vault
	d.Pipeline.Ask = d.ask
	d.Pipeline.Handoff = d.handoff
	d.Pipeline.ViewURL = d.BrowserViewURL
	if opt.Now != nil {
		d.Pipeline.Now = opt.Now
		d.State.Now = opt.Now
	}
	d.Config.Subscribe(func(c *config.Config) {
		e, err := d.compilePolicy(c)
		if err != nil {
			d.Log.Error("policy compile after config change", "err", err)
			return
		}
		d.Pipeline.SetEngine(e)
		d.Log.Info("policy updated")
	})
	// Действия, прерванные прошлой остановкой: итоговые квитанции и итоги запросов.
	if n, err := d.Pipeline.Recover(); err != nil {
		d.Log.Error("recover interrupted actions", "err", err)
	} else if n > 0 {
		d.Log.Warn("interrupted actions closed", "count", n)
	}
	return d, nil
}

// errKey — ошибка проверки без номера строки (он меняется между версиями).
func errKey(e config.FieldError) string { return e.Path + "\x00" + e.Msg }

// validate — проверка CEL по каталогу загруженных коннекторов. Сохранённая
// версия на старте проверяется мягко: если правила ссылаются на аргументы
// действий коннектора, которого нет в каталоге (удалён, отозван, повреждён),
// daemon всё равно стартует — иначе владельцу нечем исправить политику.
// Такие ошибки запоминаются и терпятся в новых версиях; новые — нет.
func (d *Daemon) validate(c *config.Config) config.Errors {
	errs := policy.Validator(policy.Catalog(d.Registry.Catalog()))(c)
	if len(errs) == 0 {
		return nil
	}
	if !d.strict.Load() {
		if policy.Validator(nil)(c) != nil {
			return errs // ошибка не в именах аргументов
		}
		d.mu.Lock()
		d.startErrs = map[string]bool{}
		for _, e := range errs {
			d.startErrs[errKey(e)] = true
		}
		d.mu.Unlock()
		d.Log.Warn("saved policy references actions of connectors that are not loaded; their conditions are not checked against the catalog until you fix the policy", "errors", errs.Error())
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out config.Errors
	for _, e := range errs {
		if !d.startErrs[errKey(e)] {
			out = append(out, e)
		}
	}
	return out
}

// compilePolicy — движок политики по каталогу. Если имена аргументов в
// условиях не сходятся с каталогом (коннектор удалён или не загрузился),
// политика компилируется без каталога: такие условия для неизвестного
// действия не вычисляются, а остальное работает. Что не сошлось — в
// PolicyWarning (видно владельцу в daemon status).
func (d *Daemon) compilePolicy(c *config.Config) (*policy.Engine, error) {
	e, err := policy.New(c, policy.Catalog(d.Registry.Catalog()))
	warn := ""
	if err != nil {
		var err2 error
		if e, err2 = policy.New(c, nil); err2 != nil {
			return nil, err
		}
		warn = err.Error()
		d.Log.Warn("policy compiled without the connector catalog", "err", err)
	}
	d.mu.Lock()
	d.policyWarn = warn
	d.mu.Unlock()
	return e, nil
}

// PolicyWarning — почему политика скомпилирована без каталога ("" — всё сошлось).
func (d *Daemon) PolicyWarning() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.policyWarn
}

// TrackServer — HTTP-сервер (Web UI), который Shutdown останавливает вместе с API.
func (d *Daemon) TrackServer(srv *http.Server) {
	d.mu.Lock()
	d.servers = append(d.servers, srv)
	d.mu.Unlock()
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
	// Тикер живёт до конца Serve (в том числе при остановке по просьбе
	// владельца): после него Close закрывает базы.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.ticker.Add(1)
	go func() {
		defer d.ticker.Done()
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
	cancel()
	d.Shutdown()
	d.ticker.Wait()
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
	d.TrackServer(srv)
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
		if err := s.Shutdown(ctx); err != nil {
			// Не дождались (status --wait, долгий вызов): закрыть соединения.
			_ = s.Close()
		}
	}
	d.Pipeline.Wait()
}

// Close освобождает ресурсы.
func (d *Daemon) Close() {
	d.Shutdown()
	d.ticker.Wait()
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
	d.userHome = home
	kv := wasm.KVDir(filepath.Join(d.ConnectorsDir, "data"))
	e, err := wasm.NewEngine(filepath.Join(opt.Home, "cache", "wasm"), wasm.Services{
		Secrets: vaultSecrets{d.Vault},
		KV:      func(name string) hostfuncs.KV { return kv(name) },
		Router:  d, Log: d.Log, Home: home, Mask: d.Vault.MaskString,
		Folders: func() []access.Folder {
			if d.Pipeline == nil {
				return []access.Folder{}
			}
			return d.Pipeline.GlobalFolders()
		}})
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
