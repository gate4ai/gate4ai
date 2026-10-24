// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/access"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/state"
)

// AccessEntry — строка списка доступа в API: домен или папка и режим ("" — убрать).
type AccessEntry struct {
	Domain string `json:"domain,omitempty"`
	Path   string `json:"path,omitempty"`
	Access string `json:"access"`
}

// accessView — списки доступа для API.
func accessView(c *config.Config) map[string]any {
	return map[string]any{"sites": orEmptySites(c.Access.Sites), "folders": orEmptyFolders(c.Access.Folders)}
}

func orEmptySites(s []access.Site) []access.Site {
	if s == nil {
		return []access.Site{}
	}
	return s
}

func orEmptyFolders(f []access.Folder) []access.Folder {
	if f == nil {
		return []access.Folder{}
	}
	return f
}

// accessRoutes — списки доступа Браузера и Файлов: владелец меняет строку одной
// командой (gate4ai access), без правки YAML.
func (d *Daemon) accessRoutes(mux *http.ServeMux, own func(func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc) {
	mux.HandleFunc("GET /v1/access", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		WriteJSON(w, 200, accessView(d.Config.Current()))
	}))
	mux.HandleFunc("POST /v1/access", own(func(w http.ResponseWriter, r *http.Request, p auth.Principal) {
		var req struct {
			Sites   []AccessEntry `json:"sites"`
			Folders []AccessEntry `json:"folders"`
		}
		if err := readJSON(r, &req); err != nil || len(req.Sites)+len(req.Folders) == 0 {
			WriteError(w, 400, "bad_request", `set sites or folders: {"sites": [{"domain": "app.example.org", "access": "read"}]} (access "" removes the entry)`)
			return
		}
		var parts []string
		v, err := d.Config.Edit(p, "", func(c *config.Config) error {
			for _, s := range req.Sites {
				if err := c.SetSiteAccess(s.Domain, s.Access); err != nil {
					return err
				}
				parts = append(parts, "site "+s.Domain+": "+orWord(s.Access, "removed"))
			}
			for _, f := range req.Folders {
				if err := c.SetFolderAccess(f.Path, f.Access); err != nil {
					return err
				}
				parts = append(parts, "folder "+f.Path+": "+orWord(f.Access, "removed"))
			}
			return nil
		})
		if err != nil {
			WriteError(w, 400, "policy", err.Error())
			return
		}
		_ = parts
		out := accessView(d.Config.Current())
		out["status"], out["version"] = "ok", v.N
		WriteJSON(w, 200, out)
	}))
}

func orWord(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// AddAccess — из запроса подтверждения: добавить его сайт или папку в список
// доступа как разрешённые (allow — режим по действию: чтение → read, запись →
// allow / append) или запрещённые (deny). Возвращает, что записано.
func (d *Daemon) AddAccess(a *state.Approval, allow bool) (string, error) {
	kind, value, ok := strings.Cut(a.Decision.Scope, ":")
	if !ok || value == "" {
		return "", errors.New("this request has no site or folder to add")
	}
	mode := access.Deny
	var comment string
	switch kind {
	case "site":
		if allow {
			mode = access.Read
			if manifest.ClassRank(a.Class) > manifest.ClassRank(policy.ClassRead) {
				mode = access.Allow
			}
		}
		comment = fmt.Sprintf("Browser: site %s — %s (from approval %s)", value, mode, a.ID)
		_, err := d.Config.Edit(auth.OwnerVia("approval"), comment, func(c *config.Config) error { return c.SetSiteAccess(value, mode) })
		return fmt.Sprintf("site %s: %s", value, mode), err
	case "folder":
		if allow {
			switch access.OpOf(a.Action) {
			case access.OpWrite:
				mode = access.Allow
			case access.OpAppend:
				mode = access.Append
			default:
				mode = access.Read
			}
		}
		comment = fmt.Sprintf("Files: folder %s — %s (from approval %s)", value, mode, a.ID)
		_, err := d.Config.Edit(auth.OwnerVia("approval"), comment, func(c *config.Config) error { return c.SetFolderAccess(value, mode) })
		return fmt.Sprintf("folder %s: %s", value, mode), err
	}
	return "", errors.New("unknown scope " + kind)
}

// AddAccessByID — AddAccess по id запроса (каналы подтверждений: Telegram).
func (d *Daemon) AddAccessByID(_ context.Context, id string, allow bool) (string, error) {
	a, err := d.State.GetApproval(id)
	if err != nil {
		return "", err
	}
	return d.AddAccess(a, allow)
}

// ensureConnectorAccess записывает умолчания сайтов и папок коннектора из его
// манифеста в настройки (connectors.<имя>.sites / folders), если владелец их
// ещё не задавал: дальше действует и правится только этот список.
func (d *Daemon) ensureConnectorAccess(name string) error {
	cli, ok := d.Registry.Get(name)
	if !ok {
		return nil
	}
	m := cli.Manifest()
	if len(m.Sites) == 0 && len(m.Folders) == 0 {
		return nil
	}
	_, err := d.Config.Edit(auth.OwnerVia("daemon"), "connector "+name+": sites and folders from its manifest", func(c *config.Config) error {
		settings := c.Connectors[name]
		changed := false
		if len(m.Sites) > 0 && c.ConnectorSites(name) == nil {
			if err := c.SetConnectorSite(name, m.ResolveSites(settings), "", ""); err != nil {
				return err
			}
			changed = true
		}
		if len(m.Folders) > 0 && c.ConnectorFolders(name) == nil {
			if err := c.SetConnectorFolder(name, m.ResolveFolders(settings), "", ""); err != nil {
				return err
			}
			changed = true
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}

var _ = pipeline.RuleAccess
