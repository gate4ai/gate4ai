// SPDX-License-Identifier: AGPL-3.0-only

// Package e2e — сквозные проверки gate4ai на собранных из исходников бинарниках
// и настоящих коннекторах: хост, relay и коннекторы собираются из этого же
// репозитория, а не из опубликованных версий.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/platform/agentid"
)

// binDir — каталог бинарников, собранных один раз на прогон (TestMain).
var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gate4ai-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// built — сборка бинарника: одна на прогон, параллельные тесты ждут её.
type built struct {
	once sync.Once
	path string
	err  error
}

var (
	buildsMu sync.Mutex
	builds   = map[string]*built{}
)

// binary собирает пакет pkg модуля (путь от корня репозитория) один раз за
// прогон и возвращает путь к бинарнику (общий для тестов: они его не меняют).
func binary(t *testing.T, pkg string) string {
	t.Helper()
	buildsMu.Lock()
	b, ok := builds[pkg]
	if !ok {
		b = &built{}
		builds[pkg] = b
	}
	buildsMu.Unlock()
	b.once.Do(func() {
		b.path = filepath.Join(binDir, filepath.Base(pkg))
		if runtime.GOOS == "windows" {
			b.path += ".exe"
		}
		env, err := goEnv()
		if err != nil {
			b.err = err
			return
		}
		cmd := exec.Command("go", "build", "-o", b.path, pkg)
		cmd.Dir, cmd.Env = "..", env
		if out, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("build %s: %v\n%s", pkg, err, out)
		}
	})
	if b.err != nil {
		t.Fatal(b.err)
	}
	return b.path
}

// src — абсолютный путь к каталогу репозитория ("" — корень, иначе relay,
// connectors/mail, …).
func src(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s not found", p)
	}
	return p
}

// workEnv — окружение для go build хоста и коннекторов. Кэши Go фиксируются
// явно: тесты подменяют HOME.
func workEnv(t *testing.T) []string {
	t.Helper()
	env, err := goEnv()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Clip(env) // общий срез: append вызывающих — в свою копию
}

var goEnv = sync.OnceValues(func() ([]string, error) {
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		return nil, fmt.Errorf("go env: %w", err)
	}
	v := strings.Fields(string(out))
	return append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPATH="+v[0], "GOMODCACHE="+v[1], "GOCACHE="+v[2]), nil
})

// StartTimeout — сколько ждать готовности daemon и relay (медленный раннер, -race).
var StartTimeout = 60 * time.Second

// waitReady ждёт ready() до StartTimeout; процесс завершился раньше — сразу
// ошибка (а не таймаут).
func waitReady(t *testing.T, what string, exited <-chan struct{}, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(StartTimeout)
	for !ready() {
		select {
		case <-exited:
			t.Fatalf("%s exited at start", what)
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start in %s", what, StartTimeout)
		}
	}
}

type cliResult struct {
	code int
	out  string
}

func (c cliResult) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(c.out), &m); err != nil {
		t.Fatalf("not JSON (exit %d): %s", c.code, c.out)
	}
	return m
}

// underAgent — тесты запущены агентом (среди предков — Claude Code и т.п.).
var underAgent = agentid.Identify(os.Getpid()).IsAgent()

// gate — бинарник gate4ai и запущенный daemon во временном GATE4AI_HOME.
type gate struct {
	t    *testing.T
	bin  string
	home string // GATE4AI_HOME
	user string // HOME daemon и CLI: от него считается ~ в путях коннекторов
	env  []string
}

// startGate собирает gate4ai из ../gate4ai и запускает daemon с extra аргументами.
func startGate(t *testing.T, extra ...string) *gate {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e builds binaries")
	}
	tmp := t.TempDir()
	g := &gate{t: t, bin: binary(t, "./cmd/gate4ai"), home: filepath.Join(tmp, "gate4ai-home"), user: filepath.Join(tmp, "user")}
	os.MkdirAll(g.user, 0o755)
	g.env = append(workEnv(t), "GATE4AI_HOME="+g.home, "HOME="+g.user)
	if underAgent {
		// Тесты запустил агент (Claude Code): иначе сокет владельца отказал бы
		// харнессу как агенту. Проверку агента ведёт TestAgentPairing — в CI.
		g.env = append(g.env, "GATE4AI_OWNER_CHECK=off")
	}

	var dlog bytes.Buffer
	d := exec.Command(g.bin, append([]string{"daemon", "--home", g.home, "--keystore", "file", "--ui", ""}, extra...)...)
	d.Env = g.env
	d.Stderr = &dlog
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { d.Wait(); close(exited) }()
	t.Cleanup(func() {
		d.Process.Signal(os.Interrupt)
		<-exited
		if t.Failed() {
			t.Log("daemon log:\n" + dlog.String())
		}
	})
	waitReady(t, "daemon", exited, func() bool { return g.run("", "", "journal", "verify").code == 0 })
	return g
}

// run вызывает CLI: token — токен агента ("" — владелец), stdin — ввод.
func (g *gate) run(token, stdin string, args ...string) cliResult {
	cmd := exec.Command(g.bin, args...)
	cmd.Env = g.env
	if token != "" {
		cmd.Env = append(cmd.Env, "GATE4AI_TOKEN="+token)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return cliResult{code, out.String()}
}

// install собирает коннектор из ../connectors/<dir> и ставит его как свою сборку
// (--dev) с согласием владельца.
func (g *gate) install(dir string) { g.t.Helper(); g.installFrom(src(g.t, "connectors/"+dir)) }

// installFrom — то же из папки с исходниками dir.
func (g *gate) installFrom(dir string) {
	g.t.Helper()
	pkg := filepath.Join(g.t.TempDir(), filepath.Base(dir)+".g4c")
	// нативный коннектор (browser) — пакет только этой платформы
	if r := g.run("", "", "connectors", "pack", dir, "-o", pkg, "--platform", runtime.GOOS+"/"+runtime.GOARCH); r.code != 0 {
		g.t.Fatal(r.out)
	}
	if r := g.run("", "y\n", "connectors", "install", pkg, "--dev"); r.code != 0 {
		g.t.Fatalf("install %s: %d %s", dir, r.code, r.out)
	}
}
