// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"bytes"
	"slices"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// DefaultProfile — профиль, в который попадает каждый установленный коннектор.
const DefaultProfile = "default"

// askInDefault — после установки или обновления коннектора: он в профиле default,
// и на каждое его действие без своего правила там — правило ask (в том числе на
// чтение, у которого по умолчанию allow). Правила владельца на действие не
// трогаются; профиля default нет (владелец его убрал) — ничего не меняется.
// Начальный комментарий политики сохраняется.
func (d *Daemon) askInDefault(name string) error {
	cli, ok := d.Registry.Get(name)
	if !ok {
		return nil
	}
	raw, _ := d.Config.CurrentRaw()
	c, err := config.Decode(raw)
	if err != nil {
		return err
	}
	p, ok := c.Profiles[DefaultProfile]
	if !ok {
		return nil
	}
	changed := !p.HasConnector(name)
	if err := c.SetProfileConnector(DefaultProfile, name, true); err != nil {
		return err
	}
	for _, a := range cli.Manifest().Actions {
		if a.ServiceOnly {
			continue // только для других коннекторов: их вызов покрыт решением по вызывающему
		}
		action := name + "." + a.Name
		p = c.Profiles[DefaultProfile]
		if slices.ContainsFunc(p.Rules, func(r config.Rule) bool { return r.Action == action }) {
			continue
		}
		if err := c.PutRule(DefaultProfile, config.Rule{ID: p.UniqueRuleID(name + "-" + a.Name + "-ask"), Action: action, Effect: config.EffectAsk}); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	out, err := config.Marshal(c)
	if err != nil {
		return err
	}
	_, err = d.Config.Put(auth.OwnerVia("daemon"), append(leadingComment(raw), out...), "connector "+name+": actions ask in profile "+DefaultProfile)
	return err
}

// leadingComment — строки-комментарии в начале YAML (Marshal их не сохраняет).
func leadingComment(raw []byte) []byte {
	var out []byte
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("#")) {
			break
		}
		out = append(out, line...)
	}
	return out
}
