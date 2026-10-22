// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

const egressCfg = `
agents: {claude-code: {profile: dev}, other: {profile: dev}}
profiles:
  dev:
    connectors: [files]
    sandbox:
      allow_domains: [api.anthropic.test, 127.0.0.1, "*.corp.test"]
      ask_domains: [localhost]
egress:
  secrets:
    - {domain: api.anthropic.test, header: X-Api-Key, env: ANTHROPIC_API_KEY, placeholder: sk-ant-gate4ai, secret: "@a", agents: [claude-code]}
    - {domain: "https://api.other.test:8443", header: Authorization, env: KEY_B, secret: "@b"}
    - {domain: "*.wild.test", header: Authorization, env: KEY_C, secret: "@c"}
    - {domain: "http://plain.test", header: Authorization, env: KEY_D, secret: "@d"}
`

func egressRuns(t *testing.T) (*Runs, *daemon.Daemon) {
	t.Helper()
	home, _ := os.MkdirTemp("", "g4asb")
	t.Cleanup(func() { os.RemoveAll(home) })
	c, _ := connectors.NewInProcess(noop{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(egressCfg)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, a := range []string{"@a", "@b", "@c", "@d"} {
		d.Vault.Set(a, "fake-value-"+a[1:])
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r, err := Attach(ctx, d, filepath.Join(home, "run"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return r, d
}

// Подстановки egress: только агентам из agents, только точному https-origin
// (схема, хост, порт; без wildcard); с env — одноразовая заглушка запуска;
// локальные адреса — только явно указанные.
func TestEgressSecretsBinding(t *testing.T) {
	r, d := egressRuns(t)
	run1, err := r.Start("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop(run1.ID)
	run2, _ := r.Start("claude-code")
	defer r.Stop(run2.ID)
	other, _ := r.Start("other")
	defer r.Stop(other.ID)
	if run1.Home != d.Home || run1.Home == "" {
		t.Fatal("run must carry the daemon home", run1.Home)
	}
	ph1, ph2 := run1.SecretEnv["ANTHROPIC_API_KEY"], run2.SecretEnv["ANTHROPIC_API_KEY"]
	if !strings.HasPrefix(ph1, "sk-ant-gate4ai-") || len(ph1) < 40 || ph1 == ph2 || other.SecretEnv["ANTHROPIC_API_KEY"] != "" || !strings.HasPrefix(other.SecretEnv["KEY_B"], "gate4ai-") {
		t.Fatal(run1.SecretEnv, run2.SecretEnv, other.SecretEnv)
	}
	// Запуск видит только свою заглушку.
	if inj := r.secrets(run1.ID, "api.anthropic.test", 443); len(inj) != 1 || inj[0].Placeholder != ph1 || inj[0].Value != "fake-value-a" {
		t.Fatal(inj)
	}
	if inj := r.secrets(run2.ID, "api.anthropic.test", 443); len(inj) != 1 || inj[0].Placeholder != ph2 {
		t.Fatal(inj)
	}
	n := func(run, host string, port int) int { return len(r.secrets(run, host, port)) }
	cases := []struct {
		run, host  string
		port, want int
	}{
		{other.ID, "api.anthropic.test", 443, 0}, // не тот агент
		{run1.ID, "api.anthropic.test", 8443, 0}, // не тот порт
		{other.ID, "api.other.test", 8443, 1},    // заглушка своего запуска
		{other.ID, "api.other.test", 443, 0},
		{other.ID, "x.wild.test", 443, 0}, // wildcard не действует
		{other.ID, "plain.test", 80, 0},   // не https
		{other.ID, "plain.test", 443, 0},
		{"nope", "api.other.test", 8443, 0}, // неизвестный запуск
	}
	for _, x := range cases {
		if got := n(x.run, x.host, x.port); got != x.want {
			t.Errorf("%+v: got %d", x, got)
		}
	}
	for host, want := range map[string]bool{"127.0.0.1": true, "localhost": true, "a.corp.test": false, "10.0.0.1": false} {
		if r.allowLocal("claude-code", host) != want {
			t.Errorf("allowLocal %s", host)
		}
	}
	if r.allowLocal("ghost", "127.0.0.1") {
		t.Fatal("unknown agent")
	}
	// Пароль прокси.
	pass, _ := urlPass(run1.ProxyURL)
	if a, id, ok := r.auth("run-"+run1.ID, pass); !ok || a != "claude-code" || id != run1.ID {
		t.Fatal("auth")
	}
	if _, _, ok := r.auth("run-"+run1.ID, pass+"x"); ok {
		t.Fatal("wrong password accepted")
	}
}

// Запуск без продления гаснет: токен истекает, прокси не пускает,
// keepalive после срока — отказ; с продлением — живёт.
func TestRunKeepAlive(t *testing.T) {
	r, d := egressRuns(t)
	defer func(ttl time.Duration) { RunTTL = ttl }(RunTTL)
	RunTTL = 300 * time.Millisecond
	run, err := r.Start("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := urlPass(run.ProxyURL)
	for i := 0; i < 4; i++ {
		time.Sleep(150 * time.Millisecond)
		if err := r.KeepAlive(run.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.State.AgentByToken(run.Token); err != nil {
		t.Fatal("kept-alive run token expired", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, _, ok := r.auth("run-"+run.ID, pass); ok {
		t.Fatal("expired run accepted by the proxy")
	}
	if _, err := d.State.AgentByToken(run.Token); err == nil {
		t.Fatal("run token without keepalive survives")
	}
	if err := r.KeepAlive(run.ID); err == nil {
		t.Fatal("keepalive revived an expired run")
	}
}

func urlPass(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return "", false
	}
	return u.User.Password()
}

// Одноразовые заглушки запуска попадают в окружение агента во всех режимах;
// токен запуска и пароль прокси — не в argv (видно в ps).
func TestBuildSecretEnvAndArgv(t *testing.T) {
	r := sampleRun()
	r.SecretEnv = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-gate4ai-0123456789abcdef"}
	o := Options{Workspace: "/home/u/projects/policy", GateBin: "/usr/bin/gate4ai", Home: "/home/u", TmpDir: "/tmp/run"}
	for _, mode := range []string{ModeSandbox, ModeContainer, ModeMicroVM} {
		o.Mode = mode
		s, err := Build(r, o)
		if mode == ModeContainer && err != nil {
			continue // только Linux
		}
		if err != nil {
			t.Fatal(mode, err)
		}
		all := strings.Join(s.Env, "\n")
		for _, b := range s.Files {
			all += "\n" + b
		}
		if !strings.Contains(all, "ANTHROPIC_API_KEY=sk-ant-gate4ai-0123456789abcdef") {
			t.Errorf("%s: placeholder not in the agent environment", mode)
		}
		if a := strings.Join(s.Argv, " "); strings.Contains(a, r.Token) || strings.Contains(a, ":pw@") {
			t.Errorf("%s: secrets in argv: %s", mode, a)
		}
	}
}
