// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// ---- Коннекторы ----

// storeItem — карточка магазина.
type storeItem struct {
	Name, Title, Description, Publisher, Trust, License, Repo, Version string
	Capabilities                                                       []string
	Installed                                                          string // установленная версия
	Loaded, Update, Revoked                                            bool
	Builtin                                                            bool   // часть ядра, из магазина не ставится
	State                                                              string // нативный пакет: stopped | running | disabled
	Profiles                                                           []string
	Agents                                                             []string // агенты с доступом
}

func (u *UI) storeItems(ctx context.Context, l string, refresh bool) ([]storeItem, string) {
	opts := u.D.StoreOptions()
	ix := opts.CachedIndex()
	var ierr string
	if (ix == nil || refresh) && opts.Registry != "" {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if nix, err := opts.RefreshIndex(cctx); err == nil {
			ix = nix
		} else {
			ierr = err.Error()
		}
		cancel()
	} else if opts.Registry == "" {
		ierr = "the store is not configured: set the store address in Settings → Advanced"
	}
	inst := u.installed()
	cfg := u.D.Config.Current()
	profilesOf := func(conn string) []string {
		var out []string
		for n, p := range cfg.Profiles {
			if p.HasConnector(conn) {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	seen := map[string]bool{}
	var items []storeItem
	if ix != nil {
		for name, e := range ix.Connectors {
			if slices.Contains(daemon.BuiltinConnectors, name) {
				continue
			}
			it := storeItem{Name: name, Description: e.Description, Publisher: e.Publisher, License: e.License, Repo: e.Repo}
			if p, ok := ix.Publishers[e.Publisher]; ok {
				it.Trust = p.Trust
			}
			if _, rel, err := ix.Resolve(name, ""); err == nil {
				it.Version, it.Capabilities = rel.Version, rel.Capabilities
			}
			if in, ok := inst[name]; ok {
				it.Installed = in.Manifest.Version
				var c connectors.Client
				c, it.Loaded = u.D.Registry.Get(name)
				if st, ok := c.(interface{ State() string }); ok {
					it.State = st.State()
				}
				it.Update = it.Version != "" && it.Version != it.Installed
				it.Revoked, _ = ix.IsRevoked(name, it.Installed)
			}
			it.Profiles = profilesOf(name)
			seen[name] = true
			items = append(items, it)
		}
	}
	for _, name := range daemon.BuiltinConnectors {
		c, ok := u.D.Registry.Get(name)
		if !ok || policy.Implicit[name] || policy.Everyone[name] {
			continue
		}
		m := c.Manifest()
		seen[name] = true
		items = append(items, storeItem{Name: name, Description: m.Description, Publisher: "gate4ai", Trust: "verified", Installed: m.Version,
			Version: m.Version, Loaded: true, Builtin: true, Capabilities: m.Summary(), Profiles: profilesOf(name)})
	}
	for name, in := range inst {
		if seen[name] {
			continue
		}
		_, loaded := u.D.Registry.Get(name)
		items = append(items, storeItem{Name: name, Description: in.Manifest.Description, Publisher: in.Info.Publisher, Trust: in.Info.Trust,
			Installed: in.Manifest.Version, Version: in.Manifest.Version, Loaded: loaded, Capabilities: in.Info.Capabilities, Profiles: profilesOf(name)})
	}
	for i := range items {
		it := &items[i]
		it.Title = u.connTitle(l, it.Name)
		if c, ok := u.D.Registry.Get(it.Name); ok {
			it.Description = localized(c.Manifest()).DescriptionIn(l)
		} else {
			it.Description = connDescription(l, it.Name, it.Description)
		}
		for a, ag := range cfg.Agents {
			if slices.Contains(it.Profiles, ag.Profile) {
				it.Agents = append(it.Agents, a)
			}
		}
		sort.Strings(it.Agents)
	}
	// Установленные — первыми.
	sort.Slice(items, func(i, j int) bool {
		if (items[i].Installed != "") != (items[j].Installed != "") {
			return items[i].Installed != ""
		}
		return items[i].Name < items[j].Name
	})
	return items, ierr
}

func (u *UI) connectorsPage(r *http.Request) (string, map[string]any, error) {
	l := u.lang(r)
	items, ierr := u.storeItems(r.Context(), l, false)
	cfg := u.D.Config.Current()
	data := map[string]any{"Items": items, "IndexError": ierr, "Grants": grants(cfg), "Registry": cfg.Store.Registry}
	if src := strings.TrimSpace(r.URL.Query().Get("install")); src != "" {
		// Не из магазина: папка с исходниками, файл .g4c на этом компьютере или адрес .g4c.
		local := r.URL.Query().Get("local") == "1"
		dev := local && r.URL.Query().Get("dev") == "1"
		data["Src"], data["Local"], data["Dev"] = src, local, dev
		if local {
			src = expandHome(src)
		}
		plan, err := u.D.PrepareConnectorDev(r.Context(), src, dev)
		if err != nil {
			return "connectors", data, M("%s: %s", src, err)
		}
		data["Src"] = src
		data["Plan"] = plan
		data["PlanTitle"] = u.connTitle(l, plan.Connector)
		data["PlanCaps"] = groupCapabilities(l, plan.Capabilities)
	}
	return "connectors", data, nil
}

func (u *UI) installConnector(w http.ResponseWriter, r *http.Request) map[string]any {
	src := r.PostForm.Get("source")
	consent := r.PostForm["consent"]
	if consent == nil {
		consent = []string{}
	}
	local, dev := r.PostForm.Get("local") == "1", r.PostForm.Get("dev") == "1"
	info, _, warn, err := u.D.InstallConnectorDev(r.Context(), src, consent, local && dev)
	if errors.Is(err, store.ErrConsent) {
		again := url.Values{"install": {src}}
		if local {
			again.Set("local", "1")
			if dev {
				again.Set("dev", "1")
			}
		}
		return redirect("/connectors?"+again.Encode(), "What the connector may access has changed — confirm again.")
	}
	if err != nil {
		return redirectErr("/connectors", err)
	}
	profs := r.PostForm["profiles"]
	if len(profs) > 0 {
		if err := u.edit("connector "+info.Connector+" for profiles", func(c *config.Config) error {
			for _, p := range profs {
				if err := c.SetProfileConnector(p, info.Connector, true); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			warn = strings.TrimSpace(warn + " " + err.Error())
		}
	}
	msg := M("%s %s is installed. Set it up below.", u.connTitle(u.lang(r), info.Connector), info.Version)
	if warn != "" {
		msg = M("%s %s is installed. Set it up below. Note: %s", u.connTitle(u.lang(r), info.Connector), info.Version, warn)
	}
	return redirect("/connectors/"+url.PathEscape(info.Connector), msg)
}

// expandHome — «~/…» в пути на этом компьютере.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func (u *UI) refreshIndex(w http.ResponseWriter, r *http.Request) map[string]any {
	if _, ierr := u.storeItems(r.Context(), u.lang(r), true); ierr != "" {
		return redirectErr("/connectors", errors.New(ierr))
	}
	return redirect("/connectors", "The catalog is updated")
}

func (u *UI) connectorPage(r *http.Request) (string, map[string]any, error) {
	name := r.PathValue("name")
	c, ok := u.D.Registry.Get(name)
	if !ok {
		items, ierr := u.storeItems(r.Context(), u.lang(r), false)
		return "connectors", map[string]any{"Items": items, "IndexError": ierr}, M("%s is not installed", name)
	}
	l := u.lang(r)
	m := localized(c.Manifest())
	cfg := u.D.Config.Current()
	fields := settingFields(m, cfg.Connectors[name])
	for i, f := range fields {
		if f.Kind == fieldSecret && f.Alias != "" {
			_, err := u.D.Vault.Get(f.Alias)
			fields[i].HasSecret = err == nil
		}
	}
	profiles := slices.Sorted(maps.Keys(cfg.Profiles))
	in := u.installed()[name]
	access := accessProfiles(cfg, name)
	prof := connectorProfile(cfg, access, r.URL.Query().Get("profile"))
	acts, calls := u.actionViews(l, name, m, cfg, prof)
	var main, adv []SettingField
	for _, f := range fields {
		if f.Advanced {
			adv = append(adv, f)
		} else {
			main = append(main, f)
		}
	}
	data := map[string]any{"Name": name, "Title": m.TitleIn(l), "Desc": m.DescriptionIn(l), "Manifest": m, "Fields": main, "AdvFields": adv,
		"Profiles": profiles, "Config": cfg, "Info": in.Info, "Builtin": in.Name == "", "Actions": acts, "Calls": calls, "Access": access,
		"Profile": prof, "Grants": grantsFor(cfg, name), "Groups": u.groups(name, cfg.Profiles[prof]), "ProfileAgents": profileAgents(cfg, prof),
		"WholeHome": name == "files" && filesWholeHome(cfg)}
	if name == "email" {
		data["Providers"] = mailProviders
	}
	return "connector", data, nil
}

func (u *UI) saveConnectorSettings(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	back := "/connectors/" + url.PathEscape(name)
	c, ok := u.D.Registry.Get(name)
	if !ok {
		return redirectErr("/connectors", M("%s is not installed", name))
	}
	f := r.PostForm
	cur := map[string]any{}
	maps.Copy(cur, u.D.Config.Current().Connectors[name])
	// Почтовый сервис: серверы по умолчанию.
	if p := f.Get("provider"); p != "" {
		for _, mp := range mailProviders {
			if mp.ID == p {
				if f.Get("imap.host") == "" {
					f.Set("imap.host", mp.IMAP)
				}
				if f.Get("smtp.host") == "" {
					f.Set("smtp.host", mp.SMTP)
				}
				if f.Get("imap.username") == "" {
					f.Set("imap.username", f.Get("address"))
				}
			}
		}
	}
	for _, fl := range settingFields(c.Manifest(), cur) {
		raw := f.Get(fl.Key)
		if fl.Kind == fieldSecret {
			if strings.TrimSpace(raw) == "" {
				continue // пусто — оставить как есть
			}
			alias := fl.Alias
			if alias == "" {
				alias = "@" + name + "." + fl.Key
			}
			if err := u.D.Vault.Set(alias, raw); err != nil {
				return redirectErr(back, err)
			}
			setPath(cur, fl.Key, alias)
			continue
		}
		v, err := fieldValue(fl, raw)
		if err != nil {
			return redirectErr(back, err)
		}
		setPath(cur, fl.Key, v)
	}
	grantsOn := f["profiles"]
	if err := u.edit("connector "+name+" settings", func(c *config.Config) error {
		c.SetConnectorSettings(name, cur)
		// Одна форма: настройки и доступ агентов сохраняются вместе.
		if f.Has("grants") {
			for _, g := range grantsFor(c, name) {
				if err := c.SetProfileConnector(g.Profile, name, slices.Contains(grantsOn, g.Profile)); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Saved")
}

func (u *UI) removeConnector(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	// Сначала убираем коннектор из профилей и правил: иначе политика ссылается на
	// действия, которых нет.
	_ = u.edit("remove connector "+name, func(c *config.Config) error {
		for pn, p := range c.Profiles {
			_ = c.SetProfileConnector(pn, name, false)
			p = c.Profiles[pn]
			p.Rules = slices.DeleteFunc(p.Rules, func(x config.Rule) bool { return strings.HasPrefix(x.Action, name+".") })
			p.Limits = slices.DeleteFunc(p.Limits, func(x config.Limit) bool { return strings.HasPrefix(x.Action, name+".") })
			c.Profiles[pn] = p
		}
		return nil
	})
	warn, err := u.D.RemoveConnector(name)
	if err != nil {
		return redirectErr("/connectors/"+url.PathEscape(name), err)
	}
	if warn != "" {
		return redirect("/connectors", M("%s is removed. %s", name, warn))
	}
	return redirect("/connectors", M("%s is removed.", name))
}

// grant — кто получит доступ к коннектору: профиль и его агенты.
type grant struct {
	Profile string
	Agents  []string
	Checked bool // по умолчанию: профили с агентами — да, без агентов — нет
}

// grants — профили для выбора «кому дать доступ» (при установке).
func grants(cfg *config.Config) []grant {
	var out []grant
	for p := range cfg.Profiles {
		ag := profileAgents(cfg, p)
		out = append(out, grant{Profile: p, Agents: ag, Checked: len(ag) > 0})
	}
	sort.Slice(out, func(i, j int) bool {
		if (len(out[i].Agents) > 0) != (len(out[j].Agents) > 0) {
			return len(out[i].Agents) > 0
		}
		return out[i].Profile < out[j].Profile
	})
	return out
}

// grantsFor — то же для установленного коннектора: отмечены профили с доступом.
func grantsFor(cfg *config.Config, conn string) []grant {
	out := grants(cfg)
	for i := range out {
		out[i].Checked = cfg.Profiles[out[i].Profile].HasConnector(conn)
	}
	return out
}

// profileAgents — агенты профиля.
func profileAgents(cfg *config.Config, prof string) []string {
	var out []string
	for a, ag := range cfg.Agents {
		if ag.Profile == prof {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// capGroup — возможности коннектора человеческим языком, каждая один раз
// (сеть — одной строкой, пароли — одной), с сырыми строками для «Технически».
type capGroup struct {
	Text string
	Raw  []string
}

func groupCapabilities(l string, caps []string) []capGroup {
	var out []capGroup
	idx := map[string]int{}
	var hosts, secrets []string
	for _, c := range caps {
		key := c
		var text string
		switch {
		case strings.HasPrefix(c, "network "):
			key = "network"
			h := settingRe.ReplaceAllString(strings.TrimPrefix(c, "network "), "$1")
			if i := strings.Index(h, "://"); i >= 0 {
				h = h[i+3:]
			}
			if !slices.Contains(hosts, h) {
				hosts = append(hosts, h)
			}
		case strings.HasPrefix(c, "secret "):
			key = "secret"
			alias, _, _ := strings.Cut(strings.TrimPrefix(c, "secret "), " for ")
			alias = settingRe.ReplaceAllString(alias, "$1")
			if !slices.Contains(secrets, alias) {
				secrets = append(secrets, alias)
			}
		default:
			text = humanCapability(l, c)
		}
		i, ok := idx[key]
		if !ok {
			idx[key] = len(out)
			i = len(out)
			out = append(out, capGroup{Text: text})
		}
		out[i].Raw = append(out[i].Raw, c)
	}
	if i, ok := idx["network"]; ok {
		out[i].Text = M("network: connects only to %s from the settings", strings.Join(hosts, ", ")).In(l)
	}
	if i, ok := idx["secret"]; ok {
		out[i].Text = M("passwords %s — gate4.ai enters them only for the servers from the settings; the connector never sees them", strings.Join(secrets, ", ")).In(l)
	}
	return out
}
