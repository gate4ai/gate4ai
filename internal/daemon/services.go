// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"reflect"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
)

// ServiceCall — service_call коннектора caller (wasm.Router): вложенный вызов
// через Action Pipeline.
func (d *Daemon) ServiceCall(ctx context.Context, caller string, rule manifest.ServiceRule, r abi.ServiceCallRequest) (abi.ExecResult, error) {
	if d.Pipeline == nil {
		return abi.ExecResult{}, abi.Errorf(abi.CodeNotSupported, "daemon is starting")
	}
	return d.Pipeline.ExecuteNested(ctx, caller, rule, r)
}

// callEnder — коннектор с состоянием на время корневого действия (нативный процесс).
type callEnder interface{ EndCall(root string) }

// endCall — корневое действие закончилось: поставщики освобождают его состояние.
func (d *Daemon) endCall(root string) {
	d.Registry.Each(func(c connectors.Client) {
		if e, ok := c.(callEnder); ok {
			e.EndCall(root)
		}
	})
}

// migrateBrowserSection переносит раздел browser конфигурации (настройки
// браузерной службы ядра до v0.6) в настройки коннектора connectors.browser:
// браузер теперь нативный пакет, и это его настройки. Значения владельца в
// connectors.browser важнее. Новая версия конфигурации — с откатом.
func (d *Daemon) migrateBrowserSection() {
	raw, _ := d.Config.CurrentRaw()
	c, err := config.Decode(raw)
	if err != nil || reflect.ValueOf(c.Browser).IsZero() {
		return
	}
	b := c.Browser
	set := map[string]any{}
	add := func(k string, v any) {
		if !reflect.ValueOf(v).IsZero() {
			set[k] = v
		}
	}
	add("engine", b.Engine)
	add("cdp_url", b.CDPURL)
	add("chrome_path", b.ChromePath)
	add("profile_dir", b.ProfileDir)
	add("model", b.Model)
	add("model_key", b.ModelKey)
	add("max_text_chars", b.MaxTextChars)
	if c.Connectors == nil {
		c.Connectors = map[string]map[string]any{}
	}
	cur := c.Connectors["browser"]
	if cur == nil {
		cur = map[string]any{}
	}
	for k, v := range set {
		if _, ok := cur[k]; !ok {
			cur[k] = v
		}
	}
	c.Connectors["browser"] = cur
	c.Browser = config.Browser{}
	out, err := config.Marshal(c)
	if err != nil {
		d.Log.Error("browser settings not moved", "err", err)
		return
	}
	if _, err := d.Config.Put(auth.OwnerVia("daemon"), append(leadingComment(raw), out...), "browser settings moved to connectors.browser"); err != nil {
		d.Log.Error("browser settings not moved", "err", err)
		return
	}
	d.Log.Info("browser settings moved to connectors.browser")
}
