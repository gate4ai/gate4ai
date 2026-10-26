// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
)

func TestRecipeRoundTrip(t *testing.T) {
	r := &Recipe{Site: "rzd.ru", Domains: []string{"ticket.rzd.ru"}, Tasks: []Task{{ID: "buy_ticket", Class: "money", VerifiedRuns: 2}},
		Login: &RecipeLogin{Bind: "https://pass.rzd.ru", Kind: "form+sms"}, HumanSteps: []string{"sms_code", "payment"}}
	raw, err := FormatRecipe(r, "## buy_ticket\n1. Open the page.\n")
	if err != nil {
		t.Fatal(err)
	}
	got, body, err := ParseRecipe(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Site != "rzd.ru" || got.Tasks[0].Status != StatusDraft || got.Tasks[0].VerifiedRuns != 2 || got.Login.Kind != "form+sms" || !strings.HasPrefix(body, "## buy_ticket") {
		t.Fatalf("%+v %q", got, body)
	}
	if !got.Matches("ticket.rzd.ru") || !got.Matches("rzd.ru") || got.Matches("example.org") {
		t.Fatal("matching")
	}
	if _, _, err := ParseRecipe([]byte("no front matter")); err == nil {
		t.Fatal("want error")
	}
}

func TestListFindAndGit(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "gate4ai-my-site")
	os.MkdirAll(filepath.Join(f, ".git"), 0o755)
	os.WriteFile(filepath.Join(f, ".git", "config"), []byte("[core]\n\tbare = false\n[remote \"origin\"]\n\turl = https://github.com/alex/gate4ai-my-site.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"), 0o644)
	os.WriteFile(filepath.Join(f, ManifestFile), []byte("connector: my_site\ndescription: x\nversion: \"0.1.0\"\nruntime: wasm\nsites:\n  - { domain: app.example.org, access: allow }\nactions:\n  - { name: summary, class: read }\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "junk"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	list, err := List(dir)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %+v", err, list)
	}
	got := list[0]
	if got.Connector != "my_site" || got.Git.Remote != "https://github.com/alex/gate4ai-my-site.git" || !got.Git.Repo {
		t.Fatalf("%+v", got)
	}
	if !got.MatchesSite("app.example.org") || got.MatchesSite("other.org") {
		t.Fatal("sites")
	}
	for _, n := range []string{"my_site", "gate4ai-my-site"} {
		if _, ok := Find(dir, n); !ok {
			t.Fatal(n)
		}
	}
	if FolderName("my_site") != "gate4ai-my-site" {
		t.Fatal(FolderName("my_site"))
	}
	if l, err := List(filepath.Join(dir, "none")); err != nil || l != nil {
		t.Fatal("missing dir is empty, not an error")
	}
	if Resolve("", "/home/u") != "/home/u/gate4ai-knowledge" || Resolve("~/k", "/home/u") != "/home/u/k" || Resolve("/x", "") != "/x" {
		t.Fatal("resolve")
	}
	// Состояния.
	dirty := true
	for _, c := range []struct {
		g    Git
		want string
	}{{Git{}, "files"}, {Git{Repo: true}, "git"}, {Git{Repo: true, Remote: "x"}, "published"}, {Git{Repo: true, Remote: "x", Dirty: &dirty}, "unpublished_changes"}} {
		if GitState(c.g) != c.want {
			t.Fatalf("%+v: %s", c.g, GitState(c.g))
		}
	}
	p := HowToPublish(Folder{Name: "gate4ai-my-site", Path: f, Connector: "my_site"}, "alex")
	if p.Kind != "scratch" || !strings.Contains(strings.Join(p.Commands, "\n"), "gh repo create alex/gate4ai-my-site") {
		t.Fatalf("%+v", p)
	}
	p = HowToPublish(Folder{Name: "gate4ai-my-site", Path: f, Git: Git{Repo: true, Remote: "https://github.com/bob/gate4ai-my-site", Dirty: &dirty}}, "alex")
	if p.Kind != "fork" {
		t.Fatalf("%+v", p)
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	write(ManifestFile, "connector: my_site\ndescription: x\nversion: \"0.1.0\"\nruntime: wasm\nsites:\n  - { domain: app.example.org, access: allow }\nactions:\n  - { name: summary, class: read }\n")
	rep := Check(dir)
	if rep.OK || len(rep.Errors) < 2 {
		t.Fatalf("%+v", rep)
	}
	write(LicenseFile, "AGPL")
	write(".gitignore", ".explore/\n")
	write(ReadmeFile, "# ok\n")
	write(RecipeFile, "---\nsite: app.example.org\ntasks:\n  - { id: summary }\n---\n## summary\n")
	write("connector.go", "package main\n")
	if rep = Check(dir); !rep.OK || len(rep.Warnings) != 0 {
		t.Fatalf("%+v", rep)
	}
	write("testdata/s.json", `{"steps":[{"result":{"text":"Иванов, +7 916 123-45-67, ivanov@mail.ru, полис 1234567890123456"}}]}`)
	write("connector.go", "package main\nconst password = \"hunter22\"\n")
	rep = Check(dir)
	if rep.OK || len(rep.Warnings) < 3 || !strings.Contains(strings.Join(rep.Errors, "\n"), "secret") {
		t.Fatalf("%+v", rep)
	}
	// Заглушки и алиасы — не секреты; поддельные значения scrub — не личные данные.
	write("connector.go", "package main\n// password: @site.password\n// token: <string>\n")
	write("testdata/s.json", `{"steps":[{"result":{"text":"user1@example.com +7 900 000-00-01 00000001"}}]}`)
	if rep = Check(dir); !rep.OK || len(rep.Warnings) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestDraftFromSteps(t *testing.T) {
	steps := []hosttest.Step{
		{Action: "open", Args: map[string]any{"url": "https://ticket.rzd.ru/", "wait_for_text": "Найти"}, Result: map[string]any{"url": "https://ticket.rzd.ru/", "login_required": map[string]any{"account": "@rzd"}}},
		{Action: "login", Args: map[string]any{"account": "@rzd"}, Result: map[string]any{"status": "still_on_login", "url": "https://pass.rzd.ru/login"}},
		{Action: "act", Args: map[string]any{"selector": "#from", "method": "fill", "value": "Москва"}, Result: map[string]any{"url": "https://ticket.rzd.ru/"}},
		{Action: "act", Args: map[string]any{"selector": "button", "text": "Найти", "method": "click", "expect_selector": ".trains"}, Error: &abi.Error{Code: "not_found", Message: "no .trains"}},
		{Action: "act", Args: map[string]any{"selector": "button", "text": "Найти", "method": "click", "expect_text": "Найдено"}, Result: map[string]any{"url": "https://ticket.rzd.ru/search"}},
		{Action: "network", Args: map[string]any{"url_contains": "/api/"}, Result: map[string]any{"requests": []any{}}},
		{Action: "observe", Args: map[string]any{"max_chars": float64(2000)}, Result: map[string]any{"url": "https://ticket.rzd.ru/search", "title": "Поезда", "text": "Найдено 5"}},
		{Action: "handoff", Args: map[string]any{"message": "Оплатите"}, Result: map[string]any{"status": "done"}},
		{Action: "observe", Args: map[string]any{}, Via: "rzd-login", Result: map[string]any{}},
		{Action: "close", Args: map[string]any{}, Result: map[string]any{"closed": true}},
	}
	d := FromSteps(steps, "rzd", "buy_ticket", "")
	if d.Site != "ticket.rzd.ru" || !strings.Contains(strings.Join(d.Domains, ","), "pass.rzd.ru") {
		t.Fatalf("%+v", d.Domains)
	}
	if d.Class != "write" || strings.Join(d.Calls, " ") != "browser.act browser.login browser.observe browser.open" {
		t.Fatalf("%s %v", d.Class, d.Calls)
	}
	if len(d.Steps) != 5 || len(d.Pitfalls) != 1 || len(d.Notes) != 1 || len(d.Inputs) != 1 || d.Inputs[0].Value != "Москва" {
		t.Fatalf("steps %d pitfalls %v notes %v inputs %+v", len(d.Steps), d.Pitfalls, d.Notes, d.Inputs)
	}
	if d.Login == nil || d.Login.Kind != "form+sms" || d.Login.Bind != "https://ticket.rzd.ru" {
		t.Fatalf("%+v", d.Login)
	}
	r := d.Recipe("v0.7.15", time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	if r.Tasks[0].Status != StatusDraft || r.Tasks[0].LastVerified != "2026-10-26" || strings.Join(r.HumanSteps, ",") != "sms_code,handoff" {
		t.Fatalf("%+v", r)
	}
	body := d.Body()
	for _, want := range []string{"1. Open `https://ticket.rzd.ru/`; wait for text `Найти`.", "Type `{input_1}` into `#from`", "Click «Найти»", "not_found", "Enters the code from SMS", "Оплатите", "session-only"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
	code := d.GoCase()
	for _, want := range []string{`"open", map[string]any{"url": "https://ticket.rzd.ru/", "wait_for_text": "Найти"}`, `"login", map[string]any{"account": "site"}`, `"value": r.Args["input_1"]`, "var p page", "p.Title"} {
		if !strings.Contains(code, want) {
			t.Fatalf("code lacks %q:\n%s", want, code)
		}
	}
	if !strings.Contains(d.InputSchema(), "input_1") || d.ExampleArgs()["input_1"] != "Москва" || len(d.Fixture().Steps) != 5 {
		t.Fatal("schema/args/fixture")
	}
}
