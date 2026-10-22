// SPDX-License-Identifier: AGPL-3.0-only

// Package native — среда исполнения коннекторов runtime: native: бинарник
// пакета запускается дочерним процессом при первом вызове и остаётся
// запущенным; обмен — JSON-RPC по stdio (github.com/gate4ai/gate4ai/sdk-go/native).
// Функции хоста процесс вызывает в рамках своего вызова — с той же проверкой
// возможностей манифеста, что у WASM. Процесс работает без песочницы, с
// правами пользователя: такие пакеты ставятся только от проверенного издателя.
package native

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/hostfuncs"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	sdknative "github.com/gate4ai/gate4ai/sdk-go/native"

	"github.com/gate4ai/gate4ai/internal/connectors/wasm"
)

// Hosts — функции хоста для одного вызова коннектора.
type Hosts interface {
	Dispatcher(ctx context.Context, m manifest.Manifest, lim manifest.Limits, settings map[string]any) (*hostfuncs.Dispatcher, error)
}

// Падения подряд, после которых коннектор считается неисправным.
const (
	MaxCrashes   = 5
	CrashWindow  = time.Minute
	startTimeout = 15 * time.Second
	stopTimeout  = 5 * time.Second
)

// Process — нативный коннектор (connectors.Client).
type Process struct {
	bin   string
	man   manifest.Manifest
	hosts Hosts
	log   *slog.Logger

	mu      sync.Mutex
	run     *running
	crashes []time.Time
	broken  error
	closed  bool

	calls sync.Map // id вызова → *hostfuncs.Dispatcher
}

type running struct {
	cmd  *exec.Cmd
	peer *sdknative.Peer
	done chan struct{}
	stop bool // остановлен хостом (не падение)
}

// New — коннектор из бинарника bin с манифестом пакета m (процесс — при первом вызове).
func New(bin string, m manifest.Manifest, hosts Hosts, log *slog.Logger) (*Process, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.Runtime != manifest.RuntimeNative {
		return nil, fmt.Errorf("connector %s: runtime %q is not native", m.Connector, m.Runtime)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Process{bin: bin, man: m, hosts: hosts, log: log}, nil
}

func (p *Process) Manifest() manifest.Manifest { return p.man }

// peer — запущенный процесс (запускает при необходимости).
func (p *Process) peer(ctx context.Context) (*sdknative.Peer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("connector closed")
	}
	if p.broken != nil {
		return nil, p.broken
	}
	if p.run != nil {
		select {
		case <-p.run.done:
			p.run = nil
		case <-p.run.peer.Done(): // соединение закрыто, процесс завершается
			p.run = nil
		default:
			return p.run.peer, nil
		}
	}
	r, err := p.start(ctx)
	if err != nil {
		p.crash(err)
		return nil, err
	}
	p.run = r
	return r.peer, nil
}

// crash учитывает падение; слишком частые — коннектор неисправен. Под p.mu.
func (p *Process) crash(err error) {
	now := time.Now()
	var recent []time.Time
	for _, t := range p.crashes {
		if now.Sub(t) < CrashWindow {
			recent = append(recent, t)
		}
	}
	p.crashes = append(recent, now)
	if len(p.crashes) > MaxCrashes {
		p.broken = abi.Errorf(abi.CodeConnector, "connector %s crashed %d times in %s and is disabled until it is updated or gate4ai restarts: %v",
			p.man.Connector, len(p.crashes), CrashWindow, err)
		p.log.Error("native connector disabled", "connector", p.man.Connector, "err", err)
	}
}

func (p *Process) start(ctx context.Context) (*running, error) {
	cmd := exec.Command(p.bin)
	cmd.Env = append(MinimalEnv(os.Environ()), "GATE4AI_NATIVE=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errOut, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("connector %s: start: %w", p.man.Connector, err)
	}
	r := &running{cmd: cmd, done: make(chan struct{})}
	r.peer = sdknative.NewPeer(out, in, p.handle)
	go p.stderr(errOut)
	go func() {
		err := r.peer.Run()
		_ = in.Close()
		werr := cmd.Wait()
		p.mu.Lock()
		if !r.stop && !p.closed {
			p.log.Warn("native connector exited", "connector", p.man.Connector, "err", errors.Join(err, werr))
			p.crash(fmt.Errorf("exited: %v", errors.Join(err, werr)))
		}
		p.mu.Unlock()
		close(r.done)
	}()
	ictx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	var ini sdknative.InitializeResult
	if err := r.peer.Call(ictx, sdknative.MethodInitialize, sdknative.InitializeParams{Protocol: sdknative.Protocol}, &ini); err != nil {
		p.kill(r)
		return nil, abi.Errorf(abi.CodeConnector, "connector %s: initialize: %v", p.man.Connector, err)
	}
	if ini.Protocol != sdknative.Protocol || !manifest.Same(ini.Manifest, p.man) {
		p.kill(r)
		return nil, abi.Errorf(abi.CodeConnector, "connector %s: the process reports another manifest or protocol %d than its package", p.man.Connector, ini.Protocol)
	}
	p.log.Info("native connector started", "connector", p.man.Connector, "pid", cmd.Process.Pid)
	return r, nil
}

func (p *Process) kill(r *running) {
	r.stop = true
	_ = r.cmd.Process.Kill()
}

// stderr — диагностика процесса в лог daemon построчно.
func (p *Process) stderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 64<<10)
	// Пакет получает значения (Revealed, коды от человека): отладочная строка
	// или паника со значением не должна лечь в лог открытым текстом.
	mask := func(s string) string { return s }
	if m, ok := p.hosts.(interface{ MaskString(string) string }); ok {
		mask = m.MaskString
	}
	for sc.Scan() {
		p.log.Info(mask(sc.Text()), "connector", p.man.Connector, "stream", "stderr")
	}
}

// handle — запросы процесса: функции хоста в рамках его вызова.
func (p *Process) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method != sdknative.MethodHost {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	var hp sdknative.HostParams
	if err := json.Unmarshal(params, &hp); err != nil {
		return nil, err
	}
	v, ok := p.calls.Load(hp.Call)
	if !ok {
		return sdknative.RawResponse(abi.Fail(abi.Errorf(abi.CodeBadRequest, "call %q is not running", hp.Call))), nil
	}
	return sdknative.RawResponse(v.(*hostfuncs.Dispatcher).Call(hp.Fn, hp.Req)), nil
}

func newCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *Process) call(ctx context.Context, method string, settings map[string]any, prepare func(*hostfuncs.Dispatcher) (any, error), out any) ([]abi.EvidenceItem, error) {
	lim := p.man.Limits.WithDefaults()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(lim.TimeoutS)*time.Second)
	defer cancel()
	d, err := p.hosts.Dispatcher(ctx, p.man, lim, settings)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	req, err := prepare(d)
	if err != nil {
		return nil, err
	}
	peer, err := p.peer(ctx)
	if err != nil {
		return nil, err
	}
	id := newCallID()
	p.calls.Store(id, d)
	defer p.calls.Delete(id)
	raw, _ := json.Marshal(req)
	var resp sdknative.RawResponse
	if err := peer.Call(ctx, method, sdknative.CallParams{Call: id, Request: raw}, &resp); err != nil {
		if ctx.Err() != nil {
			// Ответа больше не ждём: процесс должен прервать вызов, иначе он
			// доделает его без хозяина (страница браузера останется занятой).
			// Асинхронно и со сроком: процесс, который не читает stdin, не
			// задерживает ответ.
			go notify(peer, sdknative.MethodCancel, sdknative.CancelParams{Call: id})
		}
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, abi.Errorf(abi.CodeLimit, "connector %s: time limit %ds exceeded", p.man.Connector, lim.TimeoutS)
		case errors.Is(ctx.Err(), context.Canceled):
			return nil, cancelled(p.man.Connector)
		}
		return nil, abi.Errorf(abi.CodeConnector, "connector %s crashed: %v", p.man.Connector, err)
	}
	if err := abi.Decode(resp, out); err != nil {
		return nil, err
	}
	return d.Evidence(), nil
}

// Execute исполняет действие в процессе; значения алиасов из capabilities.reveal
// хост передаёт в запросе вместе с теми, что уже положил pipeline (поля
// учётки для browser.login / browser.fill_secret).
func (p *Process) Execute(ctx context.Context, req abi.ExecRequest) (abi.ExecResult, error) {
	var res abi.ExecResult
	ev, err := p.call(ctx, sdknative.MethodExecute, req.Settings, func(d *hostfuncs.Dispatcher) (any, error) {
		for _, alias := range d.Check.R.Reveal {
			if d.Secrets == nil {
				return nil, abi.Errorf(abi.CodeNotSupported, "no vault")
			}
			v, err := d.Secrets.Value(alias)
			if err != nil {
				return nil, abi.Errorf(abi.CodeNotFound, "secret %s: %v", alias, err)
			}
			if req.Revealed == nil {
				req.Revealed = map[string]string{}
			}
			req.Revealed[alias] = v
		}
		return req, nil
	}, &res)
	if err != nil {
		return abi.ExecResult{}, err
	}
	res.Evidence = append(res.Evidence, ev...)
	return res, nil
}

// Verify проверяет результат в процессе.
func (p *Process) Verify(ctx context.Context, req abi.VerifyRequest) (abi.VerifyResult, error) {
	var res abi.VerifyResult
	ev, err := p.call(ctx, sdknative.MethodVerify, req.Settings, func(*hostfuncs.Dispatcher) (any, error) { return req, nil }, &res)
	if err != nil {
		return abi.VerifyResult{}, err
	}
	res.Evidence = append(res.Evidence, ev...)
	return res, nil
}

// Состояния процесса.
const (
	StateStopped  = "stopped"  // не запущен (запустится при первом вызове)
	StateRunning  = "running"  // запущен
	StateDisabled = "disabled" // отключён после частых падений
)

// State — состояние процесса для владельца (connectors, Web UI).
func (p *Process) State() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.broken != nil:
		return StateDisabled
	case p.run != nil:
		select {
		case <-p.run.done:
		default:
			return StateRunning
		}
	}
	return StateStopped
}

// EndCall — корневой вызов закончился (только если процесс запущен).
func (p *Process) EndCall(call string) {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r != nil {
		go notify(r.peer, sdknative.MethodEndCall, sdknative.EndCallParams{Call: call})
	}
}

// notify — уведомление процессу со сроком stopTimeout (процесс мог перестать
// читать stdin: запись в полный pipe не должна держать хост).
func notify(peer *sdknative.Peer, method string, params any) {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	_ = peer.NotifyContext(ctx, method, params)
}

// envAllow — переменные окружения хоста, которые получает нативный процесс:
// системные (пути, язык, дисплей, временные каталоги, профиль пользователя
// Windows), но не токены и ключи из окружения daemon.
var envAllow = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true, "LANGUAGE": true, "TZ": true,
	"TMPDIR": true, "TEMP": true, "TMP": true, "DISPLAY": true, "WAYLAND_DISPLAY": true, "XAUTHORITY": true,
	"DBUS_SESSION_BUS_ADDRESS": true, "__CF_USER_TEXT_ENCODING": true,
	"SYSTEMROOT": true, "SYSTEMDRIVE": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true, "USERPROFILE": true,
	"USERNAME": true, "USERDOMAIN": true, "APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true, "PROGRAMFILES": true,
	"PROGRAMFILES(X86)": true, "PROGRAMW6432": true, "COMMONPROGRAMFILES": true, "COMMONPROGRAMFILES(X86)": true,
	"HOMEDRIVE": true, "HOMEPATH": true, "PUBLIC": true, "NUMBER_OF_PROCESSORS": true, "PROCESSOR_ARCHITECTURE": true, "OS": true,
}

// testEnvPrefix — переменные тестов пакета (процесс коннектора — бинарник теста).
var testEnvPrefix string

// MinimalEnv — окружение нативного процесса из окружения хоста: только
// envAllow и LC_* / XDG_* (без GATE4AI_* и прочих переменных daemon).
func MinimalEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		u := strings.ToUpper(k)
		if envAllow[u] || strings.HasPrefix(u, "LC_") || strings.HasPrefix(u, "XDG_") || testEnvPrefix != "" && strings.HasPrefix(k, testEnvPrefix) {
			out = append(out, kv)
		}
	}
	return out
}

// Close останавливает процесс: shutdown, затем kill по сроку.
func (p *Process) Close() error {
	p.mu.Lock()
	p.closed = true
	r := p.run
	p.run = nil
	if r != nil {
		r.stop = true
	}
	p.mu.Unlock()
	if r == nil {
		return nil
	}
	// Уведомление — асинхронно: процесс, переставший читать stdin, не
	// задерживает остановку дольше stopTimeout (дальше — kill).
	go notify(r.peer, sdknative.MethodShutdown, struct{}{})
	select {
	case <-r.done:
	case <-time.After(stopTimeout):
		_ = r.cmd.Process.Kill()
		<-r.done
	}
	return nil
}

// BuildGo собирает нативный Go-коннектор из srcDir под платформу "os/arch" в
// файл out: CGO_ENABLED=0 go build -trimpath -buildvcs=false тулчейном из go.mod.
// Сборка воспроизводима: бинарник зависит только от исходников, go.sum,
// версии Go и платформы.
func BuildGo(ctx context.Context, srcDir, platform, out string) error {
	goos, goarch, ok := strings.Cut(platform, "/")
	if !ok {
		return fmt.Errorf("bad platform %q (want os/arch)", platform)
	}
	tc, err := wasm.Toolchain(srcDir)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-buildid=", "-o", out, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS=", "GOTOOLCHAIN="+tc)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s for %s: %v\n%s", srcDir, platform, err, b)
	}
	return nil
}

// cancelled — вызов отменён тем, кто его начал (агент прервал CLI, закрылся
// HTTP-запрос): коннектор не падал.
func cancelled(name string) error {
	return abi.Errorf(abi.CodeCancelled, "connector %s: the call was cancelled because the request that started it ended", name)
}
