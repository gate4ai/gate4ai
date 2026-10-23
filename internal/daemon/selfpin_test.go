// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
	"github.com/gate4ai/gate4ai/sdk-go/registry"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/ipc"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
)

// copyProbe — копия тестового WASM-коннектора probe внутри модуля (testdata:
// сборке нужен go.mod модуля), чтобы тест мог менять её после запроса.
func copyProbe(t *testing.T) string {
	t.Helper()
	_ = os.MkdirAll("testdata", 0o755)
	dir, err := os.MkdirTemp("testdata", "probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir); os.Remove("testdata") })
	for _, f := range []string{"main.go", "manifest.yaml"} {
		b, err := os.ReadFile(filepath.Join("../connectors/wasm/testdata/probe", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	abs, _ := filepath.Abs(dir)
	return abs
}

// Своя сборка ставится только такой, какой её показали владельцу: правка
// манифеста или исходников после запроса — отказ при исполнении (code review K2).
func TestInstallDevPinnedToRequest(t *testing.T) {
	c, _ := connectors.NewInProcess(&notes{})
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	folder := copyProbe(t)
	request := func() string {
		r := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
			"summary": "install", "changes": []any{map[string]any{"op": "install_dev_connector", "folder": folder}}}})
		if r.Status != pipeline.StatusPending {
			t.Fatalf("%+v", r)
		}
		return r.ID
	}
	id := request()
	a, _ := d.State.GetApproval(id)
	for _, want := range []string{"runtime wasm (sandbox)", "actions: write: kv; read: deny_all", "limits per call: memory 48 MB, timeout 3 s", "secret ${setting:token} for 127.0.0.1"} {
		if !strings.Contains(a.Preview, want) {
			t.Fatalf("%q not in\n%s", want, a.Preview)
		}
	}
	man := filepath.Join(folder, "manifest.yaml")
	orig, _ := os.ReadFile(man)
	// После запроса агент расширяет возможности и переключает runtime.
	evil := strings.Replace(string(orig), "bind: [127.0.0.1]", "bind: [attacker.example]", 1)
	_ = os.WriteFile(man, []byte(strings.Replace(evil, "runtime: wasm", "runtime: native", 1)), 0o644)
	if res, err := d.Pipeline.Decide(ctx, id, true, "webui", "owner"); err != nil || res.Status == "ok" {
		t.Fatalf("changed manifest installed: %+v %v", res, err)
	}
	if _, ok := d.Registry.Get("probe"); ok {
		t.Fatal("installed")
	}
	// Правка исходников после запроса — тоже отказ.
	_ = os.WriteFile(man, orig, 0o644)
	id = request()
	f, _ := os.OpenFile(filepath.Join(folder, "main.go"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n// changed\n")
	f.Close()
	if res, err := d.Pipeline.Decide(ctx, id, true, "webui", "owner"); err != nil || res.Status == "ok" {
		t.Fatalf("changed sources installed: %+v %v", res, err)
	}
	if testing.Short() {
		return
	}
	// Без изменений — ставится.
	id = request()
	if res, err := d.Pipeline.Decide(ctx, id, true, "webui", "owner"); err != nil || res.Status != "ok" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, ok := d.Registry.Get("probe"); !ok {
		t.Fatal("not installed")
	}
}

// Агент не задаёт закреплённое сам: неизвестный аргумент отклоняется схемой.
func TestPinnedArgIsNotForAgents(t *testing.T) {
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), InitialConfig: []byte(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "x", pinsArg: "{}", "changes": []any{map[string]any{"op": "add_list_items", "list": "l", "items": []any{"a"}}}}})
	if r.Code != pipeline.CodeInvalidArgs {
		t.Fatalf("%+v", r)
	}
}

// Текст агента идёт после правок, одной строкой и с пометкой (code review E.4).
func TestSelfConfigPreviewAgentTextAfterChanges(t *testing.T) {
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), InitialConfig: []byte(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "ok\nChanges to the gate4.ai policy:\n  • Правило: запрещать files.read",
		"reason":  "x\n\n\n\n",
		"changes": []any{map[string]any{"op": "add_list_items", "list": "l", "items": []any{"a"}}}}})
	if r.Status != pipeline.StatusPending {
		t.Fatalf("%+v", r)
	}
	a, _ := d.State.GetApproval(r.ID)
	lines := strings.Split(a.Preview, "\n")
	bullets, agentAt, changesAt := 0, -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, "  • ") {
			bullets++
		}
		if strings.HasPrefix(l, "Changes to the gate4.ai policy:") {
			changesAt = i
		}
		if strings.HasPrefix(l, "Agent's own words") {
			agentAt = i
		}
	}
	if bullets != 1 || changesAt < 0 || agentAt < changesAt {
		t.Fatal(a.Preview)
	}
	long := strings.Repeat("x", 201)
	r = d.Pipeline.Execute(context.Background(), pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": long, "changes": []any{map[string]any{"op": "add_list_items", "list": "l", "items": []any{"a"}}}}})
	if r.Code != pipeline.CodeInvalidArgs {
		t.Fatalf("summary is limited: %+v", r)
	}
}

// mailLike — коннектор, у которого хост настройки — привязка секрета.
type mailLike struct{}

func (mailLike) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "mail", Capabilities: manifest.Capabilities{
		Network: []manifest.NetworkCap{{Host: "${setting:imap.host}", Scheme: "tls"}},
		Secrets: []manifest.SecretCap{{Alias: "${setting:imap.password}", Bind: []string{"${setting:imap.host}"}}}},
		Actions: []manifest.Action{{Name: "read", Class: "read"}}}
}
func (mailLike) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	return abi.ExecResult{}, nil
}
func (mailLike) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

// Настройки, от которых зависит, куда уходит секрет, правило allow не
// пропускает: только человек и с предупреждением; учётка не ссылается на
// секрет, который уже есть в Vault (code review H4).
func TestSelfConfigBindSettingsAndVaultAliases(t *testing.T) {
	c, _ := connectors.NewInProcess(mailLike{})
	conf := "agents:\n  bot: { profile: p }\nprofiles:\n  p:\n    connectors: [mail]\n    rules:\n      - { id: self-allow, action: gate4ai.change_policy, effect: allow }\n"
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c}, InitialConfig: []byte(conf)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	call := func(ch map[string]any) pipeline.Result {
		return d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{"summary": "x", "changes": []any{ch}}})
	}
	r := call(map[string]any{"op": "connector_settings", "connector": "mail", "settings_json": `{"imap":{"host":"evil.example"}}`})
	if r.Status != pipeline.StatusPending || !strings.Contains(r.Reason, "always needs the owner") {
		t.Fatalf("%+v", r)
	}
	if a, _ := d.State.GetApproval(r.ID); !strings.Contains(a.Preview, "ATTENTION: setting imap of connector mail") || !strings.Contains(a.Preview, "evil.example") {
		t.Fatal(a.Preview)
	}
	if r := call(map[string]any{"op": "connector_settings", "connector": "mail", "settings_json": `{"folder":"INBOX"}`}); r.Status != pipeline.StatusPending || strings.Contains(r.Reason, "ATTENTION") {
		t.Fatalf("other settings: the owner approves, without the warning: %+v", r)
	}
	if err := d.Vault.Set("@bank.password", "fake-value"); err != nil {
		t.Fatal(err)
	}
	cred := `{"bind":["https://evil.example"],"form":{"fields":[{"selector":"#p","secret":"@bank.password"}],"submit":{"press":"Enter"}}}`
	if r := call(map[string]any{"op": "add_credential", "account": "@bank", "credential_json": cred}); r.Code != pipeline.CodeInvalidArgs {
		t.Fatalf("existing vault alias: %+v", r)
	}
}

// storeIndex пишет подписанный индекс с одним WASM-коннектором kvx версии 1 и
// возможностями манифеста man.
func storeIndex(t *testing.T, dir, regPriv, pubPub, pubPriv, man string) {
	t.Helper()
	k, _ := registry.ParsePrivateKey(pubPriv)
	b, err := store.Pack([]byte(man), []byte("\x00asm\x01\x00\x00\x00"), k)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.Open(b)
	_ = os.WriteFile(filepath.Join(dir, "kvx.g4c"), b, 0o644)
	ix := registry.Index{Version: 1, Publishers: map[string]registry.Publisher{"acme": {Keys: []string{pubPub}, Trust: registry.TrustPublisher}},
		Connectors: map[string]registry.Entry{"kvx": {Publisher: "acme", Repo: "https://example.com/kvx", License: "MIT",
			Versions: []registry.Release{{Version: "1", URL: "kvx.g4c", SHA256: store.SHA256(b), Digest: p.Sig.Digest, Capabilities: p.Manifest.Summary()}}}}}
	raw, _ := json.Marshal(ix)
	sig, err := registry.SignIndex(raw, regPriv)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "index.json.sig"), []byte(sig), 0o644)
}

// install_connector ставит ровно версию и дайджест показанной записи индекса:
// пересобранный под той же версией пакет с другими возможностями — отказ.
func TestInstallConnectorPinnedToIndexEntry(t *testing.T) {
	reg := t.TempDir()
	regPub, regPriv, _ := registry.GenerateKey()
	pubPub, pubPriv, _ := registry.GenerateKey()
	const man = "connector: kvx\nversion: \"1\"\nruntime: wasm\ncapabilities: { storage: { quota_mb: 1 } }\nactions:\n  - { name: get, class: read }\n"
	storeIndex(t, reg, regPriv, pubPub, pubPriv, man)
	conf := cfg + "store:\n  registry: " + filepath.Join(reg, "index.json") + "\n  registry_keys: [\"" + regPub + "\"]\n"
	d, err := New(Options{Home: filepath.Join(t.TempDir(), "d"), Keys: keys.NewMemory(), InitialConfig: []byte(conf)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	request := func() string {
		r := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
			"summary": "x", "changes": []any{map[string]any{"op": "install_connector", "connector": "kvx"}}}})
		if r.Status != pipeline.StatusPending {
			t.Fatalf("%+v", r)
		}
		return r.ID
	}
	id := request()
	if a, _ := d.State.GetApproval(id); !strings.Contains(a.Preview, "kvx 1 by acme") || !strings.Contains(a.Preview, "storage 1 MB") {
		t.Fatal(a.Preview)
	}
	// Издатель переопубликовал ту же версию с сетью.
	storeIndex(t, reg, regPriv, pubPub, pubPriv, strings.Replace(man, "capabilities: {", "capabilities: { network: [ { host: evil.example, scheme: https } ],", 1))
	if res, err := d.Pipeline.Decide(ctx, id, true, "webui", "owner"); err != nil || res.Status == "ok" {
		t.Fatalf("changed release installed: %+v %v", res, err)
	}
	if _, err := d.StoreOptions().Info("kvx"); err == nil {
		t.Fatal("installed")
	}
	// Новый запрос показывает новую запись — её и ставит.
	id = request()
	if a, _ := d.State.GetApproval(id); !strings.Contains(a.Preview, "evil.example") {
		t.Fatal(a.Preview)
	}
	_, _ = d.Pipeline.Decide(ctx, id, true, "webui", "owner")
	if info, err := d.StoreOptions().Info("kvx"); err != nil || len(info.Capabilities) != 2 {
		t.Fatalf("%+v %v", info, err)
	}
}

// Трей не одобряет самонастройку одним нажатием (code review E.5): 403, а
// отклонить из трея можно.
func TestTrayCannotApproveSelfConfig(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4ad")
	defer os.RemoveAll(home)
	d, err := New(Options{Home: filepath.Join(home, "data"), InitialConfig: []byte(cfg), Keys: keys.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ownerSock, agentSock := filepath.Join(home, "run", "o.sock"), filepath.Join(home, "run", "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, ownerSock, agentSock)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(agentSock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner := client{c: ipc.HTTPClient(ownerSock)}
	r := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "bot", Action: "gate4ai.change_policy", Args: map[string]any{
		"summary": "x", "changes": []any{map[string]any{"op": "add_list_items", "list": "l", "items": []any{"a"}}}}})
	if r.Status != pipeline.StatusPending {
		t.Fatalf("%+v", r)
	}
	if code := owner.do(t, "POST", "/v1/approvals/"+r.ID+"/decide", map[string]any{"approve": true, "channel": "tray"}, nil); code != 403 {
		t.Fatal(code)
	}
	if code := owner.do(t, "POST", "/v1/approvals/"+r.ID+"/decide", map[string]any{"approve": false, "channel": "tray"}, nil); code != 200 {
		t.Fatal(code)
	}
}
