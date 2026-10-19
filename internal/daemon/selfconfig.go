// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// SelfConnector — внутренний коннектор самонастройки: вместо внешнего сервиса
// его действия меняют политику самого gate4ai. Это обычные действия под обычной
// политикой (спецификация, «Безопасность самого шлюза»): change_policy — класс
// write, по умолчанию с подтверждением владельца в любом канале; правила могут
// его разрешить или запретить. Секреты и учётки через него не задаются.
const SelfConnector = "gate4ai"

type selfConnector struct{ d *Daemon }

const changeSchema = `{ type: object, properties: {
  op: { type: string, enum: [add_rule, remove_rule, add_list_items, remove_list_items, add_connector, connector_settings, install_connector, install_dev_connector, add_setting_items, remove_setting_items, add_credential],
        description: "add_rule / remove_rule — a rule of a profile; add_list_items / remove_list_items — a policy list; add_connector — give a profile an installed connector; connector_settings — merge connector settings (no secrets); install_connector — install from the store and give the profile access; install_dev_connector — build a WASM connector from your source folder, install it as the owner's own build and give the profile access; add_setting_items / remove_setting_items — add or remove items of a list setting of a connector (sites of the browser: connector browser, key allowed_domains); add_credential — an account to sign in on a site (no secret values: the owner puts them into the vault)" },
  profile: { type: string, description: "profile to change (add_rule, remove_rule, add_connector, install_connector); empty — your own profile" },
  rule_id: { type: string, description: "rule id as policy_summary shows it (remove_rule; optional for add_rule — the same id replaces the rule)" },
  id: { type: string, description: "the same as rule_id" },
  action: { type: string, description: "add_rule: action name or glob, e.g. files.append or email.*" },
  when: { type: string, description: "add_rule: CEL condition over the action arguments, e.g. glob(path, \"~/notes/**\")" },
  effect: { type: string, enum: [allow, ask, deny] },
  rule_reason: { type: string, description: "add_rule: reason shown to agents and in approvals" },
  list: { type: string, description: "list name (lists.<name> in conditions)" },
  items: { type: array, items: { type: string } },
  connector: { type: string },
  settings_json: { type: string, description: "connector_settings: JSON object with non-secret settings, e.g. {\"roots\":[\"~/notes\"]}" },
  folder: { type: string, description: "install_dev_connector: absolute path of the folder with manifest.yaml and Go sources" },
  key: { type: string, description: "add_setting_items / remove_setting_items: the list setting, e.g. allowed_domains" },
  account: { type: string, description: "add_credential: the account alias, e.g. @emias" },
  credential_json: { type: string, description: "add_credential: the account as in credentials of the policy, JSON: {bind, form | via, login_page, prompts, use, connectors}; secrets only as vault aliases" } },
  required: [op] }`

func (selfConnector) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: SelfConnector, Version: "1", Runtime: manifest.RuntimeBuiltin,
		Description: "Settings of gate4.ai itself, under the same policy as any other action",
		Actions: []manifest.Action{
			{Name: "change_policy", Class: "write",
				Description: "Change gate4.ai policy: rules of any profile, lists, connector settings, installing connectors. Like any write action it usually needs the owner's approval. Secrets cannot be set. Explain why in reason.",
				InputSchema: manifest.MustSchema(`{ type: object, properties: {
				  summary: { type: string, description: "one line: what the change does" },
				  reason: { type: string, description: "why the user needs it" },
				  changes: { type: array, minItems: 1, maxItems: 20, description: "JSON array of changes, each {op, …}: add_rule {action, effect, when?, rule_id?, rule_reason?, profile?}; remove_rule {rule_id, profile?}; add_list_items / remove_list_items {list, items}; add_connector / install_connector {connector, profile?}; connector_settings {connector, settings_json}; install_dev_connector {folder}; add_setting_items / remove_setting_items {connector, key, items}; add_credential {account, credential_json}. A rule decides by precedence deny > ask > allow, not by order", items: ` + changeSchema + ` } },
				  required: [summary, changes] }`),
				Example: `gate4ai gate4ai change_policy --summary "Let me append to notes" --changes '[{"op":"add_rule","action":"files.append","when":"glob(path, \"~/notes/**\")","effect":"allow"}]'`},
			{Name: "policy_summary", Class: "read", Description: "Your profile (connectors, rules, limits — what you may do without asking, what needs approval, what is denied), the other profiles and the lists",
				InputSchema: manifest.MustSchema(`{ type: object, properties: {} }`)},
		}}
}

// changesOf — правки из аргументов change_policy.
func changesOf(args map[string]any) ([]config.Change, error) {
	raw, _ := json.Marshal(args["changes"])
	var changes []config.Change
	if err := json.Unmarshal(raw, &changes); err != nil || len(changes) == 0 {
		return nil, fmt.Errorf("changes must be a non-empty array")
	}
	var ids []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &ids)
	for i := range changes {
		if changes[i].RuleID == "" && i < len(ids) {
			changes[i].RuleID = ids[i].ID // id — как в policy_summary
		}
	}
	for i, ch := range changes {
		if err := config.CheckChange(ch); err != nil {
			return nil, fmt.Errorf("changes[%d]: %v", i, err)
		}
	}
	return changes, nil
}

// applyChanges применяет правки; skipMissing — пропустить установку ещё не
// установленных коннекторов (предпросмотр до установки).
func (d *Daemon) applyChanges(c *config.Config, agent string, changes []config.Change, skipMissing bool) error {
	prof := c.Agents[agent].Profile
	for _, ch := range changes {
		if ch.Op == config.OpInstallConnector && skipMissing {
			if _, ok := d.Registry.Get(ch.Connector); !ok {
				continue
			}
		}
		if err := c.ApplyChange(prof, ch); err != nil {
			return err
		}
	}
	return nil
}

// describeSelf — проверка change_policy до решения политики и текст для
// запроса подтверждения: правки человеческим языком и возможности
// устанавливаемых коннекторов.
func (d *Daemon) describeSelf(agent string, args map[string]any) (string, error) {
	if _, ok := args["changes"]; !ok {
		return "", nil // policy_summary
	}
	changes, err := changesOf(args)
	if err != nil {
		return "", err
	}
	devs := map[int]manifest.Manifest{}
	for i := range changes {
		if changes[i].Op != config.OpInstallDev {
			continue
		}
		m, err := devManifest(changes[i].Folder)
		if err != nil {
			return "", err
		}
		devs[i] = m
		changes[i].Connector = m.Connector
	}
	if _, _, err := d.Config.Preview(func(c *config.Config) error { return d.applyChanges(c, agent, changes, true) }); err != nil {
		return "", fmt.Errorf("the change would not produce a valid policy: %v", err)
	}
	var b strings.Builder
	if s, _ := args["summary"].(string); s != "" {
		fmt.Fprintf(&b, "%s\n", s)
	}
	if s, _ := args["reason"].(string); s != "" {
		fmt.Fprintf(&b, "Why: %s\n", s)
	}
	b.WriteString("Changes to the gate4.ai policy:\n")
	for i, ch := range changes {
		fmt.Fprintf(&b, "  • %s\n", ch.Describe())
		if m, ok := devs[i]; ok {
			fmt.Fprintf(&b, "    %s %s, own build (not reviewed), capabilities: %s\n", m.Connector, m.Version, strings.Join(m.Summary(), "; "))
			continue
		}
		if ch.Op != config.OpInstallConnector {
			continue
		}
		if _, ok := d.Registry.Get(ch.Connector); ok {
			continue
		}
		if ix := d.StoreOptions().CachedIndex(); ix != nil {
			if e, rel, err := ix.Resolve(ch.Connector, ""); err == nil {
				fmt.Fprintf(&b, "    %s %s by %s, capabilities: %s\n", ch.Connector, rel.Version, e.Publisher, strings.Join(rel.Capabilities, "; "))
			}
		}
	}
	return b.String(), nil
}

func (s selfConnector) Execute(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	d := s.d
	agent := pipeline.AgentFrom(ctx)
	if agent == "" {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown agent")
	}
	switch r.Action {
	case "policy_summary":
		return abi.ExecResult{Structured: PolicySummary(d.Config.Current(), agent)}, nil
	case "change_policy":
		changes, err := changesOf(r.Args)
		if err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
		// Установка коннектора: решение по действию покрывает согласие на его
		// возможности (они перечислены в запросе подтверждения).
		for i, ch := range changes {
			if ch.Op == config.OpInstallDev {
				name, err := d.installDevFolder(ctx, ch.Folder)
				if err != nil {
					return abi.ExecResult{}, fmt.Errorf("install %s: %w", ch.Folder, err)
				}
				changes[i].Connector = name
				continue
			}
			if ch.Op != config.OpInstallConnector {
				continue
			}
			if _, ok := d.Registry.Get(ch.Connector); ok {
				continue
			}
			plan, err := d.PrepareConnector(ctx, ch.Connector)
			if err != nil {
				return abi.ExecResult{}, fmt.Errorf("install %s: %w", ch.Connector, err)
			}
			if _, _, _, err := d.InstallConnector(ctx, ch.Connector, plan.Capabilities); err != nil {
				return abi.ExecResult{}, fmt.Errorf("install %s: %w", ch.Connector, err)
			}
		}
		summary, _ := r.Args["summary"].(string)
		v, err := d.Config.Edit(auth.OwnerVia(SelfConnector), fmt.Sprintf("%s.change_policy by %s: %s", SelfConnector, agent, summary),
			func(c *config.Config) error { return d.applyChanges(c, agent, changes, false) })
		if err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
		return abi.ExecResult{Structured: map[string]any{"status": "applied", "config_version": v.N}}, nil
	}
	return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown action %s", r.Action)
}

func (selfConnector) Verify(_ context.Context, r abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
}

// PolicySummary — профиль агента (коннекторы, правила, лимиты), остальные профили и списки.
func PolicySummary(cfg *config.Config, agent string) map[string]any {
	a, ok := cfg.Agents[agent]
	if !ok {
		return map[string]any{"error": "agent not in configuration"}
	}
	p := cfg.Profiles[a.Profile]
	rules := []map[string]any{}
	for _, r := range p.Rules {
		rules = append(rules, map[string]any{"rule_id": r.ID, "action": r.Action, "when": r.When, "effect": r.Effect, "reason": r.Reason})
	}
	limits := []map[string]any{}
	for _, l := range p.Limits {
		limits = append(limits, map[string]any{"id": l.ID, "action": l.Action, "per_day": l.PerDay, "amount_per_day": l.AmountPerDay})
	}
	lists := map[string]int{}
	for n, it := range cfg.Lists {
		lists[n] = len(it)
	}
	others := map[string][]string{}
	for n, op := range cfg.Profiles {
		if n != a.Profile {
			others[n] = op.Connectors
		}
	}
	// Несекретные настройки коннекторов профиля: агент видит, например, свои
	// сайты браузера, прежде чем просить добавить сайт (add_setting_items).
	settings := map[string]map[string]any{}
	for _, conn := range p.Connectors {
		for k, v := range cfg.Connectors[conn] {
			if secretSetting(k, v) {
				continue
			}
			if settings[conn] == nil {
				settings[conn] = map[string]any{}
			}
			settings[conn][k] = v
		}
	}
	return map[string]any{"agent": agent, "profile": a.Profile, "connectors": p.Connectors, "rules": rules, "limits": limits, "settings": settings,
		"lists": lists, "other_profiles": others, "defaults": "no rule matched: read → allow; write / external / money → ask; deny beats ask beats allow"}
}

// devManifest — манифест своей сборки в папке: только WASM (нативный код
// без песочницы агент поставить не может).
func devManifest(folder string) (manifest.Manifest, error) {
	dir, ok := store.SourceFolder(expandHome(folder))
	if !ok {
		return manifest.Manifest{}, fmt.Errorf("install_dev_connector: no %s in %s", store.ManifestFile, folder)
	}
	raw, err := os.ReadFile(filepath.Join(dir, store.ManifestFile))
	if err != nil {
		return manifest.Manifest{}, err
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return manifest.Manifest{}, err
	}
	if m.Runtime != manifest.RuntimeWasm {
		return manifest.Manifest{}, fmt.Errorf("install_dev_connector: only runtime wasm (%s is %s)", m.Connector, m.Runtime)
	}
	return m, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// installDevFolder собирает коннектор из папки (как `gate4ai connectors
// install <папка>`) и ставит его с согласием на объявленные возможности:
// согласие дал владелец, подтвердив change_policy.
func (d *Daemon) installDevFolder(ctx context.Context, folder string) (string, error) {
	m, err := devManifest(folder)
	if err != nil {
		return "", err
	}
	dir, _ := store.SourceFolder(expandHome(folder))
	opts := d.StoreOptions()
	plan, err := opts.Prepare(ctx, dir, true)
	if err != nil {
		return "", err
	}
	if plan.Connector != m.Connector {
		return "", fmt.Errorf("the folder changed while installing: %s, not %s", plan.Connector, m.Connector)
	}
	d.installMu.Lock()
	defer d.installMu.Unlock()
	info, err := opts.Install(plan, plan.Capabilities)
	if err != nil {
		return "", err
	}
	if err := d.reloadConnector(info.Connector, false); err != nil {
		d.Log.Warn("connector reloaded with a warning", "connector", info.Connector, "err", err)
	}
	d.Log.Info("own build installed by an agent", "connector", info.Connector, "version", info.Version, "folder", dir)
	return info.Connector, nil
}

// alwaysAskSelf — установка своей сборки (новый код в daemon) подтверждается
// человеком всегда: правило allow на change_policy её не разрешает.
func alwaysAskSelf(args map[string]any) string {
	changes, err := changesOf(args)
	if err != nil {
		return ""
	}
	for _, ch := range changes {
		switch ch.Op {
		case config.OpInstallDev:
			return "installing an own build (new code in gate4ai) always needs the owner's approval"
		case config.OpAddCredential:
			return "an account decides where your secrets are entered: it always needs the owner's approval"
		}
	}
	return ""
}

// secretSetting — настройка со значением-алиасом Vault или о секрете (ключ модели, пароль).
func secretSetting(k string, v any) bool {
	lk := strings.ToLower(k)
	if strings.Contains(lk, "key") || strings.Contains(lk, "password") || strings.Contains(lk, "token") || strings.Contains(lk, "secret") || k == "credentials" {
		return true
	}
	s, ok := v.(string)
	return ok && strings.HasPrefix(strings.TrimSpace(s), "@")
}
