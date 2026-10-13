// SPDX-License-Identifier: FSL-1.1-ALv2

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/sdk-go/manifest"
)

type notes struct{ n int }

func (x *notes) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "notes", Actions: []manifest.Action{
		{Name: "append", Class: "write", Args: []manifest.Arg{{Name: "text", Type: "string", Required: true}}}}}
}
func (x *notes) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	x.n++
	return abi.ExecResult{Result: "ok"}, nil
}
func (x *notes) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

const cfg = "agents:\n  bot: {profile: p}\nprofiles:\n  p:\n    connectors: [notes]\n"

func setup(t *testing.T) (*UI, *daemon.Daemon, *notes, string) {
	home, _ := os.MkdirTemp("", "g4aui")
	t.Cleanup(func() { os.RemoveAll(home) })
	n := &notes{}
	c, _ := connectors.NewInProcess(n)
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	u := New(d)
	l, err := u.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: u.Handler()}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return u, d, n, "http://" + u.Addr
}

func body(t *testing.T, r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

func TestWebUI(t *testing.T) {
	u, d, n, base := setup(t)
	if _, err := u.Listen("0.0.0.0:0"); err == nil {
		t.Fatal("must refuse non-loopback")
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	r, _ := c.Get(base + "/")
	if r.StatusCode != 401 || !strings.Contains(body(t, r), "gate4ai ui") {
		t.Fatal("no session must show login hint")
	}
	if r, _ := c.Get(base + "/login?t=bogus"); r.StatusCode != 401 {
		t.Fatal("bogus login")
	}
	login := u.LoginURL()
	r, _ = c.Get(login)
	if r.StatusCode != 200 || r.Request.URL.Path != "/" {
		t.Fatal("login failed", r.StatusCode)
	}
	page := body(t, r)
	csrf := csrfRe.FindStringSubmatch(page)[1]
	// Повторное использование ссылки входа.
	c2 := &http.Client{}
	if r, _ := c2.Get(login); r.StatusCode != 401 {
		t.Fatal("login link reused")
	}
	// DNS rebinding: чужой Host.
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.Host = "evil.example:80"
	if r, _ := c.Do(req); r.StatusCode != http.StatusMisdirectedRequest {
		t.Fatal("foreign host accepted", r.StatusCode)
	}

	// Подтверждение из Web UI.
	res := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "hello"}})
	r, _ = c.Get(base + "/")
	if p := body(t, r); !strings.Contains(p, "bot") || !strings.Contains(p, "text: hello") {
		t.Fatal("pending not shown")
	}
	if r, _ := c.PostForm(base+"/approvals/"+res.ID, url.Values{"decision": {"approve"}}); r.StatusCode != 403 {
		t.Fatal("missing csrf accepted")
	}
	req, _ = http.NewRequest("POST", base+"/approvals/"+res.ID, strings.NewReader(url.Values{"decision": {"approve"}, "csrf": {csrf}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := c.Do(req); r.StatusCode != 403 {
		t.Fatal("cross-origin accepted")
	}
	r, _ = c.PostForm(base+"/approvals/"+res.ID, url.Values{"decision": {"approve"}, "csrf": {csrf}})
	if p := body(t, r); n.n != 1 || !strings.Contains(p, "ok #") {
		t.Fatal("approve failed", n.n)
	}
	// Агенты: токен показывается один раз, код подключения, отзыв.
	r, _ = c.PostForm(base+"/agents/bot/token", url.Values{"csrf": {csrf}, "label": {"cli"}})
	p := body(t, r)
	tok := regexp.MustCompile(`g4a_[A-Za-z0-9_-]+`).FindString(p)
	if tok == "" {
		t.Fatal("token not shown")
	}
	if a, err := d.State.AgentByToken(tok); err != nil || a != "bot" {
		t.Fatal("token invalid")
	}
	r, _ = c.PostForm(base+"/agents/bot/connect", url.Values{"csrf": {csrf}})
	if !regexp.MustCompile(`[A-Z2-9]{4}-[A-Z2-9]{4}`).MatchString(body(t, r)) {
		t.Fatal("no connect code")
	}
	c.PostForm(base+"/agents/bot/revoke", url.Values{"csrf": {csrf}})
	if _, err := d.State.AgentByToken(tok); err == nil {
		t.Fatal("not revoked")
	}
	// Журнал и квитанция.
	r, _ = c.Get(base + "/journal")
	p = body(t, r)
	if !strings.Contains(p, "цела") || !strings.Contains(p, "notes.append") {
		t.Fatal("journal")
	}
	hash := regexp.MustCompile(`/journal/([0-9a-f]{64})`).FindStringSubmatch(p)[1]
	r, _ = c.Get(base + "/journal/" + hash)
	if !strings.Contains(body(t, r), `&#34;prev_hash&#34;`) {
		t.Fatal("receipt page")
	}
	// Политика: ошибка схемы со строкой, сохранение, симулятор, откат.
	r, _ = c.PostForm(base+"/policy", url.Values{"csrf": {csrf}, "yaml": {cfg + "    rules: [{id: r, action: notes.append, effect: maybe}]\n"}})
	if p := body(t, r); !strings.Contains(p, "line 6") || !strings.Contains(p, "unknown effect") {
		t.Fatal("schema error not shown", p)
	}
	r, _ = c.PostForm(base+"/policy", url.Values{"csrf": {csrf}, "yaml": {cfg + "    rules: [{id: r, action: notes.append, effect: allow}]\n"}, "comment": {"allow notes"}})
	if !strings.Contains(body(t, r), "saved version 2") {
		t.Fatal("save")
	}
	r, _ = c.PostForm(base+"/policy/simulate", url.Values{"csrf": {csrf}, "agent": {"bot"}, "action": {"notes.append"}, "args": {`{"text":"x"}`}})
	if p := body(t, r); !strings.Contains(p, `class="allow">allow`) {
		t.Fatal("simulate", p)
	}
	r, _ = c.PostForm(base+"/policy/rollback", url.Values{"csrf": {csrf}, "version": {"1"}})
	if !strings.Contains(body(t, r), "rolled back to 1") {
		t.Fatal("rollback")
	}
	// Секреты: значение не отображается.
	r, _ = c.PostForm(base+"/vault", url.Values{"csrf": {csrf}, "alias": {"@mail.work"}, "value": {"S3CRET-VALUE"}})
	if p := body(t, r); !strings.Contains(p, "@mail.work") || strings.Contains(p, "S3CRET") {
		t.Fatal("vault page")
	}
	// Выход.
	c.PostForm(base+"/logout", url.Values{"csrf": {csrf}})
	if r, _ := c.Get(base + "/agents"); r.StatusCode != 401 {
		t.Fatal("session survives logout")
	}
}

func TestOwnerRoutesOnSocket(t *testing.T) {
	u, d, _, _ := setup(t)
	d.AddRoutes(u.OwnerRoutes)
	home := t.TempDir()
	sock, asock := filepath.Join(home, "o.sock"), filepath.Join(home, "a.sock")
	if len(sock) > 100 {
		t.Skip("socket path too long")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, sock, asock)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(asock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	owner := ipc.HTTPClient(sock)
	r, err := owner.Post("http://gate4ai/v1/ui/session", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := body(t, r); r.StatusCode != 200 || !strings.Contains(p, "http://"+u.Addr+"/login?t=") {
		t.Fatal(r.StatusCode, p)
	}
	d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "x"}})
	r, _ = owner.Get("http://gate4ai/v1/tray/state")
	if p := body(t, r); !strings.Contains(p, `"pending":[{`) || !strings.Contains(p, `"agent":"bot"`) {
		t.Fatal(p)
	}
	// Агент с токеном не получает ссылку входа владельца.
	tok, _ := d.State.IssueToken("bot", "", "")
	req, _ := http.NewRequest("POST", "http://gate4ai/v1/ui/session", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if r, _ := ipc.HTTPClient(asock).Do(req); r.StatusCode == 200 {
		t.Fatal("agent got a login link")
	}
	if r, _ := ipc.HTTPClient(sock).Do(req); r.StatusCode != 403 {
		t.Fatal("agent token on owner socket got a login link", r.StatusCode)
	}
}

// Контракт трея: команды CLI, которые вызывает tray/src-tauri/src/main.rs.
func TestTrayContractViaCLI(t *testing.T) {
	u, d, n, _ := setup(t)
	d.AddRoutes(u.OwnerRoutes)
	home := t.TempDir()
	sock, asock := filepath.Join(home, "o.sock"), filepath.Join(home, "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, sock, asock)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(asock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	run := func(args ...string) (int, string) {
		var out bytes.Buffer
		env := &cli.Env{Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" },
			Connect: func(string) *cli.Client { return cli.NewSocketClient(sock, "") }}
		return cli.Run(ctx, env, args), out.String()
	}
	res := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "notes.append", Args: map[string]any{"text": "x"}})
	code, out := run("tray-state")
	var st struct {
		OK      bool `json:"ok"`
		Pending []struct {
			ID, Agent, Action, Hash, Summary, Reason string
		} `json:"pending"`
		Receipts int64 `json:"receipts"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || !st.OK || len(st.Pending) != 1 {
		t.Fatal(code, out)
	}
	p := st.Pending[0]
	if p.ID != res.ID || p.Agent != "bot" || p.Action != "notes.append" || !strings.HasPrefix(p.Hash, "#") || p.Summary == "" || p.Reason == "" {
		t.Fatalf("%+v", p)
	}
	if code, out := run("approvals", "approve", p.ID); code != 0 || n.n != 1 {
		t.Fatal(code, out)
	}
	if code, out := run("ui", "--print"); code != 0 || !strings.Contains(out, "/login?t=") {
		t.Fatal(code, out)
	}
}
