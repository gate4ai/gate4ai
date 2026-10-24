// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/sdk-go/access"
)

// Структурные правки конфигурации — для Web UI и предложений агентов: текущая
// версия разбирается без значений по умолчанию, меняется функцией и сохраняется
// новой версией через Put (с проверкой схемы и CEL). Комментарии YAML при этом
// не сохраняются (см. «Допущения» PLAN.md).

// Decode разбирает YAML в модель без проверки и без значений по умолчанию.
func Decode(raw []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	// Пустой документ (только комментарии) — пустая конфигурация.
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &c, nil
}

// Edit применяет правку f к текущей конфигурации и сохраняет новую версию.
// Правки через Edit идут по одной, а запись — только поверх прочитанной версии:
// если её сменил Put в обход Edit, правка применяется заново к новой версии.
func (s *Store) Edit(p auth.Principal, comment string, f func(c *Config) error) (Version, error) {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	for try := 0; ; try++ {
		raw, n := s.CurrentRaw()
		c, err := Decode(raw)
		if err != nil {
			return Version{}, err
		}
		if err := f(c); err != nil {
			return Version{}, err
		}
		out, err := Marshal(c)
		if err != nil {
			return Version{}, err
		}
		// Комментарий в начале файла (Marshal его не сохраняет) остаётся.
		out = append(LeadingComment(raw), out...)
		v, err := s.PutIf(p, out, comment, n)
		if errors.Is(err, ErrConflict) && try < 3 {
			continue
		}
		return v, err
	}
}

// Preview — YAML конфигурации после правки f (без сохранения) и ошибка проверки.
func (s *Store) Preview(f func(c *Config) error) (before, after []byte, err error) {
	before, _ = s.CurrentRaw()
	c, err := Decode(before)
	if err != nil {
		return before, nil, err
	}
	if err := f(c); err != nil {
		return before, nil, err
	}
	after, err = Marshal(c)
	if err != nil {
		return before, nil, err
	}
	_, err = s.Validate(after)
	return before, after, err
}

// ---- Правки модели ----

// SetAgent добавляет или меняет агента.
func (c *Config) SetAgent(name, profile, description string) error {
	if !idRe.MatchString(name) {
		return fmt.Errorf("agent name %q: use lowercase letters, digits, '.', '-', '_'", name)
	}
	if _, ok := c.Profiles[profile]; !ok {
		return fmt.Errorf("profile %q does not exist", profile)
	}
	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}
	c.Agents[name] = Agent{Profile: profile, Description: description}
	return nil
}

// RemoveAgent удаляет агента.
func (c *Config) RemoveAgent(name string) error {
	if _, ok := c.Agents[name]; !ok {
		return fmt.Errorf("agent %q not found", name)
	}
	delete(c.Agents, name)
	return nil
}

// EnsureProfile создаёт профиль, если его нет.
func (c *Config) EnsureProfile(name string) error {
	if !idRe.MatchString(name) {
		return fmt.Errorf("profile name %q: use lowercase letters, digits, '.', '-', '_'", name)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	if _, ok := c.Profiles[name]; !ok {
		c.Profiles[name] = Profile{Connectors: []string{}}
	}
	return nil
}

// RemoveProfile удаляет профиль, если им не пользуется ни один агент.
func (c *Config) RemoveProfile(name string) error {
	for a, ag := range c.Agents {
		if ag.Profile == name {
			return fmt.Errorf("profile %q is used by agent %q", name, a)
		}
	}
	if _, ok := c.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	delete(c.Profiles, name)
	return nil
}

func (c *Config) profile(name string) (Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("profile %q not found", name)
	}
	return p, nil
}

// SetProfileConnector даёт профилю доступ к коннектору или убирает его.
func (c *Config) SetProfileConnector(profile, connector string, on bool) error {
	p, err := c.profile(profile)
	if err != nil {
		return err
	}
	i := slices.Index(p.Connectors, connector)
	switch {
	case on && i < 0:
		p.Connectors = append(p.Connectors, connector)
	case !on && i >= 0:
		p.Connectors = slices.Delete(p.Connectors, i, i+1)
	}
	if p.Connectors == nil {
		p.Connectors = []string{}
	}
	c.Profiles[profile] = p
	return nil
}

// UniqueRuleID — свободный id правила в профиле на основе base.
func (p Profile) UniqueRuleID(base string) string {
	used := map[string]bool{}
	for _, r := range p.Rules {
		used[r.ID] = true
	}
	for _, l := range p.Limits {
		used[l.ID] = true
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		if id := fmt.Sprintf("%s-%d", base, i); !used[id] {
			return id
		}
	}
}

// PutRule добавляет правило или заменяет правило с тем же id.
func (c *Config) PutRule(profile string, r Rule) error {
	p, err := c.profile(profile)
	if err != nil {
		return err
	}
	if r.ID == "" {
		return errors.New("rule id is required")
	}
	if i := slices.IndexFunc(p.Rules, func(x Rule) bool { return x.ID == r.ID }); i >= 0 {
		p.Rules[i] = r
	} else {
		p.Rules = append(p.Rules, r)
	}
	c.Profiles[profile] = p
	return nil
}

// RemoveRule удаляет правило или лимит по id.
func (c *Config) RemoveRule(profile, id string) error {
	p, err := c.profile(profile)
	if err != nil {
		return err
	}
	n := len(p.Rules) + len(p.Limits)
	p.Rules = slices.DeleteFunc(p.Rules, func(r Rule) bool { return r.ID == id })
	p.Limits = slices.DeleteFunc(p.Limits, func(l Limit) bool { return l.ID == id })
	if len(p.Rules)+len(p.Limits) == n {
		return fmt.Errorf("rule %q not found in profile %q", id, profile)
	}
	c.Profiles[profile] = p
	return nil
}

// MoveRule сдвигает правило на delta позиций (порядок не влияет на решение —
// deny > ask > allow, — но помогает читать).
func (c *Config) MoveRule(profile, id string, delta int) error {
	p, err := c.profile(profile)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(p.Rules, func(r Rule) bool { return r.ID == id })
	if i < 0 {
		return fmt.Errorf("rule %q not found in profile %q", id, profile)
	}
	j := max(0, min(len(p.Rules)-1, i+delta))
	r := p.Rules[i]
	p.Rules = slices.Delete(p.Rules, i, i+1)
	p.Rules = slices.Insert(p.Rules, j, r)
	c.Profiles[profile] = p
	return nil
}

// PutLimit добавляет лимит или заменяет лимит с тем же id.
func (c *Config) PutLimit(profile string, l Limit) error {
	p, err := c.profile(profile)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(p.Limits, func(x Limit) bool { return x.ID == l.ID }); i >= 0 {
		p.Limits[i] = l
	} else {
		p.Limits = append(p.Limits, l)
	}
	c.Profiles[profile] = p
	return nil
}

// SetList задаёт список (пустой — удаляет).
func (c *Config) SetList(name string, items []string) error {
	if !idRe.MatchString(name) {
		return fmt.Errorf("list name %q: use lowercase letters, digits, '.', '-', '_'", name)
	}
	if c.Lists == nil {
		c.Lists = map[string][]string{}
	}
	if len(items) == 0 {
		delete(c.Lists, name)
		return nil
	}
	c.Lists[name] = items
	return nil
}

// SetConnectorSettings задаёт настройки коннектора (nil — удалить раздел).
func (c *Config) SetConnectorSettings(name string, settings map[string]any) {
	if c.Connectors == nil {
		c.Connectors = map[string]map[string]any{}
	}
	if settings == nil {
		delete(c.Connectors, name)
		return
	}
	c.Connectors[name] = settings
}

// SetChannel включает или выключает канал подтверждений.
func (c *Config) SetChannel(name string, on bool) {
	chs := c.Approvals.Channels
	if len(chs) == 0 {
		chs = []string{"desktop"} // значение по умолчанию (Defaults)
	}
	i := slices.Index(chs, name)
	switch {
	case on && i < 0:
		chs = append(chs, name)
	case !on && i >= 0:
		chs = slices.Delete(slices.Clone(chs), i, i+1)
	}
	if len(chs) == 0 {
		chs = []string{"webui"} // пустой список означал бы desktop по умолчанию
	}
	c.Approvals.Channels = chs
}

// ChannelOn — включён ли канал (с учётом значения по умолчанию).
func (c *Config) ChannelOn(name string) bool {
	if len(c.Approvals.Channels) == 0 {
		return name == "desktop"
	}
	return slices.Contains(c.Approvals.Channels, name)
}

// AddSettingItems дополняет список key в настройках коннектора conn (без
// повторов, порядок сохраняется): сайты браузера, корни файлов.
func (c *Config) AddSettingItems(conn, key string, items []string) {
	c.editSettingList(conn, key, func(list []string) []string {
		for _, it := range items {
			if it = strings.TrimSpace(it); it != "" && !slices.Contains(list, it) {
				list = append(list, it)
			}
		}
		return list
	})
}

// RemoveSettingItems убирает элементы из списка key в настройках коннектора.
func (c *Config) RemoveSettingItems(conn, key string, items []string) {
	c.editSettingList(conn, key, func(list []string) []string {
		return slices.DeleteFunc(list, func(s string) bool { return slices.Contains(items, s) })
	})
}

// editSettingList заменяет список key в настройках коннектора результатом f
// (копия настроек: прежняя карта не меняется).
func (c *Config) editSettingList(conn, key string, f func([]string) []string) {
	cur := map[string]any{}
	maps.Copy(cur, c.Connectors[conn])
	list := f(settingList(cur[key]))
	vals := make([]any, len(list))
	for i, v := range list {
		vals[i] = v
	}
	cur[key] = vals
	c.SetConnectorSettings(conn, cur)
}

// SettingList — значение настройки-списка строк (строка — список из неё).
func (c *Config) SettingList(conn, key string) []string { return settingList(c.Connectors[conn][key]) }

func settingList(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return slices.Clone(t)
	}
	return nil
}

// SetSiteAccess задаёт режим домена в списке Браузера; access "" — убирает строку.
func (c *Config) SetSiteAccess(domain, mode string) error {
	if mode != "" {
		if err := access.CheckSite(access.Site{Domain: domain, Access: mode}, access.SiteModes); err != nil {
			return err
		}
	}
	c.Access.Sites = access.SetSite(c.Access.Sites, domain, mode)
	return nil
}

// SetFolderAccess задаёт режим папки в списке Файлов; access "" — убирает строку.
func (c *Config) SetFolderAccess(p, mode string) error {
	if mode != "" {
		if err := access.CheckFolder(access.Folder{Path: p, Access: mode}, access.FolderModes); err != nil {
			return err
		}
	}
	c.Access.Folders = access.SetFolder(c.Access.Folders, p, mode)
	return nil
}

// ConnectorSites — домены коннектора из его настроек (nil — владелец не менял:
// действуют умолчания манифеста).
func (c *Config) ConnectorSites(conn string) []access.Site {
	s, _ := access.ParseSites(c.Connectors[conn]["sites"])
	return s
}

// ConnectorFolders — папки коннектора из его настроек (nil — умолчания манифеста).
func (c *Config) ConnectorFolders(conn string) []access.Folder {
	f, _ := access.ParseFolders(c.Connectors[conn]["folders"])
	return f
}

// SetConnectorSite задаёт режим домена коннектора; base — текущий действующий
// список (умолчания манифеста, если владелец ещё не менял). mode "" — убрать.
func (c *Config) SetConnectorSite(conn string, base []access.Site, domain, mode string) error {
	if mode != "" {
		if err := access.CheckSite(access.Site{Domain: domain, Access: mode}, access.ConnectorSiteModes); err != nil {
			return err
		}
	}
	cur := map[string]any{}
	maps.Copy(cur, c.Connectors[conn])
	if domain == "" {
		cur["sites"] = access.SitesAny(base) // только записать действующий список
	} else {
		cur["sites"] = access.SitesAny(access.SetSite(base, domain, mode))
	}
	c.SetConnectorSettings(conn, cur)
	return nil
}

// SetConnectorFolder задаёт режим папки коннектора (см. SetConnectorSite).
func (c *Config) SetConnectorFolder(conn string, base []access.Folder, p, mode string) error {
	if mode != "" {
		if err := access.CheckFolder(access.Folder{Path: p, Access: mode}, access.ConnectorFolderModes); err != nil {
			return err
		}
	}
	cur := map[string]any{}
	maps.Copy(cur, c.Connectors[conn])
	if p == "" {
		cur["folders"] = access.FoldersAny(base)
	} else {
		cur["folders"] = access.FoldersAny(access.SetFolder(base, p, mode))
	}
	c.SetConnectorSettings(conn, cur)
	return nil
}

// LeadingComment — строки-комментарии в начале YAML (Marshal их не сохраняет).
func LeadingComment(raw []byte) []byte {
	var out []byte
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("#")) {
			break
		}
		out = append(out, line...)
	}
	return out
}
