// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/sdk-go/access"
)

// legacyPackages — коннекторы, которые прежние версии ставили из магазина:
// браузер теперь встроен в gate4ai, почты больше нет. Их каталоги в
// ConnectorsDir удаляются при старте (removeLegacyPackages), ссылки на почту
// — из политики (migrateBuiltins).
var legacyPackages = []string{"browser", "email"}

// RemovedConnectors — коннекторы, которых больше нет: правила, настройки и
// упоминания в профилях убираются из политики новой версией (откат — в версиях);
// в магазине Web UI их не показывает, даже если они ещё есть в кэше индекса.
var RemovedConnectors = []string{"email"}

// removeLegacyPackages удаляет установленные пакеты browser и email до загрузки
// магазина: встроенный браузер важнее пакета с тем же именем.
func (d *Daemon) removeLegacyPackages() {
	for _, name := range legacyPackages {
		dir := filepath.Join(d.ConnectorsDir, name)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			d.Log.Warn("legacy connector package not removed", "connector", name, "err", err)
			continue
		}
		d.Log.Info("legacy connector package removed: it is built into gate4ai or no longer supported", "connector", name)
	}
}

// migrateBuiltins убирает из политики коннекторы, которых больше нет (почта):
// профили, правила на их действия и настройки, — и переносит прежние настройки
// сайтов и папок (connectors.browser.allowed_domains / blocked / mode,
// connectors.files.roots) в списки доступа: разрешённые прежде сайты и папки —
// read (чтение без вопроса, изменения — с подтверждением, как было по
// умолчанию класса), закрытые — deny. Пустая правка версии не создаёт.
func (d *Daemon) migrateBuiltins() {
	changed := false
	_, err := d.Config.Edit(auth.OwnerVia("daemon"), "gate4ai update: connectors removed ("+strings.Join(RemovedConnectors, ", ")+"), sites and folders moved to the access lists", func(c *config.Config) error {
		if migrateAccess(c) {
			changed = true
		}
		for _, name := range RemovedConnectors {
			if _, ok := c.Connectors[name]; ok {
				delete(c.Connectors, name)
				changed = true
			}
			for pn, p := range c.Profiles {
				if i := slices.Index(p.Connectors, name); i >= 0 {
					p.Connectors = slices.Delete(slices.Clone(p.Connectors), i, i+1)
					changed = true
				}
				rules := slices.DeleteFunc(slices.Clone(p.Rules), func(r config.Rule) bool { return r.Action == name+".*" || strings.HasPrefix(r.Action, name+".") })
				if len(rules) != len(p.Rules) {
					p.Rules = rules
					changed = true
				}
				c.Profiles[pn] = p
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		d.Log.Warn("policy not migrated", "err", err)
	}
	if changed && err == nil {
		d.Log.Info("policy migrated: removed connectors dropped", "connectors", RemovedConnectors)
	}
}

var errNoChange = errors.New("no change")

// migrateAccess переносит прежние настройки сайтов и папок в списки доступа.
func migrateAccess(c *config.Config) bool {
	changed := false
	if b := c.Connectors["browser"]; b != nil {
		mode, _ := b["mode"].(string)
		for _, dmn := range settingStrings(b["allowed_domains"]) {
			c.Access.Sites = access.SetSite(c.Access.Sites, dmn, access.Allow)
			changed = true
		}
		for _, dmn := range settingStrings(b["blocked"]) {
			c.Access.Sites = access.SetSite(c.Access.Sites, dmn, access.Deny)
			changed = true
		}
		if mode == "blocklist" {
			c.Access.Sites = access.SetSite(c.Access.Sites, "*", access.Allow)
			changed = true
		}
		for _, k := range []string{"allowed_domains", "blocked", "mode"} {
			if _, ok := b[k]; ok {
				delete(b, k)
				changed = true
			}
		}
		if len(b) == 0 {
			delete(c.Connectors, "browser")
		}
	}
	if f := c.Connectors["files"]; f != nil {
		for _, p := range settingStrings(f["roots"]) {
			c.Access.Folders = access.SetFolder(c.Access.Folders, p, access.Read)
			changed = true
		}
		if _, ok := f["roots"]; ok {
			delete(f, "roots")
			changed = true
		}
		if len(f) == 0 {
			delete(c.Connectors, "files")
		}
	}
	return changed
}

func settingStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}
