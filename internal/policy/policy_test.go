// SPDX-License-Identifier: FSL-1.1-ALv2

package policy

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
)

func init() { homeDir = func() string { return "/home/u" } }

type fakeCounters struct {
	spent  float64
	count  int
	amount float64
}

func (f fakeCounters) SpentToday() float64                { return f.spent }
func (f fakeCounters) CountToday(string, string) int      { return f.count }
func (f fakeCounters) AmountToday(string, string) float64 { return f.amount }

var exampleCatalog = Catalog{
	"email.send":        {"to", "cc", "subject", "body"},
	"email.read":        {"folder", "query", "limit"},
	"files.read":        {"path"},
	"files.append":      {"path", "text"},
	"files.write":       {"path", "content"},
	"browser.read":      {"url"},
	"payments.pay_bill": {"payee", "amount"},
}

func loadExample(t *testing.T) *Engine {
	t.Helper()
	b, err := os.ReadFile("testdata/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(b, Validator(exampleCatalog))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg, exampleCatalog)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestExamplePolicyTable(t *testing.T) {
	e := loadExample(t)
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	untrusted := map[string][]string{"to": {"untrusted_content"}}
	cases := []struct {
		name     string
		agent    string
		action   string
		class    string
		args     map[string]any
		prov     map[string][]string
		counters fakeCounters
		want     string
		rules    []string
		reason   string
	}{
		{"trusted recipient allowed", "claude-ai", "email.send", ClassExternal,
			map[string]any{"to": []any{"landlord@example.com"}, "subject": "s", "body": "b", "cc": []any{}}, nil, fakeCounters{},
			"allow", []string{"email-send-trusted"}, ""},
		{"untrusted recipient default ask", "claude-ai", "email.send", ClassExternal,
			map[string]any{"to": []any{"stranger@example.org"}, "subject": "s", "body": "b", "cc": []any{}}, nil, fakeCounters{},
			"ask", []string{}, "default for effect class"},
		{"mixed recipients ask", "claude-ai", "email.send", ClassExternal,
			map[string]any{"to": []any{"landlord@example.com", "x@evil.test"}, "subject": "s", "body": "b", "cc": []any{}}, nil, fakeCounters{},
			"ask", []string{}, ""},
		{"recipient from inbound content denied even if trusted", "claude-ai", "email.send", ClassExternal,
			map[string]any{"to": []any{"landlord@example.com"}, "subject": "s", "body": "b", "cc": []any{}}, untrusted, fakeCounters{},
			"deny", []string{"email-no-untrusted-recipients"}, "Адресат взят из входящего письма"},
		{"read inside obsidian", "claude-ai", "files.read", ClassRead,
			map[string]any{"path": "~/Documents/obsidian/notes/a.md"}, nil, fakeCounters{}, "allow", []string{"obsidian-read"}, ""},
		{"read outside obsidian falls to read default", "claude-ai", "files.read", ClassRead,
			map[string]any{"path": "/etc/passwd"}, nil, fakeCounters{}, "allow", []string{}, "default"},
		{"write is ask by default", "claude-ai", "files.write", ClassWrite,
			map[string]any{"path": "~/Documents/obsidian/a.md", "content": "x"}, nil, fakeCounters{}, "ask", []string{}, ""},
		{"append in obsidian", "claude-ai", "files.append", ClassWrite,
			map[string]any{"path": "/home/u/Documents/obsidian/log.md", "text": "x"}, nil, fakeCounters{}, "allow", []string{"obsidian-append"}, ""},
		{"append traversal is not obsidian", "claude-ai", "files.append", ClassWrite,
			map[string]any{"path": "~/Documents/obsidian/../../.bashrc", "text": "x"}, nil, fakeCounters{}, "ask", []string{}, ""},
		{"browser allowed domain", "claude-ai", "browser.read", ClassRead,
			map[string]any{"url": "https://www.gosuslugi.ru/x"}, nil, fakeCounters{}, "allow", []string{"browser-read-allowed"}, ""},
		{"browser lookalike domain uses default", "claude-ai", "browser.read", ClassRead,
			map[string]any{"url": "https://gosuslugi.ru.evil.test/"}, nil, fakeCounters{}, "allow", []string{}, "default"},
		{"utility payment asks with obligation", "claude-ai", "payments.pay_bill", ClassMoney,
			map[string]any{"payee": "@uk-dom", "amount": 5000.0}, nil, fakeCounters{spent: 1000}, "ask", []string{"utilities-payments"}, ""},
		{"daily cap beats ask", "claude-ai", "payments.pay_bill", ClassMoney,
			map[string]any{"payee": "@uk-dom", "amount": 5000.0}, nil, fakeCounters{spent: 28000}, "deny", []string{"daily-cap"}, "Дневной лимит 30 000 ₽"},
		{"unknown payee money default ask", "claude-ai", "payments.pay_bill", ClassMoney,
			map[string]any{"payee": "@casino", "amount": 100.0}, nil, fakeCounters{}, "ask", []string{}, ""},
		{"drafts-only cannot send", "openclaw-cloud", "email.send", ClassExternal,
			map[string]any{"to": []any{"landlord@example.com"}, "subject": "s", "body": "b", "cc": []any{}}, nil, fakeCounters{},
			"deny", []string{"no-send"}, "rule no-send: deny"},
		{"drafts-only has no files connector", "openclaw-cloud", "files.read", ClassRead,
			map[string]any{"path": "~/x"}, nil, fakeCounters{}, "deny", []string{"connector-not-in-profile"}, "not available"},
		{"files-obsidian denies outside", "claude-code", "files.read", ClassRead,
			map[string]any{"path": "~/.ssh/id_ed25519"}, nil, fakeCounters{}, "deny", []string{"obsidian-only"}, ""},
		{"files-obsidian reads inside by default", "claude-code", "files.read", ClassRead,
			map[string]any{"path": "~/Documents/obsidian/x.md"}, nil, fakeCounters{}, "allow", []string{}, ""},
		{"unknown agent", "rogue", "files.read", ClassRead, map[string]any{"path": "x"}, nil, fakeCounters{}, "deny", []string{"agent-unknown"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := e.Decide(Request{Agent: tc.agent, Action: tc.action, Class: tc.class, Args: tc.args, Provenance: tc.prov, Now: now}, tc.counters)
			if d.Effect != tc.want {
				t.Fatalf("effect = %s, want %s (%+v)", d.Effect, tc.want, d)
			}
			if !reflect.DeepEqual(d.RuleIDs, tc.rules) && !(len(d.RuleIDs) == 0 && len(tc.rules) == 0) {
				t.Fatalf("rule_ids = %v, want %v", d.RuleIDs, tc.rules)
			}
			if !strings.Contains(d.Reason, tc.reason) {
				t.Fatalf("reason = %q, want contains %q", d.Reason, tc.reason)
			}
			if len(d.Errors) > 0 {
				t.Fatalf("unexpected eval errors: %v", d.Errors)
			}
		})
	}
	d := e.Decide(Request{Agent: "claude-ai", Action: "payments.pay_bill", Class: ClassMoney, Args: map[string]any{"payee": "@uk-dom", "amount": 10.0}, Now: now}, nil)
	if !reflect.DeepEqual(d.Obligations, []string{"verify_statement"}) {
		t.Fatalf("obligations: %v", d.Obligations)
	}
}

func TestDefaultsByClass(t *testing.T) {
	for class, want := range map[string]string{ClassRead: "allow", ClassWrite: "ask", ClassExternal: "ask", ClassMoney: "ask", "": "ask"} {
		if got := DefaultEffect(class); got != want {
			t.Errorf("%s: %s", class, got)
		}
	}
}

func mustEngine(t *testing.T, y string, cat Catalog) *Engine {
	t.Helper()
	cfg, err := config.Parse([]byte(y), Validator(cat))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg, cat)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestOrderDenyAskAllow(t *testing.T) {
	y := `
agents: {a: {profile: p}}
profiles:
  p:
    connectors: [x]
    rules:
      - {id: r-allow, action: x.do, effect: allow}
      - {id: r-ask, action: "x.*", when: "n > 1", effect: ask}
      - {id: r-deny, action: "*", when: "n > 2", effect: deny, reason: too many}
`
	e := mustEngine(t, y, nil)
	for n, want := range map[float64]string{1: "allow", 2: "ask", 3: "deny"} {
		d := e.Decide(Request{Agent: "a", Action: "x.do", Class: ClassWrite, Args: map[string]any{"n": n}}, nil)
		if d.Effect != want {
			t.Errorf("n=%v: %s want %s", n, d.Effect, want)
		}
	}
}

func TestFailClosedOnEvalError(t *testing.T) {
	y := `
agents: {a: {profile: p}}
profiles:
  p:
    connectors: [x]
    rules:
      - {id: allow-if, action: x.a, when: "missing == 1", effect: allow}
      - {id: deny-if, action: x.b, when: "missing == 1", effect: deny}
      - {id: ask-if, action: x.c, when: "missing == 1", effect: ask}
`
	e := mustEngine(t, y, nil)
	for act, want := range map[string]string{"x.a": "allow", "x.b": "deny", "x.c": "ask"} {
		d := e.Decide(Request{Agent: "a", Action: act, Class: ClassRead, Args: map[string]any{}}, nil)
		if d.Effect != want || len(d.Errors) != 1 {
			t.Errorf("%s: %+v", act, d)
		}
		if act == "x.a" && !d.Default {
			t.Errorf("allow rule with error must not fire")
		}
	}
}

func TestLimits(t *testing.T) {
	y := `
agents: {a: {profile: p}}
profiles:
  p:
    connectors: [email, payments]
    rules: [{id: ok, action: "*", effect: allow}]
    limits:
      - {id: mail-per-day, action: email.send, per_day: 3}
      - {id: pay-cap, action: "payments.*", amount_per_day: 1000, reason: cap}
`
	e := mustEngine(t, y, nil)
	d := e.Decide(Request{Agent: "a", Action: "email.send", Args: map[string]any{}}, fakeCounters{count: 2})
	if d.Effect != "allow" {
		t.Fatal(d)
	}
	d = e.Decide(Request{Agent: "a", Action: "email.send", Args: map[string]any{}}, fakeCounters{count: 3})
	if d.Effect != "deny" || d.RuleIDs[0] != "mail-per-day" {
		t.Fatal(d)
	}
	d = e.Decide(Request{Agent: "a", Action: "payments.pay", Args: map[string]any{"amount": 600.0}}, fakeCounters{amount: 500})
	if d.Effect != "deny" || d.Reason != "cap" {
		t.Fatal(d)
	}
}

func TestCompileErrors(t *testing.T) {
	cases := map[string]string{
		"to.all(r, r in lists.nope)":   `unknown list "nope"`,
		"too == 1":                     `unknown variable "too"`,
		"glob(path)":                   "CEL:",
		"'x' +":                        "CEL:",
		"path":                         "",
		"domain_matches(path, 'x')":    "CEL:",
		"size(to) > 0 && hour < 23":    "",
		"email_domain(to[0]) == 'x.y'": "",
	}
	for when, want := range cases {
		y := "lists: {ok: []}\nagents: {a: {profile: p}}\nprofiles: {p: {connectors: [email], rules: [{id: r, action: email.send, effect: deny, when: \"" + strings.ReplaceAll(when, `"`, `\"`) + "\"}]}}\n"
		_, err := config.Parse([]byte(y), Validator(Catalog{"email.send": {"to", "path"}}))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: unexpected %v", when, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: want %q got %v", when, want, err)
		}
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		p, pat string
		want   bool
	}{
		{"~/Documents/obsidian/a.md", "~/Documents/obsidian/**", true},
		{"/home/u/Documents/obsidian/x/y/z.md", "~/Documents/obsidian/**", true},
		{"/home/u/Documents/obsidian", "~/Documents/obsidian/**", true},
		{"/home/u/Documents/obsidian2/a", "~/Documents/obsidian/**", false},
		{"/home/u/Documents/obsidian/../x", "~/Documents/obsidian/**", false},
		{"relative/a.md", "~/Documents/**", false},
		{"/a/b/c.txt", "/a/*/c.txt", true},
		{"/a/b/d/c.txt", "/a/*/c.txt", false},
		{"/a/b/d/c.txt", "/a/**/c.txt", true},
		{"/a/c.txt", "/a/**/c.txt", true},
		{"", "/**", false},
	}
	for _, c := range cases {
		if got := Glob(c.p, c.pat); got != c.want {
			t.Errorf("Glob(%q,%q)=%v", c.p, c.pat, got)
		}
	}
}

func TestDomainMatches(t *testing.T) {
	pats := []string{"*.gosuslugi.ru", "lk.mosenergosbyt.ru"}
	cases := map[string]bool{
		"https://www.gosuslugi.ru/":           true,
		"https://esia.gosuslugi.ru:443/login": true,
		"https://gosuslugi.ru/":               false,
		"https://evilgosuslugi.ru/":           false,
		"https://gosuslugi.ru.evil.test/":     false,
		"https://LK.Mosenergosbyt.RU/pay":     true,
		"http://lk.mosenergosbyt.ru.":         true,
		"javascript:alert(1)":                 false,
		"file:///etc/passwd":                  false,
		"https://user@lk.mosenergosbyt.ru/":   false,
		"lk.mosenergosbyt.ru":                 true,
		"":                                    false,
	}
	for u, want := range cases {
		if got := DomainMatches(u, pats); got != want {
			t.Errorf("%q: %v", u, got)
		}
	}
	if EmailDomain("Bob <Bob@Example.COM>") != "example.com" || EmailDomain("nope") != "" {
		t.Fatal("email domain")
	}
}

func TestTighten(t *testing.T) {
	d := Tighten(Decision{Effect: "allow", RuleIDs: []string{"r"}}, "phishing")
	if d.Effect != "ask" {
		t.Fatal(d)
	}
	if Tighten(Decision{Effect: "deny"}, "x").Effect != "deny" {
		t.Fatal("must not loosen")
	}
}

func TestActionMatches(t *testing.T) {
	if !ActionMatches("payments.*", "payments.pay_bill") || ActionMatches("payments.*", "paymentsx.a") || !ActionMatches("*", "a.b") || ActionMatches("email.send", "email.sender") {
		t.Fatal()
	}
}
