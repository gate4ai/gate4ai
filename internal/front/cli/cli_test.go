// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/skill"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

type mailFake struct{}

func (mailFake) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "email", Actions: []manifest.Action{
		{Name: "send", Class: "external", Description: "Send an email", Verify: "sent_folder", InputSchema: manifest.MustSchema(`{ type: object, properties: { to: { type: array, items: { type: string, format: email } }, subject: { type: string }, body: { type: string }, urgent: { type: boolean } }, required: [to, subject] }`)},
		{Name: "read", Class: "read", Description: "Read inbox", InputSchema: manifest.MustSchema(`{ type: object, properties: { limit: { type: integer, default: 10 } } }`)},
	}}
}
func (mailFake) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{Structured: map[string]any{"echo": r.Args}, ResultRef: "<id@x>"}, nil
}
func (mailFake) Verify(_ context.Context, r abi.VerifyRequest) (abi.VerifyResult, error) {
	if strings.Contains(r.Args["subject"].(string), "lost") {
		return abi.VerifyResult{Status: abi.NotConfirmed, Method: "sent_folder", Detail: "not in Sent"}, nil
	}
	return abi.VerifyResult{Status: abi.Verified, Method: "sent_folder"}, nil
}

const policy = `
agents:
  claude-ai: { profile: assistant }
  drafts: { profile: drafts-only }
lists:
  contacts_trusted: ["landlord@example.com"]
profiles:
  assistant:
    connectors: [email]
    rules:
      - { id: trusted, action: email.send, when: "to.all(r, r in lists.contacts_trusted)", effect: allow }
  drafts-only:
    connectors: [email]
    rules:
      - { id: no-send, action: email.send, effect: deny, reason: "drafts only" }
`

type harness struct {
	owner, agentSock string
	out, errb        *bytes.Buffer
}

func start(t *testing.T) *harness {
	t.Helper()
	c, _ := connectors.NewInProcess(mailFake{})
	return startWith(t, policy, c)
}

// startWith — daemon с этой политикой и коннекторами.
func startWith(t *testing.T, policy string, conns ...connectors.Client) *harness {
	t.Helper()
	home, _ := os.MkdirTemp("", "g4acli")
	t.Cleanup(func() { os.RemoveAll(home) })
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), InitialConfig: []byte(policy), Keys: keys.NewMemory(), Connectors: conns})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{owner: filepath.Join(home, "o.sock"), agentSock: filepath.Join(home, "a.sock")}
	go d.Serve(ctx, h.owner, h.agentSock)
	t.Cleanup(func() { cancel(); d.Close() })
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(h.agentSock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h
}

func (h *harness) run(t *testing.T, token string, args ...string) (int, string) {
	t.Helper()
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	env := &Env{Stdin: strings.NewReader("s3cret-value\n"), Stdout: out, Stderr: errb,
		Getenv: func(k string) string {
			switch k {
			case "GATE4AI_TOKEN":
				return token
			case "PASS":
				return "pw"
			}
			return ""
		},
		Connect: func(tok string) *Client {
			if tok != "" {
				return NewSocketClient(h.agentSock, tok)
			}
			return NewSocketClient(h.owner, "")
		}}
	code := Run(context.Background(), env, args)
	return code, out.String() + errb.String()
}

func jsonOf(t *testing.T, s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %s", s)
	}
	return m
}

func TestCLIFlow(t *testing.T) {
	h := start(t)
	code, out := h.run(t, "", "agents", "token", "claude-ai", "--label", "test")
	if code != 0 {
		t.Fatal(out)
	}
	tok := jsonOf(t, out)["token"].(string)
	_, out = h.run(t, "", "agents", "token", "drafts")
	dtok := jsonOf(t, out)["token"].(string)

	// Команда владельца с токеном агента: в подсказке — готовая команда для человека.
	if code, out := h.run(t, tok, "vault", "set", "@site.password"); code == 0 || !strings.Contains(out, "env -u GATE4AI_TOKEN gate4ai vault set @site.password") {
		t.Fatal("owner_only hint:", code, out)
	}
	if code, out := h.run(t, tok, "vault", "set", "@site.password"); code != ExitAuth {
		t.Fatal("owner_only: exit 7", code, out)
	}
	// Владелец без --as: agent_required — код 7 и подсказка про токен агента.
	if code, out := h.run(t, "", "mail", "send", "--to", "landlord@example.com", "--subject", "x"); code != ExitAuth ||
		errOf(jsonOf(t, out))["code"] != "agent_required" || !strings.Contains(errOf(jsonOf(t, out))["hint"].(string), "--as <agent>") {
		t.Fatal("agent_required:", code, out)
	}
	// Строковый аргумент из файла (--<name>_file) и из stdin (-).
	f := filepath.Join(t.TempDir(), "subject.txt")
	os.WriteFile(f, []byte("from file"), 0o600)
	if code, out := h.run(t, tok, "mail", "send", "--to", "landlord@example.com", "--subject_file", f); code != ExitOK || !strings.Contains(out, `"subject": "from file"`) {
		t.Fatal("--subject_file:", code, out)
	}
	if code, out := h.run(t, tok, "mail", "send", "--to", "landlord@example.com", "--subject_file", "-"); code != ExitOK || !strings.Contains(out, `"subject": "s3cret-value"`) {
		t.Fatal("--subject_file -:", code, out)
	}
	if code, out := h.run(t, tok, "mail", "send", "--to", "landlord@example.com", "--subject_file", "/nonexistent"); code != ExitUsage || errOf(jsonOf(t, out))["code"] != "invalid_arguments" {
		t.Fatal("missing file:", code, out)
	}
	// Каталог и skill.
	code, out = h.run(t, tok, "tools", "--json")
	if code != 0 || !strings.Contains(out, `"email.send"`) {
		t.Fatal(out)
	}
	code, out = h.run(t, tok, "skill")
	if code != 0 || !strings.Contains(out, "gate4ai email send --to name@example.com") {
		t.Fatal(out)
	}
	code, out = h.run(t, tok, "mail", "send", "--help")
	if code != 0 || !strings.Contains(out, "--to") || !strings.Contains(out, "repeatable") {
		t.Fatal(out)
	}

	// Разрешено и проверено (алиас mail → email, повтор --to, булев флаг).
	code, out = h.run(t, tok, "mail", "send", "--to", "landlord@example.com", "--subject", "rent", "--urgent")
	if code != ExitOK || jsonOf(t, out)["verified"] != true {
		t.Fatal(code, out)
	}
	// Проверка не прошла → код 4.
	code, out = h.run(t, tok, "email", "send", "--to=landlord@example.com", "--subject", "lost mail")
	if code != ExitVerification || errOf(jsonOf(t, out))["code"] != "verification_failed" {
		t.Fatal(code, out)
	}
	// Отказ → код 2 и честная причина.
	code, out = h.run(t, dtok, "email", "send", "--to", "landlord@example.com", "--subject", "x")
	if code != ExitDenied || !strings.Contains(errOf(jsonOf(t, out))["message"].(string), "denied_by_policy: drafts only") {
		t.Fatal(code, out)
	}
	// Подтверждение → код 3, затем approve владельцем и status.
	code, out = h.run(t, tok, "email", "send", "--to", "a@b.example,c@d.example", "--subject", "hi")
	res := jsonOf(t, out)
	if code != ExitPending || errOf(res)["code"] != "pending_approval" {
		t.Fatal(code, out)
	}
	id := res["id"].(string)
	code, out = h.run(t, "", "approvals")
	if code != 0 || !strings.Contains(out, id) {
		t.Fatal(out)
	}
	if code, out = h.run(t, tok, "approvals", "approve", id); code == 0 {
		t.Fatal("agent approved its own request", out)
	}
	if code, out = h.run(t, "", "approvals", "approve", id); code != 0 {
		t.Fatal(out)
	}
	code, out = h.run(t, tok, "status", id, "--wait", "2s")
	if code != ExitOK || jsonOf(t, out)["status"] != "ok" {
		t.Fatal(code, out)
	}
	// Ошибки использования.
	if code, _ = h.run(t, tok, "email", "send", "--bcc", "x"); code != ExitUsage {
		t.Fatal("unknown arg", code)
	}
	if code, _ = h.run(t, tok, "email", "send", "--subject", "no to"); code != ExitUsage {
		t.Fatal("missing required", code)
	}
	if code, _ = h.run(t, tok, "nosuch", "thing"); code != ExitUsage {
		t.Fatal("unknown connector", code)
	}
	// Владельческие команды агенту недоступны.
	if code, _ = h.run(t, tok, "log"); code == 0 {
		t.Fatal("agent read the log")
	}
	// Журнал, show, export, verify.
	code, out = h.run(t, "", "log")
	if code != 0 || strings.Count(out, "\n") != 6 {
		t.Fatal(out)
	}
	hash := strings.Fields(out)[0]
	if code, out = h.run(t, "", "show", hash); code != 0 || !strings.Contains(out, `"prev_hash"`) {
		t.Fatal(out)
	}
	arch := filepath.Join(t.TempDir(), "j.tar.gz")
	if code, out = h.run(t, "", "export", "-o", arch, "--passphrase-env", "PASS"); code != 0 {
		t.Fatal(out)
	}
	code, out = h.run(t, "", "verify", arch, "--passphrase-env", "PASS")
	if code != 0 || jsonOf(t, out)["ok"] != true {
		t.Fatal(out)
	}
	if code, out = h.run(t, "", "journal", "verify"); code != 0 {
		t.Fatal(out)
	}
	// Политика: тест, версии.
	code, out = h.run(t, "", "policy", "test", "--agent", "drafts", "--action", "email.send", "--args", `{"to":["x@y.z"],"subject":"s"}`)
	if code != 0 || jsonOf(t, out)["effect"] != "deny" {
		t.Fatal(out)
	}
	draft := filepath.Join(t.TempDir(), "p.yaml")
	os.WriteFile(draft, []byte(strings.Replace(policy, "effect: deny", "effect: allow", 1)), 0o600)
	code, out = h.run(t, "", "policy", "test", "--agent", "drafts", "--action", "email.send", "--args", `{"to":["x@y.z"],"subject":"s"}`, "--file", draft)
	if code != 0 || jsonOf(t, out)["effect"] != "allow" {
		t.Fatal(out)
	}
	if code, out = h.run(t, "", "policy", "set", draft, "-m", "open drafts"); code != 0 {
		t.Fatal(out)
	}
	if code, out = h.run(t, "", "policy", "show"); code != 0 || !strings.HasPrefix(out, "# version 2") {
		t.Fatal(out)
	}
	// Vault: значение из stdin, наружу — только алиасы.
	if code, out = h.run(t, "", "vault", "set", "mail.work"); code != 0 {
		t.Fatal(out)
	}
	code, out = h.run(t, "", "vault", "list")
	if code != 0 || !strings.Contains(out, "@mail.work") || strings.Contains(out, "s3cret") {
		t.Fatal(out)
	}
	// Отзыв.
	h.run(t, "", "agents", "revoke", "claude-ai")
	if code, _ = h.run(t, tok, "tools"); code == 0 {
		t.Fatal("revoked token still works")
	}
}

func TestDaemonUnavailable(t *testing.T) {
	env := &Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Getenv: func(string) string { return "" },
		Connect: func(string) *Client { return NewSocketClient(filepath.Join(t.TempDir(), "none.sock"), "") }}
	if code := Run(context.Background(), env, []string{"tools"}); code != ExitUnavailable {
		t.Fatal(code)
	}
}

// --json, --wait и --as — опции вызова в любом месте команды действия, если у
// действия нет аргумента с таким именем; --json без значения — вывод JSON.
func TestCallOptsAnywhere(t *testing.T) {
	tool := skill.Tool{Name: "files.list", Connector: "files", Action: "list",
		Schema: manifest.MustSchema(`{ type: object, properties: { path: { type: string }, as: { type: string } } }`)}
	vals, o, err := parseArgs(tool, []string{"--path", "/x", "--json", "--wait", "1m"}, nil)
	if err != nil || vals["path"] != "/x" || !o.json || o.wait != "1m" {
		t.Fatal(vals, o, err)
	}
	vals, o, err = parseArgs(tool, []string{"--json", "--path", "/x"}, nil)
	if err != nil || vals["path"] != "/x" || !o.json {
		t.Fatal(vals, o, err)
	}
	// --json со значением — по-прежнему все аргументы объектом.
	vals, o, err = parseArgs(tool, []string{"--json", `{"path":"/y"}`}, nil)
	if err != nil || vals["path"] != "/y" || o.json {
		t.Fatal(vals, o, err)
	}
	// Аргумент действия с именем опции остаётся аргументом.
	vals, o, err = parseArgs(tool, []string{"--as", "pdf"}, nil)
	if err != nil || vals["as"] != "pdf" || o.as != "" {
		t.Fatal(vals, o, err)
	}
	if _, _, err := parseArgs(tool, []string{"--wait"}, nil); err == nil || !strings.Contains(err.Error(), "10m") {
		t.Fatal(err)
	}
	rest, pre := hoistCallOpts([]string{"--json", "--wait", "5m", "--as=bot", "list", "--path", "/x"})
	if strings.Join(rest, " ") != "list --path /x" || strings.Join(pre, " ") != "--json --wait 5m --as=bot" {
		t.Fatal(rest, pre)
	}

	h := start(t)
	_, out := h.run(t, "", "agents", "token", "claude-ai")
	tok := jsonOf(t, out)["token"].(string)
	for _, args := range [][]string{
		{"email", "read", "--limit", "2", "--json"},
		{"email", "--json", "read", "--limit", "2"},
		{"--json", "email", "read", "--limit", "2"},
	} {
		code, out := h.run(t, tok, args...)
		if code != ExitOK || jsonOf(t, out)["status"] != "ok" {
			t.Fatal(args, code, out)
		}
	}
}

// errOf — error {code, message, hint} из конверта ответа.
func errOf(m map[string]any) map[string]any { e, _ := m["error"].(map[string]any); return e }
