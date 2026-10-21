// SPDX-License-Identifier: AGPL-3.0-only

// Package e2e — сквозные проверки gate4ai на собранных из исходников бинарниках
// и настоящих коннекторах: хост, relay и коннекторы собираются из этого же
// репозитория, а не из опубликованных версий.
package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// src — абсолютный путь к каталогу репозитория ("" или "gate4ai" — корень,
// иначе relay, connectors/mail, …).
func src(t *testing.T, name string) string {
	t.Helper()
	if name == "gate4ai" {
		name = ""
	}
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
	out, err := exec.Command("go", "env", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	v := strings.Fields(string(out))
	return append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPATH="+v[0], "GOMODCACHE="+v[1], "GOCACHE="+v[2])
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
	g := &gate{t: t, bin: filepath.Join(tmp, "gate4ai"), home: filepath.Join(tmp, "gate4ai-home"), user: filepath.Join(tmp, "user")}
	os.MkdirAll(g.user, 0o755)
	g.env = append(workEnv(t), "GATE4AI_HOME="+g.home, "HOME="+g.user)

	b := exec.Command("go", "build", "-o", g.bin, "./cmd/gate4ai")
	b.Dir = src(t, "gate4ai")
	b.Env = g.env
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build gate4ai: %v\n%s", err, out)
	}

	var dlog bytes.Buffer
	d := exec.Command(g.bin, append([]string{"daemon", "--home", g.home, "--keystore", "file", "--ui", ""}, extra...)...)
	d.Env = g.env
	d.Stderr = &dlog
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.Process.Signal(os.Interrupt)
		d.Wait()
		if t.Failed() {
			t.Log("daemon log:\n" + dlog.String())
		}
	})
	for i := 0; ; i++ {
		if r := g.run("", "", "journal", "verify"); r.code == 0 {
			break
		}
		if i > 100 {
			t.Fatal("daemon did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
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
