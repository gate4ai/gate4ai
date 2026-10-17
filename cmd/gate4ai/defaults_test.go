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
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/builtin/files"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/sdk-go/manifest"
)

// defaultActions — действия коннекторов по умолчанию (как в их манифестах):
// files встроен в ядро, browser ставится из магазина.
var defaultActions = map[string][]string{
	"files":   {"read", "list", "append", "write"},
	"browser": {"read", "screenshot", "extract"}, // + действия только для коннекторов (service_only)
}

var defaultManifests = map[string]string{
	// Пакет files из магазина до v0.6: ядро его не ставит и удаляет установленный.
	"files": `connector: files
version: "4"
runtime: wasm
capabilities:
  files: ["${setting:roots}/**"]
actions:
  - { name: read, class: read }
  - { name: list, class: read }
  - { name: append, class: write }
  - { name: write, class: write }
`,
	// Нативный пакет: в магазине — список платформ, ставится пакет этой платформы.
	"browser": `connector: browser
version: "6"
runtime: native
platforms: [` + store.HostPlatform() + `]
contract: 1
services: [open]
capabilities:
  reveal: ["${setting:model_key}"]
actions:
  - { name: read, class: read }
  - { name: screenshot, class: read }
  - { name: extract, class: read }
  - { name: open, class: read, service_only: true }
`,
	// Не по умолчанию: ставит владелец.
	"bank": `connector: bank
version: "1"
runtime: wasm
capabilities:
  network: [ { host: api.bank.example, scheme: https } ]
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
	for _, name := range defaultConnectors {
		want := defaultActions[name]
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "connectors", name, "manifest.yaml"))
		if err != nil {
			t.Skip("connectors sources are not next to the host:", err)
		}
		m, err := manifest.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, a := range m.Actions {
			if !a.ServiceOnly {
				got = append(got, a.Name)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s actions %v, want %v: update defaultActions", name, got, want)
		}
	}
}

// localRegistry — подписанный индекс с коннекторами по умолчанию от проверенного издателя.
func localRegistry(t *testing.T, dir string) (index, regPub string) {
	t.Helper()
	regPub, regPriv, _ := store.GenerateKey()
	pubPub, pubPriv, _ := store.GenerateKey()
	k, _ := store.ParsePrivateKey(pubPriv)
	ix := store.Index{Version: 1, Publishers: map[string]store.Publisher{"gate4ai": {Keys: []string{pubPub}, Trust: store.TrustVerified}},
		Connectors: map[string]store.Entry{}}
	os.MkdirAll(filepath.Join(dir, "packages"), 0o755)
	for name, m := range defaultManifests {
		mf, _ := manifest.Parse([]byte(m))
		var b []byte
		var err error
		if mf.Runtime == manifest.RuntimeNative {
			b, err = store.PackNative([]byte(m), []byte("fake native binary"), store.HostPlatform(), k)
		} else {
			b, err = store.Pack([]byte(m), []byte("\x00asm\x01\x00\x00\x00"), k)
		}
		if err != nil {
			t.Fatal(err)
		}
		p, _ := store.Open(b)
		file := name + "-" + p.Manifest.Version + ".g4c"
		os.WriteFile(filepath.Join(dir, "packages", file), b, 0o644)
		rel := store.Release{Version: p.Manifest.Version, URL: "packages/" + file, SHA256: store.SHA256(b),
			Digest: p.Sig.Digest, Capabilities: p.Manifest.Capabilities.Summary()}
		if p.Native() {
			set := store.PlatformSet{Connector: name, Version: p.Manifest.Version,
				Platforms: map[string]store.Artifact{store.HostPlatform(): {URL: file, SHA256: store.SHA256(b), Digest: p.Sig.Digest}}}
			sb, _ := json.Marshal(set)
			setFile := name + "-" + p.Manifest.Version + store.SetExt
			os.WriteFile(filepath.Join(dir, "packages", setFile), sb, 0o644)
			rel.URL, rel.SHA256, rel.Digest = "packages/"+setFile, store.SHA256(sb), store.SetDigest(set)
		}
		ix.Connectors[name] = store.Entry{Publisher: "gate4ai", Repo: "https://github.com/gate4ai/gate4ai-" + name, License: "AGPL-3.0-only",
			Versions: []store.Release{rel}}
	}
	raw, _ := json.Marshal(ix)
	sig, err := store.SignIndex(raw, regPriv)
	if err != nil {
		t.Fatal(err)
	}
	index = filepath.Join(dir, "index.json")
	os.WriteFile(index, raw, 0o644)
	os.WriteFile(index+".sig", []byte(sig), 0o644)
	return index, regPub
}

// Свежий home без --config: магазин недоступен — первый запуск не падает;
// при следующей возможности files и browser ставятся без вопроса владельцу,
// и каждое их действие идёт на подтверждение.
func TestFreshInstallDefaultConnectors(t *testing.T) {
	dir := t.TempDir()
	regDir := filepath.Join(dir, "registry")
	index := filepath.Join(regDir, "index.json")
	regPub, _, _ := store.GenerateKey()
	cfg := regexp.MustCompile(`registry: \S+`).ReplaceAllString(emptyConfig, "registry: "+index)
	home := filepath.Join(dir, "home")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func(cfg string) *daemon.Daemon {
		d, err := daemon.New(daemon.Options{Home: home, InitialConfig: []byte(cfg), DefaultConnectors: defaultConnectors,
			Keys: keys.NewMemory(), Log: log})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Магазина нет: daemon работает, коннекторы ждут следующей возможности.
	d := open(regexp.MustCompile(`registry_keys: .*`).ReplaceAllString(cfg, `registry_keys: ["`+regPub+`"]`))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	d.InstallDefaults(ctx)
	cancel()
	if list, _ := store.List(d.ConnectorsDir); len(list) != 0 {
		t.Fatal("installed without a store:", list)
	}
	d.Close()

	// Магазин появился; следующий запуск ставит оба коннектора.
	index2, regPub2 := localRegistry(t, regDir)
	if index2 != index {
		t.Fatal(index2)
	}
	d = open(cfg)
	raw, _ := d.Config.CurrentRaw()
	raw = regexp.MustCompile(`registry_keys: .*`).ReplaceAll(raw, []byte(`registry_keys: ["`+regPub2+`"]`))
	raw = append(raw, "agents:\n  bot: { profile: default }\n"...)
	raw = bytes.Replace(raw, []byte("    rules:\n"),
		[]byte("    rules:\n      - { id: owner-bank, action: bank.balance, effect: deny }\n"), 1)
	if _, err := d.Config.Put(auth.OwnerVia("test"), raw, "agent"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	d.InstallDefaults(ctx)
	cancel()

	list, err := store.List(d.ConnectorsDir)
	if err != nil || len(list) != 1 || list[0].Name != "browser" {
		t.Fatal(list, err)
	}
	for _, it := range list {
		if it.Info.Trust != store.TrustVerified || len(it.Info.Capabilities) == 0 {
			t.Fatal(it.Name, it.Info)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "default-connectors.json")); !os.IsNotExist(err) {
		t.Fatal("pending defaults left:", err)
	}
	c := d.Config.Current()
	if !c.Profiles["default"].HasConnector("files") || !c.Profiles["default"].HasConnector("browser") ||
		!slices.Equal(c.Connectors["files"]["roots"].([]any), []any{"~"}) || len(c.Connectors["browser"]) != 0 {
		t.Fatal(c.Profiles["default"], c.Connectors)
	}
	for conn, acts := range defaultActions {
		cli, ok := d.Registry.Get(conn)
		if !ok {
			t.Fatal(conn, "not loaded")
		}
		var got []string
		for _, a := range cli.Manifest().Actions {
			if a.ServiceOnly {
				continue // не для агентов: правила ask на них нет
			}
			got = append(got, a.Name)
			action := conn + "." + a.Name
			dec := d.Pipeline.Engine().Decide(policy.Request{Agent: "bot", Action: action, Class: a.Class}, nil)
			if dec.Effect != config.EffectAsk || dec.Default || !slices.Equal(dec.RuleIDs, []string{conn + "-" + a.Name + "-ask"}) {
				t.Fatal(action, dec)
			}
			args := map[string]map[string]any{"read": {"path": "~/a"}, "list": {"path": "~"}, "append": {"path": "~/a", "text": "x"},
				"write": {"path": "~/a", "content": "x"}}[a.Name]
			if conn != "files" {
				args = nil
			}
			if r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: action, Args: args}); r.Status != pipeline.StatusPending {
				t.Fatal(action, r)
			}
		}
		if !slices.Equal(got, acts) {
			t.Fatal(conn, got)
		}
	}

	// Любой другой коннектор при установке — тоже в default, действия с подтверждения;
	// правило владельца на действие остаётся.
	plan, err := d.PrepareConnector(context.Background(), "bank")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.InstallConnector(context.Background(), "bank", plan.Capabilities); err != nil {
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

	// Уже установленная система: повторный запуск ничего не ставит заново.
	d.Close()
	d = open(cfg)
	defer d.Close()
	if _, err := os.Stat(filepath.Join(home, "default-connectors.json")); !os.IsNotExist(err) {
		t.Fatal("existing install got pending defaults")
	}
}

// Пакет files из магазина (до v0.6) при запуске daemon заменяется встроенным
// коннектором: правила и настройки остаются, поставить его снова нельзя.
func TestStoreFilesReplacedByBuiltin(t *testing.T) {
	dir := t.TempDir()
	index, regPub := localRegistry(t, filepath.Join(dir, "registry"))
	cfg := regexp.MustCompile(`registry: \S+`).ReplaceAllString(emptyConfig, "registry: "+index)
	cfg = regexp.MustCompile(`registry_keys: .*`).ReplaceAllString(cfg, `registry_keys: ["`+regPub+`"]`)
	home := filepath.Join(dir, "home")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := daemon.New(daemon.Options{Home: home, InitialConfig: []byte(cfg), Keys: keys.NewMemory(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.PrepareConnector(context.Background(), "files"); err == nil || !strings.Contains(err.Error(), "part of gate4ai") {
		t.Fatal("store files must be refused:", err)
	}
	// Установка прежней версией ядра — пакет в каталоге коннекторов.
	opts := d.StoreOptions()
	opts.Builtin = nil
	plan, err := opts.Prepare(context.Background(), "files", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opts.Install(plan, plan.Capabilities); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d, err = daemon.New(daemon.Options{Home: home, Keys: keys.NewMemory(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if list, _ := store.List(d.ConnectorsDir); len(list) != 0 {
		t.Fatal("store package left:", list)
	}
	cli, ok := d.Registry.Get("files")
	if !ok || cli.Manifest().Runtime != manifest.RuntimeBuiltin {
		t.Fatal("built-in files not registered")
	}
	if dec := d.Pipeline.Engine().Decide(policy.Request{Agent: "x", Action: "files.read", Class: "read"}, nil); dec.Effect == "" {
		t.Fatal(dec)
	}
}
