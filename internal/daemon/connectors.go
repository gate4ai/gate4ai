// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// StoreOptions — магазин коннекторов по текущей конфигурации.
func (d *Daemon) StoreOptions() store.Options {
	s := d.Config.Current().Store
	return store.Options{Dir: d.ConnectorsDir, Registry: s.Registry, RegistryKeys: s.RegistryKeys, TrustedKeys: s.TrustedKeys}
}

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

// reloadConnector — после установки, обновления или удаления: реестр и политика.
func (d *Daemon) reloadConnector(name string, removed bool) error {
	d.Registry.Remove(name)
	if !removed {
		c, err := d.StoreOptions().Load(name, d.Wasm)
		if err != nil {
			return err
		}
		d.Registry.Add(c)
	}
	e, err := policy.New(d.Config.Current(), policy.Catalog(d.Registry.Catalog()))
	if err != nil {
		// Политика ссылается на действия удалённого коннектора: действует прежняя
		// политика, ошибка видна владельцу.
		return err
	}
	d.Pipeline.SetEngine(e)
	return nil
}

type installReq struct {
	Source  string   `json:"source"`
	Dev     bool     `json:"dev"`
	Consent []string `json:"consent"`
}

// connectorRoutes — API владельца для магазина коннекторов.
func (d *Daemon) connectorRoutes(mux *http.ServeMux) {
	owner := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !PrincipalFrom(r.Context()).IsOwner() {
				writeErr(w, 403, "owner_only", "only the owner manages connectors")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /v1/connectors", owner(func(w http.ResponseWriter, r *http.Request) {
		list, err := store.List(d.ConnectorsDir)
		if err != nil {
			writeErr(w, 500, "store", err.Error())
			return
		}
		out := []map[string]any{}
		for _, it := range list {
			_, loaded := d.Registry.Get(it.Name)
			out = append(out, map[string]any{"connector": it.Name, "version": it.Manifest.Version, "trust": it.Info.Trust,
				"publisher": it.Info.Publisher, "source": it.Info.Source, "loaded": loaded, "capabilities": it.Info.Capabilities})
		}
		writeJSON(w, 200, map[string]any{"connectors": out})
	}))
	install := func(w http.ResponseWriter, r *http.Request, source string, dev bool, consent []string, update bool) {
		opts := d.StoreOptions()
		plan, err := opts.Prepare(r.Context(), source, dev)
		if err != nil {
			writeErr(w, 400, "store", err.Error())
			return
		}
		if update && plan.Installed == plan.Version {
			writeJSON(w, 200, map[string]any{"status": "up_to_date", "plan": plan})
			return
		}
		info, err := opts.Install(plan, consent)
		if errors.Is(err, store.ErrConsent) {
			writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "consent_required",
				"message": "review the capabilities and confirm"}, "plan": plan})
			return
		}
		if err != nil {
			writeErr(w, 500, "store", err.Error())
			return
		}
		res := map[string]any{"status": "installed", "installed": info}
		if err := d.reloadConnector(info.Connector, false); err != nil {
			res["warning"] = err.Error()
		}
		d.Log.Info("connector installed", "connector", info.Connector, "version", info.Version, "trust", info.Trust)
		writeJSON(w, 200, res)
	}
	mux.HandleFunc("POST /v1/connectors/install", owner(func(w http.ResponseWriter, r *http.Request) {
		var req installReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
			writeErr(w, 400, "bad_request", "source is required")
			return
		}
		install(w, r, req.Source, req.Dev, req.Consent, false)
	}))
	mux.HandleFunc("POST /v1/connectors/update", owner(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name    string   `json:"name"`
			Consent []string `json:"consent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "bad_request", "name is required")
			return
		}
		install(w, r, req.Name, false, req.Consent, true)
	}))
	mux.HandleFunc("POST /v1/connectors/remove", owner(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeErr(w, 400, "bad_request", "name is required")
			return
		}
		if err := d.StoreOptions().Remove(req.Name); err != nil {
			writeErr(w, 400, "store", err.Error())
			return
		}
		res := map[string]any{"status": "removed", "connector": req.Name}
		if err := d.reloadConnector(req.Name, true); err != nil {
			res["warning"] = "policy still references this connector: " + err.Error()
		}
		writeJSON(w, 200, res)
	}))
	mux.HandleFunc("POST /v1/connectors/refresh", owner(func(w http.ResponseWriter, r *http.Request) {
		ix, err := d.StoreOptions().RefreshIndex(r.Context())
		if err != nil {
			writeErr(w, 400, "store", err.Error())
			return
		}
		d.dropRevoked()
		writeJSON(w, 200, map[string]any{"status": "ok", "connectors": len(ix.Connectors), "revoked": len(ix.Revoked)})
	}))
}
