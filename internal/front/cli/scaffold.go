// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"text/template"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

const newUsage = `usage:
  gate4ai connectors new <name> --site <domain> [-o folder]   a WASM connector over the browser: manifest,
                                                               code, a test on a recorded browser session`

// scaffoldSDK — версия модуля gate4ai (в нём sdk-go) для go.mod заготовки: та, из
// которой собран CLI; у сборки из рабочей копии (devel, +dirty) — запасная.
func scaffoldSDK() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; strings.HasPrefix(v, "v") && !strings.Contains(v, "+") {
			return v
		}
	}
	return "v0.7.0"
}

// scaffold — заготовка WASM-коннектора поверх браузера (service_call): с неё
// агент начинает коннектор к сайту, исследовав его в сеансе браузера.
func (e *Env) scaffold(args []string) int {
	pos, fl := splitFlags(args, map[string]bool{"site": true, "o": true})
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(e.Stderr)
	site := fs.String("site", "", "the site's domain, e.g. lk.example.org (more: edit params.domains in the manifest)")
	out := fs.String("o", "", "folder (default: ./<name>)")
	if fs.Parse(fl) != nil || len(pos) != 1 || *site == "" {
		fmt.Fprintln(e.Stderr, newUsage)
		return ExitUsage
	}
	name := pos[0]
	if !connectorName(name) {
		return e.fail(fmt.Errorf("connector name %q: use a-z, 0-9 and _ , starting with a letter", name))
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(*site, "https://"), "http://"), "/"))
	if host == "" || strings.ContainsAny(host, "/ ") || !(domain.Sites{Domains: []string{host}}).Allows("https://"+host+"/") {
		return e.fail(fmt.Errorf("--site %q: give a domain like lk.example.org", *site))
	}
	dir := *out
	if dir == "" {
		dir = name
	}
	if _, err := os.Stat(dir); err == nil {
		return e.fail(fmt.Errorf("%s already exists: choose another folder with -o", dir))
	}
	data := map[string]string{"Name": name, "Site": host, "Type": goType(name), "SDK": scaffoldSDK()}
	files := map[string]string{"go.mod": tplGoMod, "manifest.yaml": tplManifest, "main.go": tplMain, "connector.go": tplConnector,
		"connector_test.go": tplTest, "testdata/session.json": tplFixture, "README.md": tplReadme, ".gitignore": tplGitignore}
	var written []string
	for path, src := range files {
		var b strings.Builder
		if err := template.Must(template.New(path).Parse(src)).Execute(&b, data); err != nil {
			return e.fail(err)
		}
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return e.fail(err)
		}
		if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
			return e.fail(err)
		}
		written = append(written, full)
	}
	e.printJSON(map[string]any{"status": "created", "folder": dir, "files": len(written),
		"next": []string{
			"cd " + dir + " && go mod tidy && go test ./...",
			"explore the site: gate4ai browser open --url https://" + host + "/ (then observe --elements, act, network, close)",
			"save browser.close to .explore/session.json, then: gate4ai fixture scrub .explore/session.json -o testdata/session.json --auto --replace \"real=fake\"",
			"write the action in connector.go (wait with wait_for_*/expect_*, explain clicks in call_reasons)",
			"gate4ai connectors install . --watch (owner) or change_policy install_dev_connector (agent); call: gate4ai " + name + " summary",
		}})
	return ExitOK
}

func connectorName(n string) bool {
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return n != "data"
}

// goType — имя типа коннектора: my_site → MySite.
func goType(n string) string {
	var b strings.Builder
	for _, part := range strings.Split(n, "_") {
		if part != "" {
			b.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	return b.String()
}

const tplGitignore = `# сырые записи сеансов браузера: в них настоящие личные данные
.explore/
`

const tplGoMod = `module gate4ai-{{.Name}}

go 1.27.1

require github.com/gate4ai/gate4ai {{.SDK}}
`

const tplManifest = `connector: {{.Name}}
description: {{.Site}} through the user's browser
version: "0.1.0"
runtime: wasm
capabilities:
  services:
    # Действия браузера на странице этого коннектора; сайты — только эти.
    - target: browser
      contract: 1
      actions: [open, observe, act, page_extract, page_screenshot]
      params: { domains: ["{{.Site}}"] }
limits: { timeout_s: 60 }
actions:
  - name: summary
    class: read
    untrusted: true
    description: Title and the start of the text of the {{.Site}} home page
    calls: [browser.open, browser.observe]
    output_schema:
      type: object
      properties:
        title: { type: string }
        text: { type: string }
      required: [title]
`

const tplMain = `// SPDX-License-Identifier: AGPL-3.0-only

// Коннектор gate4.ai к {{.Site}}: действия сайта через браузер пользователя
// (service_call к browser), сайты — из capabilities.services манифеста.
package main

import (
	_ "embed"

	"github.com/gate4ai/gate4ai/sdk-go/connector"
)

//go:embed manifest.yaml
var manifestYAML []byte

func init() { connector.Register(manifestYAML, {{.Type}}{}) }

func main() {}
`

const tplConnector = `// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
)

// Home — главная страница сайта.
const Home = "https://{{.Site}}/"

// {{.Type}} — коннектор: каждое действие открывает страницу браузера (одна на
// вызов агента) и работает с ней через browser.observe / browser.act.
type {{.Type}} struct{}

// page — ответ browser.observe.
type page struct {
	URL   string ` + "`json:\"url\"`" + `
	Title string ` + "`json:\"title\"`" + `
	Text  string ` + "`json:\"text\"`" + `
}

// Execute исполняет действие.
func ({{.Type}}) Execute(ctx context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	switch r.Action {
	case "summary":
		if _, err := h.ServiceCall("browser", "open", map[string]any{"url": Home}, nil); err != nil {
			return abi.ExecResult{}, err
		}
		var p page
		if _, err := h.ServiceCall("browser", "observe", map[string]any{"max_chars": 2000}, &p); err != nil {
			return abi.ExecResult{}, err
		}
		h.Log("info", "observed "+p.URL)
		return abi.ExecResult{Structured: map[string]any{"title": p.Title, "text": p.Text}, Source: "web:" + p.URL}, nil
	}
	return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown action %s", r.Action)
}

// Verify — у действий чтения проверки нет.
func ({{.Type}}) Verify(_ context.Context, _ *abi.Host, r abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
}
`

const tplTest = `// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"testing"

	"github.com/gate4ai/gate4ai/sdk-go/hosttest"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// host — тестовый хост: браузер отвечает шагами, записанными в сеансе агента
// (результат browser.close в testdata/session.json).
func host(t *testing.T, fixture string) *hosttest.Host {
	t.Helper()
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		t.Fatal(err)
	}
	fx, err := hosttest.LoadFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	h := hosttest.New(m, nil, nil)
	h.Services = hosttest.Replay("browser", fx)
	return h
}

func TestSummary(t *testing.T) {
	h := host(t, "testdata/session.json")
	res, err := h.Execute({{.Type}}{}, "summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Structured.(map[string]any)["title"] == "" {
		t.Fatalf("%+v", res)
	}
}
`

const tplFixture = `{
  "steps": [
    {"action": "open", "args": {"url": "https://{{.Site}}/"}, "result": {"url": "https://{{.Site}}/", "title": "{{.Site}}"}},
    {"action": "observe", "args": {"max_chars": 2000}, "result": {"url": "https://{{.Site}}/", "title": "{{.Site}}", "text": "replace with a recorded session"}}
  ]
}
`

const tplReadme = `# {{.Name}} — коннектор gate4.ai к {{.Site}}

WASM-коннектор поверх браузера пользователя: страницу открывает и действия на ней выполняет коннектор ` + "`browser`" + ` (service_call), сайты ограничены ` + "`params.domains`" + ` в манифесте.

## Как писать

1. Исследуйте сайт в сеансе браузера: ` + "`gate4ai browser open --url https://{{.Site}}/`" + ` (вернёт ` + "`session`" + `), затем ` + "`observe --session … --elements`" + ` (селекторы), ` + "`act`" + ` (клики, ввод), ` + "`network`" + ` (запросы страницы к API). Войти на сайт человек может сам в окне выделенного профиля Chrome.
2. ` + "`gate4ai browser close --session …`" + ` возвращает записанные шаги: сохраните их в ` + "`.explore/`" + ` (не в git), обезличьте ` + "`gate4ai fixture scrub .explore/x.json -o testdata/x.json --auto --replace \"настоящее=вымышленное\"`" + ` и проверьте глазами — это фикстура для ` + "`hosttest.Replay`" + `.
3. Опишите действия в ` + "`manifest.yaml`" + ` (класс, схемы, ` + "`calls`" + ` — подвызовы браузера) и реализуйте их в ` + "`connector.go`" + `; ` + "`go test ./...`" + `.
4. ` + "`gate4ai connectors install . --watch`" + ` — поставить в свой daemon и пересобирать на каждое сохранение (агент без сокета владельца — ` + "`gate4ai gate4ai change_policy`" + ` с ` + "`install_dev_connector`" + `); вызов — ` + "`gate4ai {{.Name}} summary`" + `. Ошибки своей сборки приходят вместе с её логом (поле ` + "`log`" + `), весь лог — ` + "`gate4ai connectors logs {{.Name}}`" + `.

Селекторы вида ` + "`div:nth-of-type(2) > button`" + ` ломаются от вёрстки: в ` + "`act`" + ` надёжнее общий селектор и видимый текст (` + "`selector: button, text: Оплатить`" + `).
`
