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
)

// legacyPackages — коннекторы, которые прежние версии ставили из магазина:
// браузер теперь встроен в gate4ai, почты больше нет. Их каталоги в
// ConnectorsDir удаляются при старте (removeLegacyPackages), ссылки на почту
// — из политики (migrateBuiltins).
var legacyPackages = []string{"browser", "email"}

// removedConnectors — коннекторы, которых больше нет: правила, настройки и
// упоминания в профилях убираются из политики новой версией (откат — в версиях).
var removedConnectors = []string{"email"}

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
// профили, правила на их действия и настройки. Пустая правка версии не создаёт.
func (d *Daemon) migrateBuiltins() {
	changed := false
	_, err := d.Config.Edit(auth.OwnerVia("daemon"), "connectors removed from gate4ai: "+strings.Join(removedConnectors, ", "), func(c *config.Config) error {
		for _, name := range removedConnectors {
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
		d.Log.Info("policy migrated: removed connectors dropped", "connectors", removedConnectors)
	}
}

var errNoChange = errors.New("no change")
