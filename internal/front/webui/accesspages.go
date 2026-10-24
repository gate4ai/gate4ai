// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
)

// accessRow — строка списка доступа на странице: домен или папка, режим,
// допустимые режимы и предупреждение (строка коннектора, закрытая в Браузере).
type accessRow struct {
	Value, Access string
	Modes         []string
	Warn          string
}

// pageOf — страница компонента: встроенные Браузер и Файлы — свои, остальные —
// страница коннектора.
func pageOf(name string) string {
	switch name {
	case "browser":
		return "/access/browser"
	case "files":
		return "/access/files"
	}
	return "/connectors/" + url.PathEscape(name)
}

func siteRows(sites []access.Site, modes []string) []accessRow {
	out := make([]accessRow, 0, len(sites))
	for _, s := range sites {
		out = append(out, accessRow{Value: s.Domain, Access: s.Access, Modes: modes})
	}
	return out
}

func folderRows(folders []access.Folder, modes []string) []accessRow {
	out := make([]accessRow, 0, len(folders))
	for _, f := range folders {
		out = append(out, accessRow{Value: f.Path, Access: f.Access, Modes: modes})
	}
	return out
}

// builtinPage — страница Браузера или Файлов: список доступа сверху, ниже —
// настройки и решения по действиям, как у коннектора.
func (u *UI) builtinPage(name string) func(r *http.Request) (string, map[string]any, error) {
	return func(r *http.Request) (string, map[string]any, error) {
		tpl, data, err := u.connectorData(r, name)
		if err != nil {
			return tpl, data, err
		}
		l := u.lang(r)
		cfg := u.D.Config.Current()
		data["Nav"] = name
		switch name {
		case "browser":
			data["Rows"] = siteRows(cfg.Access.Sites, access.SiteModes)
			data["Kind"] = "sites"
		case "files":
			data["Rows"] = folderRows(cfg.Access.Folders, access.FolderModes)
			data["Kind"] = "folders"
		}
		data["ModeHelp"] = modeHelp(l, name == "files")
		return "builtin", data, nil
	}
}

// modeHelp — что значит каждый режим (для подсказки под таблицей).
func modeHelp(l string, folder bool) []string {
	rows := []Msg{
		M("allow — everything without asking"),
	}
	if folder {
		rows = append(rows, M("append — reading and adding to files without asking, overwriting asks you"))
	}
	rows = append(rows,
		M("read — reading without asking, changes ask you"),
		M("connector — only through a connector from the store, not to agents directly"),
		M("ask — every action asks you"),
		M("deny — closed for everyone, including connectors"))
	out := make([]string, len(rows))
	for i, m := range rows {
		out[i] = m.In(l)
	}
	return out
}

// connectorRows — строки коннектора с предупреждением, если Браузер или Файлы
// закрывают то, что коннектор разрешает.
func (u *UI) connectorRows(l string, cfg *config.Config, name string, m manifest.Manifest) (sites, folders []accessRow) {
	settings := cfg.Connectors[name]
	cs := cfg.ConnectorSites(name)
	if cs == nil {
		cs = m.ResolveSites(settings)
	}
	cf := cfg.ConnectorFolders(name)
	if cf == nil {
		cf = m.ResolveFolders(settings)
	}
	sites = siteRows(cs, access.ConnectorSiteModes)
	for i, row := range sites {
		if row.Access == access.Deny || strings.Contains(row.Value, "*") {
			continue
		}
		g, ok := access.MatchSite(cfg.Access.Sites, "https://"+row.Value+"/")
		switch {
		case !ok || g.Access == access.Deny:
			sites[i].Warn = M("closed in Browser: the connector cannot open it").In(l)
		case g.Access == access.Ask:
			sites[i].Warn = M("Browser asks about this site: each call waits for you").In(l)
		}
	}
	folders = folderRows(cf, access.ConnectorFolderModes)
	for i, row := range folders {
		if row.Access == access.Deny || strings.ContainsAny(row.Value, "*?") {
			continue
		}
		if g, ok := access.MatchFolder(cfg.Access.Folders, row.Value, u.D.Pipeline.Home); !ok || g.Access == access.Deny {
			folders[i].Warn = M("closed in Files: the connector cannot open it").In(l)
		}
	}
	return sites, folders
}

// setAccess — POST /access/sites и /access/folders: строка списка Браузера или
// Файлов (op set — задать режим, remove — убрать).
func (u *UI) setAccess(kind string) func(w http.ResponseWriter, r *http.Request) map[string]any {
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		f := r.PostForm
		back := or(f.Get("back"), map[string]string{"sites": "/access/browser", "folders": "/access/files"}[kind])
		value, mode := strings.TrimSpace(f.Get("value")), f.Get("access")
		if f.Get("op") == "remove" {
			mode = ""
		}
		if value == "" {
			return redirectErr(back, M("Enter a site or a folder"))
		}
		if kind == "sites" {
			value = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://"), "/"))
		}
		err := u.edit("access: "+kind+" "+value+" "+or(mode, "removed"), func(c *config.Config) error {
			if kind == "sites" {
				return c.SetSiteAccess(value, mode)
			}
			return c.SetFolderAccess(value, mode)
		})
		if err != nil {
			return redirectErr(back, err)
		}
		if mode == "" {
			return redirect(back, M("%s removed from the list.", value))
		}
		return redirect(back, M("%s: %s.", value, Word(mode)))
	}
}

// setConnectorAccess — POST /connectors/{name}/sites и /folders: строка коннектора.
func (u *UI) setConnectorAccess(kind string) func(w http.ResponseWriter, r *http.Request) map[string]any {
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		name := r.PathValue("name")
		back := "/connectors/" + url.PathEscape(name) + "#access"
		c, ok := u.D.Registry.Get(name)
		if !ok {
			return redirectErr("/connectors", M("%s is not installed", name))
		}
		f := r.PostForm
		value, mode := strings.TrimSpace(f.Get("value")), f.Get("access")
		if f.Get("op") == "remove" {
			mode = ""
		}
		if value == "" {
			return redirectErr(back, M("Enter a site or a folder"))
		}
		m := c.Manifest()
		err := u.edit("connector "+name+": "+kind+" "+value+" "+or(mode, "removed"), func(cfg *config.Config) error {
			if kind == "sites" {
				base := cfg.ConnectorSites(name)
				if base == nil {
					base = m.ResolveSites(cfg.Connectors[name])
				}
				return cfg.SetConnectorSite(name, base, strings.ToLower(value), mode)
			}
			base := cfg.ConnectorFolders(name)
			if base == nil {
				base = m.ResolveFolders(cfg.Connectors[name])
			}
			return cfg.SetConnectorFolder(name, base, value, mode)
		})
		if err != nil {
			return redirectErr(back, err)
		}
		if mode == "" {
			return redirect(back, M("%s removed from the list.", value))
		}
		return redirect(back, M("%s: %s.", value, Word(mode)))
	}
}

// builtinRedirect — /connectors/browser и /connectors/files ведут на свои страницы.
func builtinRedirect(name string) bool { return slices.Contains([]string{"browser", "files"}, name) }
