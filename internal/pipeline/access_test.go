// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
)

// accBrowser — встроенный «Браузер» для теста: open, act (write), read;
// страница сеанса s1 — на app.example.org.
type accBrowser struct {
	settings map[string]any
	callers  []*abi.Caller
}

func (b *accBrowser) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "browser", Runtime: manifest.RuntimeBuiltin, Contract: 1, Services: []string{"open", "act"}, Actions: []manifest.Action{
		{Name: "open", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { url: { type: string }, session: { type: string } }, required: [url] }`)},
		{Name: "act", Class: "write", InputSchema: manifest.MustSchema(`{ type: object, properties: { session: { type: string } }, required: [session] }`)},
		{Name: "read", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { url: { type: string } }, required: [url] }`)},
		{Name: "close", Class: "read", InputSchema: manifest.MustSchema(`{ type: object, properties: { session: { type: string } } }`)},
	}}
}

func (b *accBrowser) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	b.settings = r.Settings
	b.callers = append(b.callers, r.Caller)
	return abi.ExecResult{Structured: map[string]any{"ok": true}}, nil
}

func (b *accBrowser) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func (b *accBrowser) PageURL(session, call string) string {
	if session == "s1" || call != "" {
		return "https://app.example.org/page"
	}
	if session == "s2" {
		return "https://other.test/page" // * ask
	}
	return ""
}

// browserClient — InProcess с PageURL (как wasm.Builtin).
type browserClient struct {
	*connectors.InProcess
	b *accBrowser
}

func (c browserClient) PageURL(session, call string) string { return c.b.PageURL(session, call) }

// accFiles — «Файлы»: read / append / write по path.
type accFiles struct{ settings map[string]any }

func (f *accFiles) Manifest() manifest.Manifest {
	obj := manifest.MustSchema(`{ type: object, properties: { path: { type: string } }, required: [path] }`)
	return manifest.Manifest{Connector: "files", Runtime: manifest.RuntimeBuiltin, Actions: []manifest.Action{
		{Name: "read", Class: "read", InputSchema: obj}, {Name: "list", Class: "read", InputSchema: obj},
		{Name: "append", Class: "write", InputSchema: obj}, {Name: "write", Class: "write", InputSchema: obj},
	}}
}

func (f *accFiles) Execute(_ context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	f.settings = r.Settings
	return abi.ExecResult{Structured: map[string]any{"ok": true}}, nil
}

func (f *accFiles) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

// shop — WASM-коннектор к сайту: его действие look открывает страницу через browser.
type shop struct {
	p   *Pipeline
	url string
	got error
}

func (s *shop) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "shop", Runtime: manifest.RuntimeWasm,
		Capabilities: manifest.Capabilities{Services: []manifest.ServiceCap{{Target: "browser", Contract: 1, Actions: []string{"open", "act"}}}},
		Sites:        []access.Site{{Domain: "shop.example", Access: access.Allow}, {Domain: "ask.example", Access: access.Ask}},
		Actions:      []manifest.Action{{Name: "look", Class: "write", InputSchema: manifest.MustSchema(`{ type: object, properties: {} }`), Calls: []string{"browser.open", "browser.act"}}}}
}

func (s *shop) Execute(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	_, s.got = s.p.ExecuteNested(ctx, "shop", manifest.ServiceRule{Target: "browser", Contract: 1, Actions: []string{"open", "act"}},
		abi.ServiceCallRequest{Target: "browser", Action: "open", Args: map[string]any{"url": s.url}})
	return abi.ExecResult{Structured: map[string]any{"ok": true}}, nil
}

func (s *shop) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{Status: abi.Verified}, nil
}

const accessPolicy = `
agents:
  bot: { profile: p }
  ruled: { profile: r }
access:
  sites:
    - { domain: "*", access: ask }
    - { domain: "*.example.org", access: allow }
    - { domain: shop.example, access: connector }
    - { domain: ask.example, access: connector }
    - { domain: evil.example, access: deny }
  folders:
    - { path: HOME/notes, access: append }
    - { path: HOME/ro, access: read }
    - { path: HOME/all, access: allow }
    - { path: HOME/closed, access: deny }
profiles:
  p:
    connectors: [files, browser, shop]
    rules:
      - { id: look-ok, action: shop.look, effect: allow }
  r:
    connectors: [files, browser]
    rules:
      - { id: all-ok, action: "browser.*", effect: allow }
      - { id: act-ok, action: browser.act, effect: allow }
      - { id: write-ask, action: files.write, effect: ask }
`

func accessEnv(t *testing.T) (*Pipeline, *accBrowser, *accFiles, *shop, string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	ks := keys.NewMemory()
	b, f, s := &accBrowser{}, &accFiles{}, &shop{}
	reg := connectors.NewRegistry()
	bc, err := connectors.NewInProcess(b)
	if err != nil {
		t.Fatal(err)
	}
	reg.Add(browserClient{bc, b})
	for _, c := range []connectors.Connector{f, s} {
		cli, err := connectors.NewInProcess(c)
		if err != nil {
			t.Fatal(err)
		}
		reg.Add(cli)
	}
	pol := strings.ReplaceAll(strings.ReplaceAll(accessPolicy, "HOME", home), `ro.example"`, "ro.example")
	cfg, err := config.Parse([]byte(pol), policy.Validator(reg.Catalog()))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.New(cfg, reg.Catalog())
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(filepath.Join(dir, "state.db"), ks)
	if err != nil {
		t.Fatal(err)
	}
	j, err := audit.Open(filepath.Join(dir, "journal.db"), filepath.Join(dir, "ev"), ks, audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(); j.Close() })
	p := New(reg, st, j, fakeSecrets{}, &fakeNotifier{}, eng)
	p.Home = home
	s.p = p
	return p, b, f, s, home
}

func TestAccessSites(t *testing.T) {
	p, b, _, _, _ := accessEnv(t)
	ctx := context.Background()
	run := func(agent, action string, args map[string]any) Result {
		return p.Execute(ctx, Call{Agent: agent, Action: action, Args: args})
	}
	cases := []struct {
		agent, action string
		args          map[string]any
		status, scope string
	}{
		{"bot", "browser.read", map[string]any{"url": "https://a.example.org/x"}, StatusOK, "site:a.example.org"},    // allow: *.example.org
		{"bot", "browser.read", map[string]any{"url": "https://example.org/"}, StatusOK, "site:example.org"},         // apex тоже
		{"bot", "browser.act", map[string]any{"session": "s1"}, StatusOK, "site:app.example.org"},                    // страница сеанса — allow
		{"bot", "browser.read", map[string]any{"url": "https://other.test/"}, StatusPending, "site:other.test"},      // * ask
		{"bot", "browser.read", map[string]any{"url": "https://shop.example/"}, StatusDenied, "site:shop.example"},   // connector: агенту напрямую нельзя
		{"bot", "browser.read", map[string]any{"url": "https://evil.example/"}, StatusDenied, "site:evil.example"},   // deny
		{"bot", "browser.close", map[string]any{}, StatusOK, ""},                                                     // без сайта — класс read
		{"ruled", "browser.read", map[string]any{"url": "https://other.test/"}, StatusOK, "site:other.test"},         // явное правило allow побеждает ask списка
		{"ruled", "browser.read", map[string]any{"url": "https://evil.example/"}, StatusDenied, "site:evil.example"}, // deny списка — всегда
	}
	for _, c := range cases {
		res := run(c.agent, c.action, c.args)
		if res.Status != c.status {
			t.Errorf("%s %s %v: %s %s (%s)", c.agent, c.action, c.args, res.Status, res.Reason, res.Message)
			continue
		}
		if c.status == StatusPending {
			a, _ := p.State.GetApproval(res.ID)
			if a.Decision.Scope != c.scope {
				t.Errorf("%s: scope %q, want %q", c.action, a.Decision.Scope, c.scope)
			}
		}
	}
	// Браузер получает сайты, куда вообще можно обратиться: всё, кроме deny.
	if b.settings["mode"] != "blocklist" || len(b.settings["blocked"].([]any)) != 1 || b.settings["blocked"].([]any)[0] != "evil.example" {
		t.Fatal(b.settings)
	}
	// Класс действия для сайта не важен: на allow-сайте действие класса write
	// проходит без вопроса, на ask-сайте и чтение ждёт владельца.
	if res := run("bot", "browser.act", map[string]any{"session": "s1"}); res.Status != StatusOK {
		t.Fatal(res)
	}
	if res := run("bot", "browser.act", map[string]any{"session": "s2"}); res.Status != StatusPending {
		t.Fatal(res)
	}
	// Режим read у сайта — ошибка конфигурации (он только у папок).
	if _, err := config.Parse([]byte("access:\n  sites:\n    - { domain: ro.example, access: read }\n")); err == nil || !strings.Contains(err.Error(), "allow | connector | ask | deny") {
		t.Fatal("site read accepted:", err)
	}
}

func TestAccessFolders(t *testing.T) {
	p, _, f, _, home := accessEnv(t)
	ctx := context.Background()
	run := func(agent, action, path string) Result {
		return p.Execute(ctx, Call{Agent: agent, Action: action, Args: map[string]any{"path": path}})
	}
	for _, c := range []struct{ agent, action, path, status string }{
		{"bot", "files.read", home + "/notes/a.md", StatusOK},
		{"bot", "files.append", home + "/notes/a.md", StatusOK},
		{"bot", "files.write", home + "/notes/a.md", StatusPending},
		{"bot", "files.read", home + "/ro/a.md", StatusOK},
		{"bot", "files.append", home + "/ro/a.md", StatusPending},
		{"bot", "files.write", home + "/all/a.md", StatusOK},
		{"bot", "files.read", home + "/closed/a.md", StatusDenied},
		{"bot", "files.read", home + "/elsewhere/a.md", StatusDenied},
		{"bot", "files.read", "/etc/passwd", StatusDenied},
		{"ruled", "files.write", home + "/all/a.md", StatusPending}, // явное правило ask побеждает allow списка
	} {
		if res := run(c.agent, c.action, c.path); res.Status != c.status {
			t.Errorf("%s %s %s: %s %s", c.agent, c.action, c.path, res.Status, res.Reason)
		}
	}
	if roots := f.settings["roots"].([]any); len(roots) != 3 {
		t.Fatal("roots are the open folders:", roots)
	}
	res := run("bot", "files.write", home+"/notes/a.md")
	a, _ := p.State.GetApproval(res.ID)
	if a.Decision.Scope != "folder:"+filepath.Join(home, "notes") {
		t.Fatal(a.Decision.Scope)
	}
}

func TestAccessNested(t *testing.T) {
	p, b, _, s, _ := accessEnv(t)
	ctx := context.Background()
	look := func(url string) (Result, error) {
		s.url = url
		return p.Execute(ctx, Call{Agent: "bot", Action: "shop.look"}), s.got
	}
	// Сайт коннектора allow, в Браузере — connector: вызов разрешён.
	if res, err := look("https://shop.example/"); res.Status != StatusOK || err != nil {
		t.Fatal(res, err)
	}
	if c := b.callers[len(b.callers)-1]; c == nil || c.Connector != "shop" || len(c.Params["domains"].([]any)) != 2 {
		t.Fatalf("browser gets the connector's open sites: %+v", c)
	}
	// Сайта нет в списке коннектора — отказ, даже если Браузер его разрешает.
	if _, err := look("https://a.example.org/"); err == nil || !strings.Contains(err.Error(), "does not list site") {
		t.Fatal(err)
	}
	// Сайт закрыт в Браузере — отказ, что бы ни говорил коннектор.
	if _, err := look("https://evil.example/"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatal(err)
	}
	// Строка коннектора ask: подтверждение внутри вызова; одобрено — вызов идёт.
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 200; i++ {
			pend, _ := p.State.ListApprovals(state.Pending, 5)
			for _, a := range pend {
				if a.Action == "browser.open" {
					_, err := p.Decide(ctx, a.ID, true, "webui", "owner")
					done <- err
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		done <- context.DeadlineExceeded
	}()
	if res, err := look("https://ask.example/"); res.Status != StatusOK || err != nil {
		t.Fatal(res, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
