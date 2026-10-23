// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// SelfConnector — внутренний коннектор самонастройки: вместо внешнего сервиса
// его действия меняют политику самого gate4ai. Это обычные действия под обычной
// политикой (спецификация, «Безопасность самого шлюза»): change_policy — класс
// write, по умолчанию с подтверждением владельца в любом канале; правила могут
// его разрешить или запретить. Значения секретов через него не задаются; новая
// учётка (add_credential, только с новыми алиасами в своём пространстве имён),
// своя сборка и настройки, от которых зависит, куда уходят секреты, — только
// с подтверждением человека (alwaysAskSelf).
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
  key: { type: string, description: "add_setting_items / remove_setting_items: the list setting. browser: allowed_domains (sites agents may open: app.example.org, *.example.org), blocked (sites closed with mode blocklist), redact_params (URL parameters and JSON fields hidden in network and responses), download_types (file types browser.download returns: application/pdf, image/*, .csv); files: roots (allowed folders), allow_paths (protected paths opened). Other settings — connector_settings: browser mode (allowlist | blocklist), max_text_chars, model, max_download_bytes, download_dir; files allow_hidden, max_bytes" },
  account: { type: string, description: "add_credential: the account alias, e.g. @emias" },
  credential_json: { type: string, description: "add_credential: a NEW account as in credentials of the policy, JSON: {bind, form | via, login_page, prompts, use, connectors}; secrets only as new vault aliases of the account itself (@site.password for @site) — the owner puts the values into the vault" } },
  required: [op] }`

func (selfConnector) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: SelfConnector, Version: "1", Runtime: manifest.RuntimeBuiltin,
		Description: "Settings of gate4.ai itself, under the same policy as any other action",
		Actions: []manifest.Action{
			{Name: "change_policy", Class: "write",
				Description: "Change gate4.ai policy: rules of any profile, lists, connector settings, installing connectors. Like any write action it usually needs the owner's approval. Secrets cannot be set. Explain why in reason.",
				InputSchema: manifest.MustSchema(`{ type: object, properties: {
				  summary: { type: string, maxLength: 200, description: "one line: what the change does" },
				  reason: { type: string, maxLength: 1000, description: "why the user needs it" },
				  changes: { type: array, minItems: 1, maxItems: 20, description: "JSON array of changes, each {op, …}: add_rule {action, effect, when?, rule_id?, rule_reason?, profile?}; remove_rule {rule_id, profile?}; add_list_items / remove_list_items {list, items}; add_connector / install_connector {connector, profile?}; connector_settings {connector, settings_json}; install_dev_connector {folder}; add_setting_items / remove_setting_items {connector, key, items}; add_credential {account, credential_json}. Example — a site for the browser: [{\"op\":\"add_setting_items\",\"connector\":\"browser\",\"key\":\"allowed_domains\",\"items\":[\"grafana.example.net\"]}]. A rule decides by precedence deny > ask > allow, not by order", items: ` + changeSchema + ` } },
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
// устанавливаемых коннекторов. Показанное человеку закрепляется в аргументах
// запроса (pinsArg): исполняется только оно (code review K2).
func (d *Daemon) describeSelf(agent string, args map[string]any) (string, error) {
	if _, ok := args["changes"]; !ok {
		return "", nil // policy_summary
	}
	changes, err := changesOf(args)
	if err != nil {
		return "", err
	}
	pins, err := d.pinChanges(args, changes)
	if err != nil {
		return "", err
	}
	for i, pin := range pins {
		if pin.Op == config.OpInstallDev {
			changes[i].Connector = pin.Connector
		}
	}
	if err := d.checkCredentials(changes); err != nil {
		return "", err
	}
	if _, _, err := d.Config.Preview(func(c *config.Config) error { return d.applyChanges(c, agent, changes, true) }); err != nil {
		return "", fmt.Errorf("the change would not produce a valid policy: %v", err)
	}
	cur := d.Config.Current()
	prof := cur.Agents[agent].Profile
	// Сначала — правки, как их понимает gate4ai; текст агента — после них,
	// одной строкой и с пометкой (code review E.4): им не нарисовать
	// фальшивый блок правок.
	var b strings.Builder
	b.WriteString("Changes to the gate4.ai policy:\n")
	for i, ch := range changes {
		fmt.Fprintf(&b, "  • %s\n", oneLine(ch.DescribeIn(cur, prof), 600))
		for _, w := range d.settingWarnings(ch) {
			fmt.Fprintf(&b, "    ⚠ %s\n", oneLine(w, 600))
		}
		if pin, ok := pins[i]; ok {
			for _, l := range pin.lines() {
				fmt.Fprintf(&b, "    %s\n", oneLine(l, 1500))
			}
		}
	}
	summary, _ := args["summary"].(string)
	reason, _ := args["reason"].(string)
	if summary != "" || reason != "" {
		b.WriteString("Agent's own words (not checked by gate4.ai):\n")
		if summary != "" {
			fmt.Fprintf(&b, "  summary: %s\n", oneLine(summary, 200))
		}
		if reason != "" {
			fmt.Fprintf(&b, "  why: %s\n", oneLine(reason, 500))
		}
	}
	return b.String(), nil
}

// oneLine — текст агента или его манифеста в запросе подтверждения: одна
// строка без управляющих символов, не длиннее n символов.
func oneLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
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
		// Установка коннектора: решение по действию покрывает согласие ровно
		// на те возможности, что были в запросе (закреплены в аргументах при
		// его создании); другое не ставится (code review K2).
		pins, err := readPins(r.Args)
		if err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
		if err := d.checkCredentials(changes); err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
		for i, ch := range changes {
			pin, pinned := pins[i]
			if pinned && pin.Op != ch.Op {
				return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "changes[%d]: the approved request does not match", i)
			}
			if ch.Op == config.OpInstallDev {
				if !pinned {
					return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "changes[%d]: own build was not shown to the owner; ask again", i)
				}
				name, err := d.installDevFolder(ctx, ch.Folder, pin)
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
			if !pinned {
				return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "changes[%d]: connector %s was not shown to the owner; ask again", i, ch.Connector)
			}
			if err := d.installStorePinned(ctx, pin); err != nil {
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

// devManifest — манифест своей сборки в папке и его байты: только WASM
// (нативный код без песочницы агент поставить не может).
func devManifest(folder string) (manifest.Manifest, []byte, error) {
	dir, ok := store.SourceFolder(expandHome(folder))
	if !ok {
		return manifest.Manifest{}, nil, fmt.Errorf("install_dev_connector: no %s in %s", store.ManifestFile, folder)
	}
	raw, err := os.ReadFile(filepath.Join(dir, store.ManifestFile))
	if err != nil {
		return manifest.Manifest{}, nil, err
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return manifest.Manifest{}, nil, err
	}
	if m.Runtime != manifest.RuntimeWasm {
		return manifest.Manifest{}, nil, fmt.Errorf("install_dev_connector: only runtime wasm (%s is %s)", m.Connector, m.Runtime)
	}
	return m, raw, nil
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
// install <папка>`) и ставит его с согласием на возможности из запроса:
// исходники и манифест должны совпасть с показанными владельцу, собирается
// только WASM, согласие — ровно на закреплённые возможности.
func (d *Daemon) installDevFolder(ctx context.Context, folder string, pin selfPin) (string, error) {
	dir, ok := store.SourceFolder(expandHome(folder))
	if !ok {
		return "", fmt.Errorf("install_dev_connector: no %s in %s", store.ManifestFile, folder)
	}
	if sum, err := sourceDigest(dir); err != nil {
		return "", err
	} else if sum != pin.SourceSHA256 {
		return "", errors.New("the folder changed after the request was shown to the owner; ask again")
	}
	opts := d.StoreOptions()
	plan, err := opts.PrepareDevWasm(ctx, dir, pin.ManifestSHA256)
	if err != nil {
		return "", err
	}
	if err := pin.matches(plan); err != nil {
		return "", err
	}
	info, _, err := d.installPlan(opts, plan, pin.Capabilities)
	if err != nil {
		return "", err
	}
	d.Log.Info("own build installed by an agent", "connector", info.Connector, "version", info.Version, "folder", dir)
	return info.Connector, nil
}

// installStorePinned ставит из магазина ровно ту версию (и дайджест) из
// записи индекса, что показали владельцу, с согласием на её возможности.
func (d *Daemon) installStorePinned(ctx context.Context, pin selfPin) error {
	opts := d.StoreOptions()
	plan, err := opts.Prepare(ctx, pin.Connector+"@"+pin.Version, false)
	if err != nil {
		return err
	}
	if ix := opts.CachedIndex(); ix == nil {
		return errors.New("store index is not available")
	} else if _, rel, err := ix.Resolve(pin.Connector, pin.Version); err != nil || rel.Digest != pin.Digest {
		return fmt.Errorf("connector %s %s changed in the store after the request was shown to the owner; ask again", pin.Connector, pin.Version)
	}
	if err := pin.matches(plan); err != nil {
		return err
	}
	_, _, err = d.installPlan(opts, plan, pin.Capabilities)
	return err
}

// alwaysAskSelf — правки, которые подтверждает только человек, даже при
// правиле allow на change_policy: своя сборка (новый код в daemon), учётка и
// настройки, от которых зависит, куда коннектор ходит в сеть и отправляет
// секреты (code review H4).
func (d *Daemon) alwaysAskSelf(args map[string]any) string {
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
		if len(d.settingWarnings(ch)) > 0 {
			return "the change moves where a connector connects or sends secrets: it always needs the owner's approval"
		}
	}
	return ""
}

// secretSetting — настройка со значением-алиасом Vault или о секрете (ключ модели, пароль).
func secretSetting(k string, v any) bool {
	if config.SecretKey(k) {
		return true
	}
	s, ok := v.(string)
	return ok && strings.HasPrefix(strings.TrimSpace(s), "@")
}
