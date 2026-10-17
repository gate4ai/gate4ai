// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// defaultsFile — коннекторы по умолчанию, ещё не поставленные из магазина.
// Записывается на свежей установке; удаляется, когда все поставлены.
const defaultsFile = "default-connectors.json"

// DefaultsRetry — первая пауза перед повтором установки коннекторов по
// умолчанию (магазин недоступен); дальше вдвое больше, до DefaultsRetryMax.
var DefaultsRetry, DefaultsRetryMax = 30 * time.Second, 30 * time.Minute

func (d *Daemon) defaultsPath() string { return filepath.Join(d.Home, defaultsFile) }

// pendingDefaults — коннекторы по умолчанию, которые ещё надо поставить.
func (d *Daemon) pendingDefaults() []string {
	var names []string
	if b, err := os.ReadFile(d.defaultsPath()); err == nil {
		_ = json.Unmarshal(b, &names)
	}
	return names
}

func (d *Daemon) savePendingDefaults(names []string) error {
	if len(names) == 0 {
		if err := os.Remove(d.defaultsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, _ := json.Marshal(names)
	return os.WriteFile(d.defaultsPath(), b, 0o600)
}

// InstallDefaults ставит из магазина коннекторы по умолчанию свежей установки
// (Options.DefaultConnectors) с согласием на их возможности без вопроса владельцу —
// только пакеты, проверенные gate4.ai. Пока магазин недоступен, повторяет с
// растущей паузой (и при следующем запуске daemon); возвращается, когда всё
// поставлено или ctx отменён. Уже установленный владельцем коннектор не трогает.
func (d *Daemon) InstallDefaults(ctx context.Context) {
	wait := DefaultsRetry
	for {
		pending := d.pendingDefaults()
		if len(pending) == 0 {
			return
		}
		var left []string
		for _, name := range pending {
			if err := d.installDefault(ctx, name); err != nil {
				d.Log.Warn("default connector not installed yet, will retry", "connector", name, "err", err)
				left = append(left, name)
			}
		}
		if err := d.savePendingDefaults(left); err != nil {
			d.Log.Error("default connectors", "err", err)
			return
		}
		if len(left) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, DefaultsRetryMax)
	}
}

func (d *Daemon) installDefault(ctx context.Context, name string) error {
	opts := d.StoreOptions()
	if _, err := opts.Info(name); err == nil {
		return nil
	}
	plan, err := opts.Prepare(ctx, name, false)
	if err != nil {
		return err
	}
	if plan.Trust != store.TrustVerified {
		return fmt.Errorf("trust %s: only connectors verified by gate4.ai are installed without the owner", plan.Trust)
	}
	d.installMu.Lock()
	defer d.installMu.Unlock()
	// Владелец мог поставить коннектор сам, пока шла загрузка: его установка важнее.
	if _, err := opts.Info(name); err == nil {
		return nil
	}
	info, err := opts.Install(plan, plan.Capabilities)
	if err != nil {
		return err
	}
	if err := d.reloadConnector(info.Connector, false); err != nil {
		d.Log.Warn("default connector installed, policy not recompiled", "connector", name, "err", err)
	}
	d.Log.Info("default connector installed", "connector", info.Connector, "version", info.Version, "trust", info.Trust)
	return nil
}

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
