// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/access"
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
	// Folder — install_dev_connector: папка с исходниками своей сборки (WASM).
	Folder string `json:"folder,omitempty"`
	// Key — add_setting_items / remove_setting_items: настройка-список коннектора.
	Key string `json:"key,omitempty"`
	// Account, CredentialJSON — add_credential: учётка (без значений секретов,
	// только алиасы Vault) в JSON, как в credentials политики.
	Account        string `json:"account,omitempty"`
	CredentialJSON string `json:"credential_json,omitempty"`
	// Domain, Path, Access — set_site_access / set_folder_access (списки
	// Браузера и Файлов) и set_connector_site / set_connector_folder (строки
	// коннектора Connector); Access "" — убрать строку.
	Domain string `json:"domain,omitempty"`
	Path   string `json:"path,omitempty"`
	Access string `json:"access,omitempty"`
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
	// OpInstallDev — собрать WASM-коннектор из папки и поставить как свою
	// сборку (доверие local): агент, который пишет коннектор, ставит его без
	// сокета владельца — под подтверждением change_policy.
	OpInstallDev = "install_dev_connector"
	// OpAddSettingItems / OpRemoveSettingItems — дополнить или сократить
	// настройку-список коннектора (сайты браузера), не заменяя её целиком.
	OpAddSettingItems    = "add_setting_items"
	OpRemoveSettingItems = "remove_setting_items"
	// OpAddCredential — учётка входа на сайт (метаданные; значения секретов
	// кладёт владелец в Vault). Подтверждается человеком всегда.
	OpAddCredential = "add_credential"
	// Списки доступа: домены Браузера, папки Файлов и строки коннекторов.
	OpSetSiteAccess      = "set_site_access"
	OpSetFolderAccess    = "set_folder_access"
	OpSetConnectorSite   = "set_connector_site"
	OpSetConnectorFolder = "set_connector_folder"
)

// ChangeOps — допустимые операции.
var ChangeOps = []string{OpAddRule, OpRemoveRule, OpAddListItems, OpRemoveListItems, OpAddConnector, OpConnectorSetting, OpInstallConnector, OpInstallDev,
	OpAddSettingItems, OpRemoveSettingItems, OpAddCredential, OpSetSiteAccess, OpSetFolderAccess, OpSetConnectorSite, OpSetConnectorFolder}

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
		if err := need("list", ch.List); err != nil {
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
	case OpInstallDev:
		if err := need("folder", ch.Folder); err != nil {
			return err
		}
		if !strings.HasPrefix(ch.Folder, "/") && !strings.HasPrefix(ch.Folder, "~") && !(len(ch.Folder) > 2 && ch.Folder[1] == ':') {
			return errors.New("install_dev_connector: folder must be an absolute path")
		}
	case OpAddSettingItems, OpRemoveSettingItems:
		if err := errors.Join(need("connector", ch.Connector), need("key", ch.Key)); err != nil {
			return err
		}
		if len(ch.Items) == 0 {
			return fmt.Errorf("%s: items are required", ch.Op)
		}
		if slices.ContainsFunc(ch.Items, func(it string) bool { return hasAlias(it) }) || SecretKey(ch.Key) {
			return fmt.Errorf("%s: secrets and vault aliases cannot be set by agents", ch.Op)
		}
	case OpSetSiteAccess:
		if err := need("domain", ch.Domain); err != nil {
			return err
		}
		if ch.Access != "" {
			return access.CheckSite(access.Site{Domain: ch.Domain, Access: ch.Access}, access.SiteModes)
		}
	case OpSetFolderAccess:
		if err := need("path", ch.Path); err != nil {
			return err
		}
		if ch.Access != "" {
			return access.CheckFolder(access.Folder{Path: ch.Path, Access: ch.Access}, access.FolderModes)
		}
	case OpSetConnectorSite:
		if err := errors.Join(need("connector", ch.Connector), need("domain", ch.Domain)); err != nil {
			return err
		}
		if ch.Access != "" {
			return access.CheckSite(access.Site{Domain: ch.Domain, Access: ch.Access}, access.ConnectorSiteModes)
		}
	case OpSetConnectorFolder:
		if err := errors.Join(need("connector", ch.Connector), need("path", ch.Path)); err != nil {
			return err
		}
		if ch.Access != "" {
			return access.CheckFolder(access.Folder{Path: ch.Path, Access: ch.Access}, access.ConnectorFolderModes)
		}
	case OpAddCredential:
		if err := errors.Join(need("account", ch.Account), need("credential_json", ch.CredentialJSON)); err != nil {
			return err
		}
		if !credAliasRe.MatchString(ch.Account) {
			return fmt.Errorf("add_credential: account must be an alias like @site")
		}
		var cr Credential
		if err := json.Unmarshal([]byte(ch.CredentialJSON), &cr); err != nil {
			return fmt.Errorf("add_credential: credential_json: %v", err)
		}
		// Только новые секреты в пространстве самой учётки (@site.password):
		// ссылка на чужой алиас (пароль банка) привязала бы его к сайту агента
		// (code review H4). Существующие в Vault алиасы проверяет daemon.
		for _, a := range cr.Secrets() {
			if !strings.HasPrefix(a, ch.Account+".") {
				return fmt.Errorf("add_credential: secret %s must be a new alias of the account itself (%s.<field>)", a, ch.Account)
			}
		}
	case OpConnectorSetting:
		if err := need("connector", ch.Connector); err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ch.SettingsJSON), &m); err != nil || len(m) == 0 {
			return errors.New("connector_settings: settings_json must be a non-empty JSON object")
		}
		if hasAlias(m) {
			return errors.New("connector_settings: secrets, secret-like keys (key, secret, password, token, credentials) and vault aliases (@…) cannot be set by agents; the owner sets them in the Web UI")
		}
	}
	return nil
}

// SecretKey — ключ настройки о секрете (ключ модели, пароль, токен, учётные
// данные): такие настройки агент не задаёт и не видит. Одна классификация для
// проверки правок и для сводки политики (code review E.6).
func SecretKey(k string) bool {
	lk := strings.ToLower(k)
	for _, w := range []string{"key", "password", "token", "secret", "credential"} {
		if strings.Contains(lk, w) {
			return true
		}
	}
	return false
}

// hasAlias — где-то в значении есть строка-алиас Vault или секретный ключ.
func hasAlias(v any) bool {
	switch x := v.(type) {
	case string:
		return strings.HasPrefix(strings.TrimSpace(x), "@")
	case map[string]any:
		for k, e := range x {
			if SecretKey(k) || hasAlias(e) {
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
	case OpAddConnector, OpInstallConnector, OpInstallDev:
		if ch.Connector == "" {
			return nil // своя сборка ещё не собрана (предпросмотр): имя — из её манифеста
		}
		return c.SetProfileConnector(profile, ch.Connector, true)
	case OpAddSettingItems:
		c.AddSettingItems(ch.Connector, ch.Key, ch.Items)
	case OpRemoveSettingItems:
		c.RemoveSettingItems(ch.Connector, ch.Key, ch.Items)
	case OpSetSiteAccess:
		return c.SetSiteAccess(ch.Domain, ch.Access)
	case OpSetFolderAccess:
		return c.SetFolderAccess(ch.Path, ch.Access)
	case OpSetConnectorSite:
		return c.SetConnectorSite(ch.Connector, c.ConnectorSites(ch.Connector), ch.Domain, ch.Access)
	case OpSetConnectorFolder:
		return c.SetConnectorFolder(ch.Connector, c.ConnectorFolders(ch.Connector), ch.Path, ch.Access)
	case OpAddCredential:
		var cr Credential
		_ = json.Unmarshal([]byte(ch.CredentialJSON), &cr)
		// Агент заводит только новую учётку: перезапись перепривязала бы её
		// секреты к другому сайту (code review H4).
		if _, ok := c.Credentials[ch.Account]; ok {
			return fmt.Errorf("add_credential: account %s already exists; only the owner changes accounts (Web UI)", ch.Account)
		}
		inUse := c.AliasesInUse()
		for _, a := range cr.Secrets() {
			if inUse[a] {
				return fmt.Errorf("add_credential: secret %s is already used in the policy; use a new alias", a)
			}
		}
		if c.Credentials == nil {
			c.Credentials = map[string]Credential{}
		}
		c.Credentials[ch.Account] = cr // проверка всей политики — при сохранении
	case OpConnectorSetting:
		var m map[string]any
		_ = json.Unmarshal([]byte(ch.SettingsJSON), &m)
		cur := map[string]any{}
		maps.Copy(cur, c.Connectors[ch.Connector])
		maps.Copy(cur, m)
		c.SetConnectorSettings(ch.Connector, cur)
	}
	return nil
}

// AliasesInUse — алиасы Vault, на которые уже ссылается политика: секреты
// учёток и строки-алиасы в настройках коннекторов.
func (c *Config) AliasesInUse() map[string]bool {
	out := map[string]bool{}
	for _, cr := range c.Credentials {
		for _, a := range cr.Secrets() {
			out[a] = true
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if t := strings.TrimSpace(x); strings.HasPrefix(t, "@") {
				out[t] = true
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	for _, st := range c.Connectors {
		for _, v := range st {
			walk(v)
		}
	}
	return out
}

// DescribeIn — правка человеческим языком с учётом текущей политики:
// add_rule с id существующего правила показывается как замена с прежним
// правилом (code review E.3). profile — профиль агента-автора.
func (ch Change) DescribeIn(c *Config, profile string) string {
	if ch.Op == OpAddRule && ch.RuleID != "" && c != nil {
		if ch.Profile != "" {
			profile = ch.Profile
		}
		for _, r := range c.Profiles[profile].Rules {
			if r.ID != ch.RuleID {
				continue
			}
			old := Change{Op: OpAddRule, Action: r.Action, When: r.When, Effect: r.Effect, RuleReason: r.Reason}
			return fmt.Sprintf("REPLACE rule %s (was: %s) with: %s", r.ID, old.describe(), ch.Describe())
		}
		return ch.Describe() + " [id " + ch.RuleID + "]"
	}
	return ch.Describe()
}

// Describe — правка человеческим языком.
func (ch Change) Describe() string {
	d := ch.describe()
	if ch.Profile != "" && ch.Op != OpAddListItems && ch.Op != OpRemoveListItems && ch.Op != OpConnectorSetting {
		d += " (profile " + ch.Profile + ")"
	}
	return d
}

func (ch Change) describe() string {
	switch ch.Op {
	case OpAddRule:
		eff := map[string]string{EffectAllow: "allow", EffectAsk: "ask", EffectDeny: "deny"}[ch.Effect]
		s := fmt.Sprintf("Rule: %s %s", eff, ch.Action)
		if ch.When != "" {
			s += ", if " + ch.When
		}
		if ch.RuleReason != "" {
			s += " — “" + ch.RuleReason + "”"
		}
		return s
	case OpRemoveRule:
		return "Remove rule " + ch.RuleID
	case OpAddListItems:
		return fmt.Sprintf("Add to list %s: %s", ch.List, strings.Join(ch.Items, ", "))
	case OpRemoveListItems:
		return fmt.Sprintf("Remove from list %s: %s", ch.List, strings.Join(ch.Items, ", "))
	case OpAddConnector:
		return "Give access to connector " + ch.Connector
	case OpInstallConnector:
		return "Install connector " + ch.Connector + " from the store and give access to it"
	case OpInstallDev:
		s := "Build and install an own build from " + ch.Folder
		if ch.Connector != "" {
			s += " (connector " + ch.Connector + ")"
		}
		return s + " and give access to it"
	case OpConnectorSetting:
		return "Settings of connector " + ch.Connector + ": " + ch.SettingsJSON
	case OpAddSettingItems:
		return fmt.Sprintf("Add to setting %s.%s: %s", ch.Connector, ch.Key, strings.Join(ch.Items, ", "))
	case OpRemoveSettingItems:
		return fmt.Sprintf("Remove from setting %s.%s: %s", ch.Connector, ch.Key, strings.Join(ch.Items, ", "))
	case OpAddCredential:
		return "Account " + ch.Account + " (secret values stay in the vault, you set them): " + ch.CredentialJSON
	case OpSetSiteAccess:
		if ch.Access == "" {
			return "Browser: remove site " + ch.Domain + " from the access list"
		}
		return "Browser: site " + ch.Domain + " — " + ch.Access
	case OpSetFolderAccess:
		if ch.Access == "" {
			return "Files: remove folder " + ch.Path + " from the access list"
		}
		return "Files: folder " + ch.Path + " — " + ch.Access
	case OpSetConnectorSite:
		if ch.Access == "" {
			return "Connector " + ch.Connector + ": remove site " + ch.Domain
		}
		return "Connector " + ch.Connector + ": site " + ch.Domain + " — " + ch.Access
	case OpSetConnectorFolder:
		if ch.Access == "" {
			return "Connector " + ch.Connector + ": remove folder " + ch.Path
		}
		return "Connector " + ch.Connector + ": folder " + ch.Path + " — " + ch.Access
	}
	return ch.Op
}
