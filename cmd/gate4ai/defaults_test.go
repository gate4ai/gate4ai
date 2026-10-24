// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser"
	"github.com/gate4ai/gate4ai/internal/connectors/builtin/files"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/gate4ai/sdk-go/registry"
)

// defaultActions — действия встроенных компонентов ядра (как в их манифестах):
// files и browser.
var defaultActions = map[string][]string{
	"files":   {"read", "list", "append", "write"},
	"browser": {"read", "screenshot", "extract", "open", "observe", "act", "reload", "handoff", "network", "get", "head", "post", "put", "patch", "delete", "download", "login", "fill_secret", "page_extract", "page_screenshot", "dialog", "close"},
}

var defaultManifests = map[string]string{
	// Пакет files в магазине: ядро его не ставит (files встроен).
	"files": `connector: files
version: "4"
runtime: wasm
folders:
  - { path: "${setting:roots}", access: allow }
actions:
  - { name: read, class: read }
  - { name: list, class: read }
  - { name: append, class: write }
  - { name: write, class: write }
`,
	// Не по умолчанию: ставит владелец.
	"bank": `connector: bank
version: "1"
runtime: wasm
folders:
  - { path: /srv/api.bank.example, access: allow }
actions:
  - { name: balance, class: read }
  - { name: pay, class: external }
`,
}

// Манифесты коннекторов монорепозитория (если он рядом) — с теми же действиями.
func TestDefaultActionsMatchManifests(t *testing.T) {
	var got []string
	for _, a := range files.Manifest().Actions {
		got = append(got, a.Name)
	}
	if !slices.Equal(got, defaultActions["files"]) {
		t.Fatalf("files actions %v: update defaultActions", got)
	}
	got = nil
	for _, a := range browser.Manifest().Actions {
		if !a.ServiceOnly {
			got = append(got, a.Name)
		}
	}
	if !slices.Equal(got, defaultActions["browser"]) {
		t.Fatalf("browser actions %v: update defaultActions", got)
	}
}

// localRegistry — подписанный индекс с коннекторами по умолчанию от проверенного издателя.
func localRegistry(t *testing.T, dir string) (index, regPub string) {
	t.Helper()
	regPub, regPriv, _ := registry.GenerateKey()
	pubPub, pubPriv, _ := registry.GenerateKey()
	k, _ := registry.ParsePrivateKey(pubPriv)
	ix := registry.Index{Version: 1, Publishers: map[string]registry.Publisher{"gate4ai": {Keys: []string{pubPub}, Trust: registry.TrustVerified}},
		Connectors: map[string]registry.Entry{}}
	os.MkdirAll(filepath.Join(dir, "packages"), 0o755)
	for name, m := range defaultManifests {
		b, err := store.Pack([]byte(m), []byte("\x00asm\x01\x00\x00\x00"), k)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := store.Open(b)
		file := name + "-" + p.Manifest.Version + ".g4c"
		os.WriteFile(filepath.Join(dir, "packages", file), b, 0o644)
		rel := registry.Release{Version: p.Manifest.Version, URL: "packages/" + file, SHA256: store.SHA256(b),
			Digest: p.Sig.Digest, Capabilities: p.Manifest.Summary()}
		ix.Connectors[name] = registry.Entry{Publisher: "gate4ai", Repo: "https://github.com/gate4ai/gate4ai-" + name, License: "AGPL-3.0-only",
			Versions: []registry.Release{rel}}
	}
	raw, _ := json.Marshal(ix)
	sig, err := registry.SignIndex(raw, regPriv)
	if err != nil {
		t.Fatal(err)
	}
	index = filepath.Join(dir, "index.json")
	os.WriteFile(index, raw, 0o644)
	os.WriteFile(index+".sig", []byte(sig), 0o644)
	return index, regPub
}

// Свежий home без --config: files и browser встроены и видны в профиле default,
// каждое их действие идёт на подтверждение; магазин для этого не нужен.
// Любой коннектор из магазина при установке — тоже в default с ask; правило
// владельца на действие остаётся; начальный комментарий политики сохраняется.
func TestFreshInstallBuiltins(t *testing.T) {
	dir := t.TempDir()
	index, regPub := localRegistry(t, filepath.Join(dir, "registry"))
	cfg := regexp.MustCompile(`registry: \S+`).ReplaceAllString(emptyConfig, "registry: "+index)
	cfg = regexp.MustCompile(`registry_keys: .*`).ReplaceAllString(cfg, `registry_keys: ["`+regPub+`"]`)
	home := filepath.Join(dir, "home")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func() *daemon.Daemon {
		d, err := daemon.New(daemon.Options{Home: home, InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Log: log})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := open()
	if list, _ := store.List(d.ConnectorsDir); len(list) != 0 {
		t.Fatal("something installed from the store on a fresh install:", list)
	}
	raw, _ := d.Config.CurrentRaw()
	raw = append(raw, "agents:\n  bot: { profile: default }\n"...)
	raw = bytes.Replace(raw, []byte("    connectors: [files, browser]\n"),
		[]byte("    connectors: [files, browser]\n    rules:\n      - { id: owner-bank, action: bank.balance, effect: deny }\n"), 1)
	if _, err := d.Config.Put(auth.OwnerVia("test"), raw, "agent"); err != nil {
		t.Fatal(err)
	}
	c := d.Config.Current()
	if !c.Profiles["default"].HasConnector("files") || !c.Profiles["default"].HasConnector("browser") ||
		len(c.Access.Folders) != 1 || c.Access.Folders[0] != (access.Folder{Path: "~/Documents", Access: access.Ask}) ||
		len(c.Access.Sites) != 1 || c.Access.Sites[0] != (access.Site{Domain: "*", Access: access.Ask}) {
		t.Fatal(c.Profiles["default"], c.Access)
	}
	for conn, acts := range defaultActions {
		cli, ok := d.Registry.Get(conn)
		if !ok || cli.Manifest().Runtime != manifest.RuntimeBuiltin {
			t.Fatal(conn, "not built in")
		}
		var got []string
		for _, a := range cli.Manifest().Actions {
			if a.ServiceOnly {
				continue // не для агентов: правила ask на них нет
			}
			got = append(got, a.Name)
			action := conn + "." + a.Name
			if conn != "files" {
				continue // действия браузера проверяют аргументы и ждут Chrome
			}
			// Правил нет: решает список доступа (~/Documents: ask).
			args := map[string]map[string]any{"read": {"path": "~/Documents/a"}, "list": {"path": "~/Documents"}, "append": {"path": "~/Documents/a", "text": "x"},
				"write": {"path": "~/Documents/a", "content": "x"}}[a.Name]
			if r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: action, Args: args}); r.Status != pipeline.StatusPending {
				t.Fatal(action, r)
			}
		}
		if !slices.Equal(got, acts) {
			t.Fatal(conn, got)
		}
	}

	plan, err := d.PrepareConnectorDev(context.Background(), "bank", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.InstallConnectorDev(context.Background(), "bank", plan.Capabilities, false); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]string{"bank.balance": config.EffectDeny, "bank.pay": config.EffectAsk} {
		if dec := d.Pipeline.Engine().Decide(policy.Request{Agent: "bot", Action: action, Class: "read"}, nil); dec.Effect != want {
			t.Fatal(action, dec)
		}
	}
	raw, _ = d.Config.CurrentRaw()
	if !strings.HasPrefix(string(raw), "# gate4ai: no agents yet.") || strings.Count(string(raw), "bank.balance") != 1 {
		t.Fatalf("policy:\n%s", raw)
	}
	d.Close()
}

// Установка прежней версии: пакеты browser (теперь встроен) и email (удалён)
// в каталоге коннекторов стираются при старте, почта уходит из политики новой
// версией; откат возможен.
func TestLegacyPackagesMigrated(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	cdir := filepath.Join(home, "connectors")
	for _, name := range []string{"browser", "email"} {
		os.MkdirAll(filepath.Join(cdir, name), 0o755)
		os.WriteFile(filepath.Join(cdir, name, "manifest.yaml"), []byte("connector: "+name+"\n"), 0o644)
	}
	cfg := `# gate4ai: old install
store:
  registry: https://example.invalid/index.json
  registry_keys: ["ed25519:yDcbQuNCGqSqHSt08HdvW5/fNyWRW8FIXtKiL91hqtw="]
agents:
  bot: { profile: default }
  mailer: { profile: drafts }
lists:
  contacts_trusted: [a@example.com]
connectors:
  files: { roots: ["~/Documents"] }
  browser: { allowed_domains: [account.example], engine: cdp }
  email: { address: me@example.com }
profiles:
  default:
    connectors: [files, browser]
  drafts:
    connectors: [email, files]
    rules:
      - { id: no-send, action: email.send, effect: deny }
      - { id: files-ro, action: files.write, effect: deny }
`
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Первый запуск — «старая» установка: кладём политику с почтой и прежними
	// настройками сайтов и папок напрямую.
	d, err := daemon.New(daemon.Options{Home: home, InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = daemon.New(daemon.Options{Home: home, InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, name := range []string{"browser", "email"} {
		if _, err := os.Stat(filepath.Join(cdir, name)); !os.IsNotExist(err) {
			t.Fatal("legacy package left:", name)
		}
	}
	c := d.Config.Current()
	if _, ok := c.Connectors["email"]; ok || c.Profiles["drafts"].HasConnector("email") || !c.Profiles["drafts"].HasConnector("files") {
		t.Fatal(c.Connectors, c.Profiles["drafts"])
	}
	if ids := ruleIDs(c.Profiles["drafts"].Rules); !slices.Equal(ids, []string{"files-ro"}) {
		t.Fatal(ids)
	}
	if vs, _ := d.Config.Versions(); len(vs) < 2 {
		t.Fatal("migration must be a new policy version:", vs)
	}
	// Прежние папки — в списке доступа как read, сайты — как allow (у сайтов
	// режима read нет); настройки убраны.
	if f, ok := access.MatchFolder(c.Access.Folders, "~/Documents/x", d.Pipeline.Home); !ok || f.Access != access.Read || c.Connectors["files"] != nil {
		t.Fatal(c.Access.Folders, c.Connectors["files"])
	}
	if s, ok := access.MatchSite(c.Access.Sites, "https://account.example/"); !ok || s.Access != access.Allow || c.Connectors["browser"]["allowed_domains"] != nil {
		t.Fatal(c.Access.Sites, c.Connectors["browser"])
	}
}

func ruleIDs(rs []config.Rule) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// Пакет files в магазине не ставится: files — встроенный коннектор ядра.
func TestStoreFilesRefused(t *testing.T) {
	dir := t.TempDir()
	index, regPub := localRegistry(t, filepath.Join(dir, "registry"))
	cfg := regexp.MustCompile(`registry: \S+`).ReplaceAllString(emptyConfig, "registry: "+index)
	cfg = regexp.MustCompile(`registry_keys: .*`).ReplaceAllString(cfg, `registry_keys: ["`+regPub+`"]`)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := daemon.New(daemon.Options{Home: filepath.Join(dir, "home"), InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.PrepareConnectorDev(context.Background(), "files", false); err == nil || !strings.Contains(err.Error(), "part of gate4ai") {
		t.Fatal("store files must be refused:", err)
	}
	cli, ok := d.Registry.Get("files")
	if !ok || cli.Manifest().Runtime != manifest.RuntimeBuiltin {
		t.Fatal("built-in files not registered")
	}
	if dec := d.Pipeline.Engine().Decide(policy.Request{Agent: "x", Action: "files.read", Class: "read"}, nil); dec.Effect == "" {
		t.Fatal(dec)
	}
}
