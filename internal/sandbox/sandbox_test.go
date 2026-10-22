// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

func sampleRun() Run {
	return Run{ID: "abc123", Agent: "claude-code", Token: "g4a_TOKEN", Socket: "/run/user/1000/gate4ai/agent-abc123.sock",
		ProxyURL: "http://run-abc123:pw@127.0.0.1:4321", ProxyHost: "127.0.0.1", ProxyPort: 4321, CAPEM: "PEM", SkillMD: "# skill"}
}

func TestBuildModes(t *testing.T) {
	r := sampleRun()
	o := Options{Workspace: "/home/u/projects/policy", GateBin: "/usr/bin/gate4ai", Home: "/home/u", TmpDir: "/tmp/run"}
	s, err := Build(r, o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Argv, " ") != "srt --settings /tmp/run/srt-settings.json -- claude" {
		t.Fatal(s.Argv)
	}
	env := strings.Join(s.Env, "\n")
	for _, want := range []string{"GATE4AI_TOKEN=g4a_TOKEN", "GATE4AI_AGENT_SOCKET=" + r.Socket, "HTTPS_PROXY=" + r.ProxyURL, "SSL_CERT_FILE=/tmp/run/gate4ai-ca.pem", "NODE_EXTRA_CA_CERTS="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	var set map[string]map[string]any
	json.Unmarshal([]byte(s.Files["/tmp/run/srt-settings.json"]), &set)
	deny := set["filesystem"]["denyRead"].([]any)
	if !contains(deny, "~/.ssh") || !contains(set["filesystem"]["allowWrite"].([]any), o.Workspace) {
		t.Fatal(set)
	}
	if socks := set["network"]["allowUnixSockets"].([]any); len(socks) != 1 || socks[0] != r.Socket {
		t.Fatal("only the gate4ai socket may be reachable", socks)
	}
	if s.Files["/home/u/projects/policy/.claude/skills/gate4ai/SKILL.md"] != "# skill" || !strings.Contains(s.Files["/home/u/projects/policy/.mcp.json"], `"mcp"`) {
		t.Fatal("skill/mcp not prepared")
	}

	defer func(g string) { runtimeGOOS = g }(runtimeGOOS)
	runtimeGOOS = "linux"
	o.Mode, o.Command = ModeContainer, []string{"bash"}
	s, _ = Build(r, o)
	argv := strings.Join(s.Argv, " ")
	for _, want := range []string{"docker run --rm -it", "--network none", "--cap-drop ALL", "-v /home/u/projects/policy:/workspace", "-v " + r.Socket + ":/run/gate4ai.sock",
		"-v /tmp/run/egress.sock:/run/gate4ai-proxy.sock", "--env-file /tmp/run/container.env", "--entrypoint /usr/local/bin/gate4ai",
		"node:22-bookworm _egress-shim --listen 127.0.0.1:3128 --socket /run/gate4ai-proxy.sock -- bash"} {
		if !strings.Contains(argv, want) {
			t.Errorf("container argv lacks %q: %s", want, argv)
		}
	}
	// Прямого выхода в сеть нет: ни сети по умолчанию, ни адреса хоста; токен
	// и пароль прокси — не в argv (видны в ps), а в файле окружения.
	if strings.Contains(argv, "host.docker.internal") || strings.Contains(argv, "host-gateway") || strings.Contains(argv, r.Token) || strings.Contains(argv, ":pw@") {
		t.Fatal("container argv:", argv)
	}
	envFile := s.Files["/tmp/run/container.env"]
	for _, want := range []string{"GATE4AI_TOKEN=g4a_TOKEN\n", "GATE4AI_AGENT_SOCKET=/run/gate4ai.sock\n", "HTTPS_PROXY=http://run-abc123:pw@127.0.0.1:3128\n"} {
		if !strings.Contains(envFile, want) {
			t.Errorf("container env lacks %q: %s", want, envFile)
		}
	}
	if s.Bridge == nil || s.Bridge.Socket != "/tmp/run/egress.sock" || s.Bridge.Target != "127.0.0.1:4321" {
		t.Fatal("bridge:", s.Bridge)
	}
	if strings.Contains(argv, "docker.sock") || strings.Contains(argv, ".ssh") {
		t.Fatal("container gets host secrets")
	}
	o.Mode, o.Engine = ModeContainer, "podman"
	if s, _ = Build(r, o); s.Argv[0] != "podman" {
		t.Fatal(s.Argv)
	}
	o.Mode, o.Command = ModeMicroVM, nil
	s, _ = Build(r, o)
	if a := strings.Join(s.Argv, " "); !strings.HasPrefix(a, "docker sandbox run --workspace /home/u/projects/policy") || !strings.HasSuffix(a, " claude") {
		t.Fatal(a)
	}
	r.Agent = "my-custom-bot"
	if _, err := Build(r, Options{Workspace: "/w", TmpDir: "/t"}); err == nil {
		t.Fatal("unknown agent without command")
	}
	if tpl, ok := TemplateFor("claude-code-work"); !ok || tpl.Command[0] != "claude" {
		t.Fatal("prefix template")
	}
}

func contains(l []any, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestDoctor(t *testing.T) {
	base := DoctorEnv{Home: "/home/u", GOOS: "linux", LookPath: func(string) (string, error) { return "", errors.New("no") },
		Daemon: func() error { return nil }}
	bad := base
	bad.Environ = []string{"OPENAI_API_KEY=sk-123", "GITHUB_TOKEN=ghp", "PATH=/bin", "GATE4AI_TOKEN=g4a"}
	bad.Readable = func(p string) bool { return strings.HasSuffix(p, "google-chrome") || strings.HasSuffix(p, ".ssh") }
	bad.Writable = func(p string) bool { return p == "/var/run/docker.sock" }
	got := map[string]Check{}
	for _, c := range Doctor(bad) {
		got[c.ID] = c
	}
	for _, id := range []string{"isolation", "browser-profiles", "env-secrets", "docker-socket", "ssh-keys", "sandbox-runtime", "containers"} {
		if got[id].Status != "warn" || got[id].Fix == "" {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if d := got["env-secrets"].Detail; !strings.Contains(d, "GITHUB_TOKEN, OPENAI_API_KEY") || strings.Contains(d, "sk-123") || strings.Contains(d, "GATE4AI_TOKEN") {
		t.Fatal("env check must list names only:", d)
	}
	good := base
	good.Environ = []string{"GATE4AI_RUN_ID=abc", "PATH=/bin"}
	good.Readable = func(string) bool { return false }
	good.Writable = func(string) bool { return false }
	good.LookPath = func(string) (string, error) { return "/usr/bin/x", nil }
	for _, c := range Doctor(good) {
		if c.Status != "ok" {
			t.Errorf("%+v", c)
		}
	}
	down := good
	down.Daemon = func() error { return errors.New("connection refused") }
	if Doctor(down)[0].Status != "fail" {
		t.Fatal("daemon down must fail")
	}
}

// TestDoctorAgentAccess — агент без своего токена (права владельца), настройки
// Claude Code (токен, правило Bash) и устаревший SKILL.md.
func TestDoctorAgentAccess(t *testing.T) {
	files := map[string]string{}
	base := DoctorEnv{Home: "/home/u", GOOS: "linux", LookPath: func(string) (string, error) { return "/usr/bin/claude", nil },
		Readable: func(string) bool { return false }, Writable: func(string) bool { return false },
		ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		},
		ClaudeSettings: "/home/u/.claude/settings.json", SkillPath: "/home/u/.claude/skills/gate4ai/SKILL.md",
		Skill: func() (string, error) { return "new skill", nil }}
	check := func(e DoctorEnv) map[string]Check {
		got := map[string]Check{}
		for _, c := range Doctor(e) {
			got[c.ID] = c
		}
		return got
	}
	agent := base
	agent.Environ = []string{"CLAUDECODE=1"}
	files["/home/u/.claude/skills/gate4ai/SKILL.md"] = "old skill"
	got := check(agent)
	for _, id := range []string{"agent-token", "claude-code-token", "claude-code-permission", "skill"} {
		if got[id].Status != "warn" || got[id].Fix == "" {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if !strings.Contains(got["agent-token"].Detail, "approve its own requests") || !strings.Contains(got["claude-code-permission"].Fix, BashRule) {
		t.Fatal(got)
	}
	agent.Environ = []string{"CLAUDECODE=1", "GATE4AI_TOKEN=g4a_x"}
	files["/home/u/.claude/settings.json"] = `{"env": {"GATE4AI_TOKEN": "g4a_x"}, "permissions": {"allow": ["Bash(gate4ai *)"]}}`
	files["/home/u/.claude/skills/gate4ai/SKILL.md"] = "new skill"
	for id, c := range check(agent) {
		if strings.HasPrefix(id, "claude-code") || id == "agent-token" || id == "skill" {
			if c.Status != "ok" {
				t.Errorf("%s: %+v", id, c)
			}
		}
	}
}

type noop struct{}

func (noop) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "files", Actions: []manifest.Action{{Name: "read", Class: "read"}}}
}
func (noop) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Structured: "ok"}, nil
}
func (noop) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

const cfg = `
agents: {claude-code: {profile: dev}}
profiles:
  dev:
    connectors: [files]
    sandbox:
      allow_domains: [api.anthropic.test]
      ask_domains: ["*.github.test"]
    rules:
      - {id: no-evil-gh, action: egress.connect, when: "host == 'evil.github.test'", effect: deny, reason: blocked repo host}
egress:
  secrets:
    - {domain: api.anthropic.test, header: X-Api-Key, env: ANTHROPIC_API_KEY, secret: "@anthropic.key"}
`

func TestRunsAndEgress(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4asb")
	defer os.RemoveAll(home)
	c, _ := connectors.NewInProcess(noop{})
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Vault.Set("@anthropic.key", "sk-real")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := Attach(ctx, d, filepath.Join(home, "run"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r.AskWait = 3 * time.Second
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "key="+req.Header.Get("X-Api-Key"))
	}))
	defer upstream.Close()
	r.Proxy.Dial = func(ctx context.Context, n, a string) (net.Conn, error) {
		var dd net.Dialer
		return dd.DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	}

	if _, err := r.Start("ghost"); err == nil {
		t.Fatal("unknown agent run")
	}
	run, err := r.Start("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.SkillMD, "files.read") || strings.Contains(run.SkillMD, "egress") {
		t.Fatal("skill must list the agent's tools only")
	}
	// Агентский сокет запуска: только права агента.
	agentC := &http.Client{Transport: ipc.HTTPClient(run.Socket).Transport}
	req, _ := http.NewRequest("GET", "http://gate4ai/v1/tools", nil)
	req.Header.Set("Authorization", "Bearer "+run.Token)
	resp, err := agentC.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err)
	}
	// Агент не может вызвать внутренний egress.connect сам.
	req, _ = http.NewRequest("POST", "http://gate4ai/v1/call", strings.NewReader(`{"action":"egress.connect","args":{"host":"x"}}`))
	req.Header.Set("Authorization", "Bearer "+run.Token)
	resp, _ = agentC.Do(req)
	var res map[string]any
	json.NewDecoder(resp.Body).Decode(&res)
	if errOf(res)["code"] != "unknown_action" {
		t.Fatal("agent called internal connector", res)
	}

	ph := run.SecretEnv["ANTHROPIC_API_KEY"]
	pu, _ := url.Parse(run.ProxyURL)
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	get := func(u string, h map[string]string) (int, string) {
		req, _ := http.NewRequest("GET", u, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// allow_domains; в обычный HTTP секрет не подставляется (только HTTPS через
	// расшифровку), заглушка там — отказ secret_denied.
	if code, body := get("http://api.anthropic.test/v1", nil); code != 200 || body != "key=" {
		t.Fatal(code, body)
	}
	if code, body := get("http://api.anthropic.test/v1", map[string]string{"X-Api-Key": ph}); code != 403 || !strings.Contains(body, "secret_denied") {
		t.Fatal(code, body)
	}
	// Не в списке — отказ.
	if code, body := get("http://pypi.test/", nil); code != 403 || !strings.Contains(body, "denied_by_policy") {
		t.Fatal(code, body)
	}
	// ask_domains → запрос подтверждения → человек разрешает.
	go func() {
		for i := 0; i < 200; i++ {
			l, _ := d.State.ListApprovals(state.Pending, 1)
			if len(l) == 1 {
				d.Pipeline.Decide(context.Background(), l[0].ID, true, "test", "owner")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if code, body := get("http://code.github.test/repo", map[string]string{"X-Api-Key": ph}); code != 200 || body != "key="+ph {
		t.Fatal("ask domain:", code, body)
	}
	// Правило политики на egress.connect запрещает конкретный хост.
	if code, body := get("http://evil.github.test/", nil); code != 403 || !strings.Contains(body, "blocked repo host") {
		t.Fatal(code, body)
	}
	// Журнал соединений.
	logs, _ := d.State.EgressLog(10)
	if len(logs) < 4 || !strings.Contains(string(logs[len(logs)-1]), "api.anthropic.test") {
		t.Fatal(len(logs))
	}
	// Остановка: токен отозван, сокет удалён.
	r.Stop(run.ID)
	if _, err := d.State.AgentByToken(run.Token); err == nil {
		t.Fatal("run token survives")
	}
	if _, err := os.Stat(run.Socket); err == nil {
		t.Fatal("run socket survives")
	}
	if code, _ := get("http://api.anthropic.test/v1", nil); code != http.StatusProxyAuthRequired {
		t.Fatal("proxy creds survive the run", code)
	}
}

// Прокси в контейнере без сети: 127.0.0.1:порт контейнера → сокет → прокси на
// хосте. Проверяется сама пересылка: shim и мост на хосте.
func TestProxyBridgeAndShim(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	go func() {
		for {
			c, err := proxy.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b := make([]byte, 5)
				io.ReadFull(c, b)
				c.Write(append([]byte("echo:"), b...))
			}()
		}
	}()
	dir, _ := os.MkdirTemp("", "g4ab")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "egress.sock")
	stop, err := StartBridge(context.Background(), Bridge{Socket: sock, Target: proxy.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	l, err := ShimListen("127.0.0.1:0", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, 10)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "echo:hello" {
		t.Fatal(string(got), err)
	}
}

// Shim запускает команду агента и возвращает её код выхода.
func TestShimRunsCommand(t *testing.T) {
	if runtimeGOOS == "windows" {
		t.Skip("sh")
	}
	dir, _ := os.MkdirTemp("", "g4as")
	defer os.RemoveAll(dir)
	var out strings.Builder
	code := Shim(context.Background(), []string{"--listen", "127.0.0.1:0", "--socket", filepath.Join(dir, "none.sock"), "--", "sh", "-c", "echo hi; exit 3"},
		strings.NewReader(""), &out, io.Discard)
	if code != 3 || strings.TrimSpace(out.String()) != "hi" {
		t.Fatal(code, out.String())
	}
}

// Каталог данных daemon закрыт для чтения в srt и при нестандартном
// расположении (GATE4AI_HOME, XDG_STATE_HOME); сокет запуска внутри него
// остаётся доступным.
func TestSRTDeniesDataHome(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "run"), 0o700)
	os.WriteFile(filepath.Join(home, "keys.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(home, "vault.enc"), []byte("x"), 0o600)
	t.Setenv("GATE4AI_HOME", home)
	r := sampleRun()
	r.Socket = filepath.Join(home, "run", "agent-abc123.sock")
	set := SRTSettings(r, Options{Workspace: "/w", Home: "/home/u"})
	deny := set["filesystem"].(map[string]any)["denyRead"].([]string)
	j := strings.Join(deny, "\n")
	if !strings.Contains(j, filepath.Join(home, "keys.json")) || !strings.Contains(j, filepath.Join(home, "vault.enc")) || strings.Contains(j, filepath.Join(home, "run")) {
		t.Fatal(deny)
	}
	other := t.TempDir()
	r.Home = other
	deny = SRTSettings(r, Options{Workspace: "/w", Home: "/home/u"})["filesystem"].(map[string]any)["denyRead"].([]string)
	if !slices.Contains(deny, other) {
		t.Fatal("daemon home from the run is not denied:", deny)
	}
}

// errOf — error {code, message, hint} из конверта ответа.
func errOf(m map[string]any) map[string]any { e, _ := m["error"].(map[string]any); return e }
