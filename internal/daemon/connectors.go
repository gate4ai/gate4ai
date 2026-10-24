// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors/builtin/files"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// StoreOptions — магазин коннекторов по текущей конфигурации.
func (d *Daemon) StoreOptions() store.Options {
	s := d.Config.Current().Store
	return store.Options{Dir: d.ConnectorsDir, Registry: s.Registry, RegistryKeys: s.RegistryKeys, TrustedKeys: s.TrustedKeys,
		Builtin: BuiltinConnectors, Log: d.Log}
}

// stopNative останавливает процесс нативного коннектора до замены или удаления
// его файлов (на Windows файл запущенной программы не заменить); коннектор
// загружается заново в reloadConnector.
func (d *Daemon) stopNative(name string) {
	if c, ok := d.Registry.Get(name); ok && c.Manifest().Runtime == manifest.RuntimeNative {
		_ = c.Close()
	}
}

// restartNative — установка не удалась: прежняя версия остановленного
// нативного коннектора загружается заново.
func (d *Daemon) restartNative(name string) {
	if c, ok := d.Registry.Get(name); ok && c.Manifest().Runtime == manifest.RuntimeNative {
		if err := d.reloadConnector(name, false); err != nil {
			d.Log.Warn("connector not reloaded", "connector", name, "err", err)
		}
	}
}

// BuiltinConnectors — коннекторы ядра: из магазина не ставятся.
var BuiltinConnectors = []string{files.Name, SelfConnector, "egress"}

// dropRevoked выгружает отозванные версии по кэшу индекса registry.
func (d *Daemon) dropRevoked() {
	list, err := store.List(d.ConnectorsDir)
	if err != nil {
		return
	}
	for _, it := range list {
		if err := d.StoreOptions().Check(it.Name); errors.Is(err, store.ErrRevoked) {
			d.Log.Error("connector disabled", "connector", it.Name, "err", err)
			d.Registry.Remove(it.Name)
		}
	}
}

// reloadConnector — после установки, обновления или удаления: реестр и политика;
// установленный коннектор попадает в профиль default с правилами ask (askInDefault).
func (d *Daemon) reloadConnector(name string, removed bool) error {
	d.Registry.Remove(name)
	if !removed {
		c, err := d.StoreOptions().Load(name, d.Wasm)
		if err != nil {
			return err
		}
		d.Registry.Add(c)
		if err := d.askInDefault(name); err != nil {
			d.Log.Warn("connector not added to profile "+DefaultProfile, "connector", name, "err", err)
		}
	}
	// Политика — из текущей версии по новому каталогу, тем же путём, что и
	// смены версий (по порядку с ними). Если она ссылается на аргументы
	// действий удалённого коннектора, она компилируется без каталога, а
	// предупреждение видно владельцу.
	d.Config.Reapply()
	if w := d.PolicyWarning(); w != "" {
		return errors.New(w)
	}
	return nil
}

// InstallConnectorDev ставит коннектор (файл .g4c или имя из registry) с согласием
// на возможности consent; при ErrConsent возвращает план для показа владельцу.
// dev — своя сборка без доверенной подписи (папка с исходниками, файл или адрес
// .g4c), как `connectors install --dev`.
func (d *Daemon) InstallConnectorDev(ctx context.Context, source string, consent []string, dev bool) (store.Info, *store.Plan, string, error) {
	opts := d.StoreOptions()
	plan, err := opts.Prepare(ctx, source, dev)
	if err != nil {
		return store.Info{}, nil, "", err
	}
	info, warn, err := d.installPlan(opts, plan, consent)
	return info, plan, warn, err
}

// installPlan ставит подготовленный план под installMu (без согласия —
// ErrConsent); warn — политика не сошлась с новым каталогом (см. reloadConnector).
func (d *Daemon) installPlan(opts store.Options, plan *store.Plan, consent []string) (info store.Info, warn string, err error) {
	d.installMu.Lock()
	defer d.installMu.Unlock()
	if !plan.ConsentOK(consent) {
		return store.Info{}, "", store.ErrConsent
	}
	d.stopNative(plan.Connector)
	if info, err = opts.Install(plan, consent); err != nil {
		d.restartNative(plan.Connector)
		return store.Info{}, "", err
	}
	if err := d.reloadConnector(info.Connector, false); err != nil {
		warn = err.Error()
		d.Log.Warn("connector reloaded with a warning", "connector", info.Connector, "err", err)
	}
	d.Log.Info("connector installed", "connector", info.Connector, "version", info.Version, "trust", info.Trust)
	return info, warn, nil
}

// ReinstallConnector ставит установленный коннектор заново из того же источника
// (Info.ReinstallSource) с прежним согласием и заменяет установленную копию;
// настройки, учётки и правила в конфигурации и данные коннектора не трогаются.
// Изменились возможности — ErrConsent и план для показа владельцу.
func (d *Daemon) ReinstallConnector(ctx context.Context, name string) (store.Info, *store.Plan, string, error) {
	opts := d.StoreOptions()
	in, err := opts.Info(name)
	if err != nil {
		return store.Info{}, nil, "", fmt.Errorf("%s is not installed", name)
	}
	source, dev := in.ReinstallSource()
	plan, err := opts.Prepare(ctx, source, dev)
	if err != nil {
		return store.Info{}, nil, "", err
	}
	if plan.Connector != name {
		return store.Info{}, plan, "", fmt.Errorf("%s now holds connector %s, not %s", source, plan.Connector, name)
	}
	info, warn, err := d.installPlan(opts, plan, in.Capabilities)
	return info, plan, warn, err
}

// PrepareConnectorDev — план установки (возможности, доверие) без установки;
// dev — своя сборка (см. InstallConnectorDev).
func (d *Daemon) PrepareConnectorDev(ctx context.Context, source string, dev bool) (*store.Plan, error) {
	return d.StoreOptions().Prepare(ctx, source, dev)
}

// RemoveConnector удаляет коннектор; warning — политика ещё ссылается на него.
// Под installMu: не пересекается с установкой того же коннектора.
func (d *Daemon) RemoveConnector(name string) (warning string, err error) {
	d.installMu.Lock()
	defer d.installMu.Unlock()
	d.stopNative(name)
	if err := d.StoreOptions().Remove(name); err != nil {
		return "", err
	}
	if err := d.reloadConnector(name, true); err != nil {
		return "policy still references this connector: " + err.Error(), nil
	}
	return "", nil
}

type installReq struct {
	Source  string   `json:"source"`
	Dev     bool     `json:"dev"`
	Consent []string `json:"consent"`
	// Origin — "dir:<папка>": пакет source собран CLI из этой папки исходников;
	// записывается источником установки, update пересобирает из неё.
	Origin string `json:"origin,omitempty"`
}

// connectorRoutes — API владельца для магазина коннекторов.
func (d *Daemon) connectorRoutes(mux *http.ServeMux) {
	owner := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !PrincipalFrom(r.Context()).IsOwner() {
				WriteError(w, 403, "owner_only", "only the owner manages connectors")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /v1/connectors", owner(func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(d.ConnectorsDir)
		if err != nil {
			WriteError(w, 500, "store", err.Error())
			return
		}
		out := []map[string]any{}
		for _, name := range BuiltinConnectors {
			c, ok := d.Registry.Get(name)
			if !ok || policy.Implicit[name] || policy.Everyone[name] {
				continue
			}
			m := c.Manifest()
			out = append(out, map[string]any{"connector": name, "version": m.Version, "trust": "builtin", "publisher": "gate4ai",
				"source": "built in", "loaded": true, "capabilities": m.Summary()})
		}
		for _, it := range list {
			c, loaded := d.Registry.Get(it.Name)
			row := map[string]any{"connector": it.Name, "version": it.Manifest.Version, "trust": it.Info.Trust,
				"publisher": it.Info.Publisher, "source": it.Info.Source, "loaded": loaded, "capabilities": it.Info.Capabilities}
			if s, ok := c.(interface{ State() string }); ok {
				row["runtime"], row["state"] = it.Manifest.Runtime, s.State()
			}
			out = append(out, row)
		}
		WriteJSON(w, 200, map[string]any{"connectors": out})
	}))
	install := func(w http.ResponseWriter, r *http.Request, source string, dev bool, consent []string, update bool, origin string) {
		opts := d.StoreOptions()
		plan, err := opts.Prepare(r.Context(), source, dev)
		if err != nil {
			WriteError(w, 400, "store", err.Error())
			return
		}
		if origin != "" {
			if !dev || !strings.HasPrefix(origin, store.SourceDir) || plan.Trust != store.TrustLocal {
				WriteError(w, 400, "bad_request", "origin is only for your own build from a source folder (dir:<path>, --dev)")
				return
			}
			plan.Source = origin
		}
		if update && plan.Installed == plan.Version && !strings.HasPrefix(plan.Source, store.SourceDir) {
			WriteJSON(w, 200, map[string]any{"status": "up_to_date", "plan": plan})
			return
		}
		info, warn, err := d.installPlan(opts, plan, consent)
		if errors.Is(err, store.ErrConsent) {
			WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "consent_required",
				"message": "review the capabilities and confirm"}, "plan": plan})
			return
		}
		if err != nil {
			WriteError(w, 500, "store", err.Error())
			return
		}
		res := map[string]any{"status": "installed", "installed": info}
		if warn != "" {
			res["warning"] = warn
		}
		WriteJSON(w, 200, res)
	}
	mux.HandleFunc("POST /v1/connectors/install", owner(func(w http.ResponseWriter, r *http.Request) {
		var req installReq
		if err := readJSON(r, &req); err != nil || req.Source == "" {
			WriteError(w, 400, "bad_request", "source is required")
			return
		}
		install(w, r, req.Source, req.Dev, req.Consent, false, req.Origin)
	}))
	mux.HandleFunc("POST /v1/connectors/update", owner(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name    string   `json:"name"`
			Consent []string `json:"consent"`
		}
		if err := readJSON(r, &req); err != nil || req.Name == "" {
			WriteError(w, 400, "bad_request", "name is required")
			return
		}
		// Коннектор из папки с исходниками пересобирается из неё же (своя сборка).
		if info, err := d.StoreOptions().Info(req.Name); err == nil && strings.HasPrefix(info.Source, store.SourceDir) {
			install(w, r, info.Source, true, req.Consent, true, "")
			return
		}
		install(w, r, req.Name, false, req.Consent, true, "")
	}))
	mux.HandleFunc("POST /v1/connectors/remove", owner(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := readJSON(r, &req); err != nil || req.Name == "" {
			WriteError(w, 400, "bad_request", "name is required")
			return
		}
		// Тот же путь, что у Web UI: нативный процесс останавливается (на
		// Windows иначе exe занят), установка того же коннектора ждёт.
		warn, err := d.RemoveConnector(req.Name)
		if err != nil {
			WriteError(w, 400, "store", err.Error())
			return
		}
		res := map[string]any{"status": "removed", "connector": req.Name}
		if warn != "" {
			res["warning"] = warn
		}
		WriteJSON(w, 200, res)
	}))
	mux.HandleFunc("GET /v1/connectors/logs", owner(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			WriteError(w, 400, "bad_request", "name is required")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var since time.Time
		if s := r.URL.Query().Get("since"); s != "" {
			dur, err := time.ParseDuration(s)
			if err != nil {
				WriteError(w, 400, "bad_request", "since: "+err.Error())
				return
			}
			since = time.Now().Add(-dur)
		}
		WriteJSON(w, 200, map[string]any{"connector": name, "lines": d.ConnLogs.Lines(name, since, limit)})
	}))
	mux.HandleFunc("POST /v1/connectors/refresh", owner(func(w http.ResponseWriter, r *http.Request) {
		ix, err := d.StoreOptions().RefreshIndex(r.Context())
		if err != nil {
			WriteError(w, 400, "store", err.Error())
			return
		}
		d.dropRevoked()
		WriteJSON(w, 200, map[string]any{"status": "ok", "connectors": len(ix.Connectors), "revoked": len(ix.Revoked)})
	}))
}
