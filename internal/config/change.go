// SPDX-License-Identifier: FSL-1.1-ALv2

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Change — одна правка политики из действия агента gate4ai.change_policy.
// Действие проходит политику как любое другое (класс write: по умолчанию —
// подтверждение владельца в любом канале); правка может касаться любого
// профиля (Profile; пусто — профиль агента); секреты и учётки не задаются.
type Change struct {
	Op           string   `json:"op"`
	Profile      string   `json:"profile,omitempty"`
	RuleID       string   `json:"rule_id,omitempty"`
	Action       string   `json:"action,omitempty"`
	When         string   `json:"when,omitempty"`
	Effect       string   `json:"effect,omitempty"`
	RuleReason   string   `json:"rule_reason,omitempty"`
	List         string   `json:"list,omitempty"`
	Items        []string `json:"items,omitempty"`
	Connector    string   `json:"connector,omitempty"`
	SettingsJSON string   `json:"settings_json,omitempty"`
}

// Операции правок.
const (
	OpAddRule          = "add_rule"           // добавить / заменить правило профиля
	OpRemoveRule       = "remove_rule"        // удалить правило профиля
	OpAddListItems     = "add_list_items"     // дополнить список
	OpRemoveListItems  = "remove_list_items"  // убрать из списка
	OpAddConnector     = "add_connector"      // дать профилю установленный коннектор
	OpConnectorSetting = "connector_settings" // объединить настройки коннектора
	OpInstallConnector = "install_connector"  // установить коннектор из магазина (согласие — у владельца)
)

// ChangeOps — допустимые операции.
var ChangeOps = []string{OpAddRule, OpRemoveRule, OpAddListItems, OpRemoveListItems, OpAddConnector, OpConnectorSetting, OpInstallConnector}

// CheckChange проверяет правку до решения политики: форму и запрет секретов.
func CheckChange(ch Change) error {
	if !slices.Contains(ChangeOps, ch.Op) {
		return fmt.Errorf("unknown op %q (one of %s)", ch.Op, strings.Join(ChangeOps, ", "))
	}
	if ch.Profile != "" && !idRe.MatchString(ch.Profile) {
		return fmt.Errorf("%s: invalid profile %q", ch.Op, ch.Profile)
	}
	need := func(name, v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s: %s is required", ch.Op, name)
		}
		return nil
	}
	switch ch.Op {
	case OpAddRule:
		if err := errors.Join(need("action", ch.Action), need("effect", ch.Effect)); err != nil {
			return err
		}
		if !actionRe.MatchString(ch.Action) {
			return fmt.Errorf("add_rule: invalid action %q", ch.Action)
		}
		switch ch.Effect {
		case EffectAllow, EffectAsk, EffectDeny:
		default:
			return fmt.Errorf("add_rule: effect must be allow, ask or deny")
		}
		if ch.RuleID != "" && !idRe.MatchString(ch.RuleID) {
			return fmt.Errorf("add_rule: invalid rule_id %q", ch.RuleID)
		}
	case OpRemoveRule:
		return need("rule_id", ch.RuleID)
	case OpAddListItems, OpRemoveListItems:
		if err := errors.Join(need("list", ch.List)); err != nil {
			return err
		}
		if len(ch.Items) == 0 {
			return fmt.Errorf("%s: items are required", ch.Op)
		}
		for _, it := range ch.Items {
			if strings.HasPrefix(strings.TrimSpace(it), "@") {
				return fmt.Errorf("%s: vault aliases (@…) cannot be set by agents", ch.Op)
			}
		}
	case OpAddConnector, OpInstallConnector:
		return need("connector", ch.Connector)
	case OpConnectorSetting:
		if err := need("connector", ch.Connector); err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ch.SettingsJSON), &m); err != nil || len(m) == 0 {
			return errors.New("connector_settings: settings_json must be a non-empty JSON object")
		}
		if hasAlias(m) {
			return errors.New("connector_settings: secrets and vault aliases (@…) cannot be set by agents; the owner sets them in the Web UI")
		}
	}
	return nil
}

// hasAlias — где-то в значении есть строка-алиас Vault.
func hasAlias(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.HasPrefix(strings.TrimSpace(x), "@")
	case map[string]any:
		for k, e := range x {
			if strings.Contains(strings.ToLower(k), "password") || strings.Contains(strings.ToLower(k), "token") || hasAlias(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if hasAlias(e) {
				return true
			}
		}
	}
	return false
}

// ApplyChange применяет правку к профилю ch.Profile, а без него — к profile
// (профиль агента-автора). install_connector здесь не исполняется — его ставит
// владелец через магазин, а в профиль коннектор добавляет следом.
func (c *Config) ApplyChange(profile string, ch Change) error {
	if err := CheckChange(ch); err != nil {
		return err
	}
	if ch.Profile != "" {
		profile = ch.Profile
	}
	switch ch.Op {
	case OpAddRule:
		p, err := c.profile(profile)
		if err != nil {
			return err
		}
		id := ch.RuleID
		if id == "" {
			id = p.UniqueRuleID("agent-" + strings.NewReplacer(".", "-", "*", "all").Replace(ch.Action))
		}
		return c.PutRule(profile, Rule{ID: id, Action: ch.Action, When: ch.When, Effect: ch.Effect, Reason: ch.RuleReason})
	case OpRemoveRule:
		return c.RemoveRule(profile, ch.RuleID)
	case OpAddListItems:
		items := c.Lists[ch.List]
		for _, it := range ch.Items {
			if !slices.Contains(items, it) {
				items = append(items, it)
			}
		}
		return c.SetList(ch.List, items)
	case OpRemoveListItems:
		items := slices.DeleteFunc(slices.Clone(c.Lists[ch.List]), func(s string) bool { return slices.Contains(ch.Items, s) })
		return c.SetList(ch.List, items)
	case OpAddConnector, OpInstallConnector:
		return c.SetProfileConnector(profile, ch.Connector, true)
	case OpConnectorSetting:
		var m map[string]any
		_ = json.Unmarshal([]byte(ch.SettingsJSON), &m)
		cur := map[string]any{}
		for k, v := range c.Connectors[ch.Connector] {
			cur[k] = v
		}
		for k, v := range m {
			cur[k] = v
		}
		c.SetConnectorSettings(ch.Connector, cur)
	}
	return nil
}

// Describe — правка человеческим языком.
func (ch Change) Describe() string {
	d := ch.describe()
	if ch.Profile != "" && ch.Op != OpAddListItems && ch.Op != OpRemoveListItems && ch.Op != OpConnectorSetting {
		d += " (профиль " + ch.Profile + ")"
	}
	return d
}

func (ch Change) describe() string {
	switch ch.Op {
	case OpAddRule:
		eff := map[string]string{EffectAllow: "разрешать", EffectAsk: "спрашивать", EffectDeny: "запрещать"}[ch.Effect]
		s := fmt.Sprintf("Правило: %s %s", eff, ch.Action)
		if ch.When != "" {
			s += ", если " + ch.When
		}
		if ch.RuleReason != "" {
			s += " — «" + ch.RuleReason + "»"
		}
		return s
	case OpRemoveRule:
		return "Удалить правило " + ch.RuleID
	case OpAddListItems:
		return fmt.Sprintf("Добавить в список %s: %s", ch.List, strings.Join(ch.Items, ", "))
	case OpRemoveListItems:
		return fmt.Sprintf("Убрать из списка %s: %s", ch.List, strings.Join(ch.Items, ", "))
	case OpAddConnector:
		return "Дать доступ к коннектору " + ch.Connector
	case OpInstallConnector:
		return "Установить коннектор " + ch.Connector + " из магазина и дать к нему доступ"
	case OpConnectorSetting:
		return "Настройки коннектора " + ch.Connector + ": " + ch.SettingsJSON
	}
	return ch.Op
}
