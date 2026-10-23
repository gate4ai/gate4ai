// SPDX-License-Identifier: AGPL-3.0-only

package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
)

// Семантика Request.Nested на уровне движка: ask не действует (подвызов покрыт
// подтверждением родителя), без правила — allow, deny действует, профиль не
// проверяется, лимиты действуют.
func TestNestedSemantics(t *testing.T) {
	y := `
agents: {a: {profile: p}}
profiles:
  p:
    connectors: [mail]
    rules:
      - {id: ask-open, action: browser.open, effect: ask}
      - {id: deny-act, action: browser.act, effect: deny, reason: no clicks}
    limits:
      - {id: open-cap, action: browser.open, per_day: 2}
`
	e := mustEngine(t, y, nil)
	d := e.Decide(Request{Agent: "a", Action: "browser.open", Class: ClassExternal, Args: map[string]any{}, Nested: true}, fakeCounters{})
	if d.Effect != config.EffectAllow || !d.Default {
		t.Fatalf("nested ask must not apply, profile not checked: %+v", d)
	}
	d = e.Decide(Request{Agent: "a", Action: "browser.read", Class: ClassExternal, Args: map[string]any{}, Nested: true}, fakeCounters{})
	if d.Effect != config.EffectAllow {
		t.Fatalf("nested without rule: %+v", d)
	}
	d = e.Decide(Request{Agent: "a", Action: "browser.act", Class: ClassWrite, Args: map[string]any{}, Nested: true}, fakeCounters{})
	if d.Effect != config.EffectDeny || d.RuleIDs[0] != "deny-act" {
		t.Fatalf("nested deny must apply: %+v", d)
	}
	d = e.Decide(Request{Agent: "a", Action: "browser.open", Class: ClassExternal, Args: map[string]any{}, Nested: true}, fakeCounters{count: 2})
	if d.Effect != config.EffectDeny || d.RuleIDs[0] != "open-cap" {
		t.Fatalf("nested limit must apply: %+v", d)
	}
	// Не вложенный вызов того же действия: коннектора нет в профиле — deny.
	d = e.Decide(Request{Agent: "a", Action: "browser.open", Class: ClassExternal, Args: map[string]any{}}, fakeCounters{})
	if d.Effect != config.EffectDeny || d.RuleIDs[0] != "connector-not-in-profile" {
		t.Fatalf("direct call outside profile: %+v", d)
	}
}

type errCounters struct {
	fakeCounters
	err error
}

func (c errCounters) Err() error { return c.err }

// Ошибка State Store — deny, а не нулевые счётчики или «меток нет».
func TestStateErrorFailsClosed(t *testing.T) {
	y := `
agents: {a: {profile: p}}
profiles:
  p:
    connectors: [x]
    rules: [{id: ok, action: "*", effect: allow}]
`
	e := mustEngine(t, y, nil)
	d := e.Decide(Request{Agent: "a", Action: "x.do", Class: ClassRead, Args: map[string]any{}}, errCounters{err: errors.New("database is locked")})
	if d.Effect != config.EffectDeny || d.RuleIDs[0] != RuleStateUnavailable || !strings.Contains(d.Reason, "database is locked") {
		t.Fatalf("counters error: %+v", d)
	}
	d = e.Decide(Request{Agent: "a", Action: "x.do", Class: ClassRead, Args: map[string]any{}, StateError: errors.New("taint: io")}, fakeCounters{})
	if d.Effect != config.EffectDeny || d.RuleIDs[0] != RuleStateUnavailable {
		t.Fatalf("provenance error: %+v", d)
	}
	d = e.Decide(Request{Agent: "a", Action: "x.do", Class: ClassRead, Args: map[string]any{}}, errCounters{})
	if d.Effect != config.EffectAllow {
		t.Fatalf("no error: %+v", d)
	}
}

// Неверный шаблон glob: литерал — ошибка компиляции, значение из аргумента —
// ошибка вычисления (deny срабатывает), а не молчаливое false.
func TestGlobBadPattern(t *testing.T) {
	y := "agents: {a: {profile: p}}\nprofiles: {p: {connectors: [files], rules: [{id: r, action: files.read, effect: deny, when: \"glob(path, '/x/[abc')\"}]}}\n"
	if _, err := config.Parse([]byte(y), Validator(Catalog{"files.read": {"path"}})); err == nil || !strings.Contains(err.Error(), "bad pattern") {
		t.Fatalf("literal bad pattern must not compile: %v", err)
	}
	e := mustEngine(t, `
agents: {a: {profile: p}}
profiles:
  p:
    connectors: [files]
    rules:
      - {id: ok, action: "*", effect: allow}
      - {id: no-pat, action: files.read, when: "glob(path, pat)", effect: deny}
`, nil)
	d := e.Decide(Request{Agent: "a", Action: "files.read", Class: ClassRead, Args: map[string]any{"path": "/x/a", "pat": "/x/[abc"}}, nil)
	if d.Effect != config.EffectDeny || len(d.Errors) != 1 {
		t.Fatalf("runtime bad pattern must fail closed: %+v", d)
	}
}

// Регистр на macOS/Windows и симлинки: решение по настоящему пути.
func TestGlobRealPath(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "vault", "private")
	if err := os.MkdirAll(priv, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "vault", "link")
	if err := os.Symlink(priv, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	pat := filepath.ToSlash(filepath.Join(dir, "vault", "private")) + "/**"
	if !Glob(filepath.Join(link, "secret.md"), pat) {
		t.Fatal("path through a symlink must match the real directory's rule")
	}
	if !Glob(filepath.Join(link, "new", "file.md"), pat) {
		t.Fatal("not yet existing file under a symlink")
	}
	if Glob(filepath.Join(dir, "vault", "other", "x"), pat) {
		t.Fatal("other directory must not match")
	}
	old := foldCase
	defer func() { foldCase = old }()
	foldCase = true
	if !Glob("/home/u/Documents/Obsidian/Private/x", "~/Documents/obsidian/private/**") {
		t.Fatal("case-insensitive FS: case must not matter")
	}
	foldCase = false
	if Glob("/home/u/Documents/Obsidian/Private/x", "~/Documents/obsidian/private/**") {
		t.Fatal("case-sensitive FS")
	}
}

// Агентский профиль с browser.* → allow и change_policy → allow: правка
// политики всё равно у владельца, запись на сайтах и вход — только по правилу,
// которое называет действие; deny действует как прежде.
func TestOwnerOnlyAndExplicitOnly(t *testing.T) {
	y := `
agents: {a: {profile: connector-dev}, b: {profile: strict}}
profiles:
  connector-dev:
    connectors: [browser]
    rules:
      - {id: dev-browser-allow, action: "browser.*", effect: allow}
      - {id: gate4ai-change_policy-allow, action: gate4ai.change_policy, effect: allow}
      - {id: get-named, action: browser.delete, when: 'domain_matches(url, ["api.example"])', effect: allow}
  strict:
    connectors: [browser]
    rules:
      - {id: all, action: "*", effect: allow}
      - {id: no-self, action: gate4ai.change_policy, effect: deny}
`
	e := mustEngine(t, y, nil)
	dec := func(agent, action, class string, args map[string]any) Decision {
		if args == nil {
			args = map[string]any{}
		}
		return e.Decide(Request{Agent: agent, Action: action, Class: class, Args: args}, fakeCounters{})
	}
	if d := dec("a", "gate4ai.change_policy", ClassWrite, nil); d.Effect != config.EffectAsk || !strings.Contains(d.Reason, "always needs the owner") {
		t.Fatalf("change_policy: %+v", d)
	}
	if d := dec("b", "gate4ai.change_policy", ClassWrite, nil); d.Effect != config.EffectDeny {
		t.Fatalf("deny still forbids: %+v", d)
	}
	for _, act := range []string{"browser.post", "browser.put", "browser.patch", "browser.delete", "browser.login", "browser.fill_secret"} {
		if d := dec("a", act, ClassWrite, map[string]any{"url": "https://other.example/x"}); d.Effect != config.EffectAsk || !strings.Contains(d.Reason, "does not cover") {
			t.Fatalf("%s under browser.*: %+v", act, d)
		}
		if d := dec("b", act, ClassWrite, map[string]any{"url": "https://other.example/x"}); d.Effect != config.EffectAsk {
			t.Fatalf("%s under *: %+v", act, d)
		}
	}
	if d := dec("a", "browser.delete", ClassWrite, map[string]any{"url": "https://api.example/items/1"}); d.Effect != config.EffectAllow || d.RuleIDs[0] != "get-named" {
		t.Fatalf("a rule that names the action allows it: %+v", d)
	}
	for _, act := range []string{"browser.get", "browser.act", "browser.open"} {
		if d := dec("a", act, ClassWrite, map[string]any{"url": "https://x.example/"}); d.Effect != config.EffectAllow {
			t.Fatalf("%s still allowed by browser.*: %+v", act, d)
		}
	}
	ws := strings.Join(Warnings(e.Config()), "\n")
	for _, want := range []string{"profiles.connector-dev rule gate4ai-change_policy-allow: allow has no effect on gate4ai.change_policy",
		"profiles.connector-dev rule dev-browser-allow: browser.* does not cover browser.delete", "profiles.strict rule all: * does not cover"} {
		if !strings.Contains(ws, want) {
			t.Fatalf("warnings %q lack %q", ws, want)
		}
	}
	if strings.Contains(ws, "rule get-named") || strings.Contains(ws, "rule no-self") {
		t.Fatal(ws)
	}
}
