// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/state"
)

// SelfConnector — внутренний коннектор самонастройки: агент *предлагает*
// изменение любой части политики, утверждает его владелец в локальном Web UI
// (спецификация, «Безопасность самого шлюза»). Сам коннектор конфигурацию не меняет.
const SelfConnector = "gate4ai"

// defaultMaxOpen — открытых предложений на агента по умолчанию.
const defaultMaxOpen = 5

type selfConnector struct{ d *Daemon }

const changeSchema = `{ type: object, properties: {
  op: { type: string, enum: [add_rule, remove_rule, add_list_items, remove_list_items, add_connector, connector_settings, install_connector],
        description: "add_rule / remove_rule — a rule of a profile; add_list_items / remove_list_items — a policy list; add_connector — give a profile an installed connector; connector_settings — merge connector settings (no secrets); install_connector — install from the store" },
  profile: { type: string, description: "profile to change (add_rule, remove_rule, add_connector, install_connector); empty — your own profile" },
  rule_id: { type: string, description: "rule id (remove_rule; optional for add_rule)" },
  action: { type: string, description: "add_rule: action name or glob, e.g. files.append or email.*" },
  when: { type: string, description: "add_rule: CEL condition over the action arguments, e.g. glob(path, \"~/notes/**\")" },
  effect: { type: string, enum: [allow, ask, deny] },
  rule_reason: { type: string, description: "add_rule: reason shown to agents and in approvals" },
  list: { type: string, description: "list name (lists.<name> in conditions)" },
  items: { type: array, items: { type: string } },
  connector: { type: string },
  settings_json: { type: string, description: "connector_settings: JSON object with non-secret settings, e.g. {\"roots\":[\"~/notes\"]}" } },
  required: [op] }`

func (selfConnector) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: SelfConnector, Version: "1", Runtime: manifest.RuntimeBuiltin,
		Description: "Settings of gate4.ai itself: propose a change to the policy; the owner reviews and approves it",
		Actions: []manifest.Action{
			{Name: "propose_change", Class: "read",
				Description: "Propose a change to gate4.ai policy (rules of any profile, lists, connector settings, installing connectors). Nothing changes until the owner approves it in the Web UI on their computer. Secrets cannot be proposed. Explain why in reason.",
				InputSchema: manifest.MustSchema(`{ type: object, properties: {
				  summary: { type: string, description: "one line: what the change does" },
				  reason: { type: string, description: "why the user needs it" },
				  changes: { type: array, minItems: 1, maxItems: 20, items: ` + changeSchema + ` } },
				  required: [summary, changes] }`),
				Example: `gate4ai gate4ai propose_change --summary "Let me append to notes" --changes '[{"op":"add_rule","action":"files.append","when":"glob(path, \"~/notes/**\")","effect":"allow"}]'`},
			{Name: "proposal_status", Class: "read", Description: "Status of your proposal: open, applied or rejected (with the owner's note)",
				InputSchema: manifest.MustSchema(`{ type: object, properties: { id: { type: string } }, required: [id] }`)},
			{Name: "policy_summary", Class: "read", Description: "Your profile (connectors, rules, limits — what you may do without asking, what needs approval, what is denied), the other profiles and the lists",
				InputSchema: manifest.MustSchema(`{ type: object, properties: {} }`)},
		}}
}

func (s selfConnector) Execute(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	d := s.d
	agent := pipeline.AgentFrom(ctx)
	cfg := d.Config.Current()
	if agent == "" {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown agent")
	}
	if cfg.Proposals.Disabled {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "the owner turned off proposals from agents (Web UI → Settings)")
	}
	switch r.Action {
	case "propose_change":
		return s.propose(agent, r.Args)
	case "proposal_status":
		id, _ := r.Args["id"].(string)
		p, err := d.State.GetProposal(id)
		if err != nil || p.Agent != agent {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "proposal %q not found", id)
		}
		return abi.ExecResult{Structured: map[string]any{"id": p.ID, "status": p.Status, "note": p.Note, "version": p.Version}}, nil
	case "policy_summary":
		return abi.ExecResult{Structured: PolicySummary(cfg, agent)}, nil
	}
	return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown action %s", r.Action)
}

func (s selfConnector) propose(agent string, args map[string]any) (abi.ExecResult, error) {
	d := s.d
	raw, _ := json.Marshal(args["changes"])
	var changes []config.Change
	if err := json.Unmarshal(raw, &changes); err != nil || len(changes) == 0 {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "changes must be a non-empty array")
	}
	for i, ch := range changes {
		if err := config.CheckChange(ch); err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "changes[%d]: %v", i, err)
		}
	}
	// Предпросмотр: правки должны давать корректную конфигурацию (кроме установки
	// коннектора, которой ещё нет в каталоге).
	prof := d.Config.Current().Agents[agent].Profile
	_, _, err := d.Config.Preview(func(c *config.Config) error {
		for _, ch := range changes {
			if ch.Op == config.OpInstallConnector {
				continue
			}
			if err := c.ApplyChange(prof, ch); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "the change would not produce a valid policy: %v", err)
	}
	limit := d.Config.Current().Proposals.MaxOpen
	if limit == 0 {
		limit = defaultMaxOpen
	}
	if open, _ := d.State.ListProposals(state.ProposalOpen, agent, limit+1); len(open) >= limit {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "you already have %d open proposals; wait until the owner reviews them", len(open))
	}
	summary, _ := args["summary"].(string)
	reason, _ := args["reason"].(string)
	cj, _ := json.Marshal(changes)
	p := &state.Proposal{Agent: agent, Summary: strings.TrimSpace(summary), Reason: strings.TrimSpace(reason), Changes: cj}
	if err := d.State.AddProposal(p); err != nil {
		return abi.ExecResult{}, err
	}
	d.notifiers.Proposed(p)
	return abi.ExecResult{Structured: map[string]any{"id": p.ID, "status": "pending_owner",
		"message": "The owner will review the proposal in the gate4.ai Web UI on their computer. Check it with gate4ai.proposal_status."}}, nil
}

func (selfConnector) Verify(_ context.Context, r abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
}

// PolicySummary — профиль агента: коннекторы, правила, лимиты, списки.
func PolicySummary(cfg *config.Config, agent string) map[string]any {
	a, ok := cfg.Agents[agent]
	if !ok {
		return map[string]any{"error": "agent not in configuration"}
	}
	p := cfg.Profiles[a.Profile]
	rules := []map[string]any{}
	for _, r := range p.Rules {
		rules = append(rules, map[string]any{"id": r.ID, "action": r.Action, "when": r.When, "effect": r.Effect, "reason": r.Reason})
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
	return map[string]any{"agent": agent, "profile": a.Profile, "connectors": p.Connectors, "rules": rules, "limits": limits,
		"lists": lists, "other_profiles": others, "defaults": "no rule matched: read → allow; write / external / money → ask; deny beats ask beats allow"}
}

// ApplyProposal применяет предложение (только владелец): правки профиля агента —
// одной новой версией. install — установить коннектор (из магазина, с согласием,
// которое владелец дал на экране предложения); nil — установка не нужна.
func (d *Daemon) ApplyProposal(ctx context.Context, id string, install func(name string) error) (config.Version, error) {
	p, err := d.State.GetProposal(id)
	if err != nil {
		return config.Version{}, err
	}
	if p.Status != state.ProposalOpen {
		return config.Version{}, fmt.Errorf("proposal is already %s", p.Status)
	}
	var changes []config.Change
	if err := json.Unmarshal(p.Changes, &changes); err != nil {
		return config.Version{}, err
	}
	a, ok := d.Config.Current().Agents[p.Agent]
	if !ok {
		return config.Version{}, fmt.Errorf("agent %q is no longer in the configuration", p.Agent)
	}
	for _, ch := range changes {
		if ch.Op == config.OpInstallConnector {
			if _, loaded := d.Registry.Get(ch.Connector); loaded {
				continue
			}
			if install == nil {
				return config.Version{}, errors.New("the proposal installs a connector: confirm its capabilities")
			}
			if err := install(ch.Connector); err != nil {
				return config.Version{}, fmt.Errorf("install %s: %w", ch.Connector, err)
			}
		}
	}
	v, err := d.Config.Edit(auth.OwnerVia("webui"), fmt.Sprintf("agent proposal %s from %s: %s", p.ID, p.Agent, p.Summary), func(c *config.Config) error {
		for _, ch := range changes {
			if err := c.ApplyChange(a.Profile, ch); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return config.Version{}, err
	}
	_ = d.State.DecideProposal(id, state.ProposalApplied, v.N, "")
	return v, nil
}
