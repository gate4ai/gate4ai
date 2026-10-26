// SPDX-License-Identifier: AGPL-3.0-only

package daemon

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/knowledge"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/policy"
)

// KnowledgeDir — папка знаний владельца (настройка knowledge.dir; по умолчанию
// ~/gate4ai-knowledge). gate4ai складывает в неё папки коннекторов файлами и
// клонирует чужие репозитории; git init и push — дело владельца.
func (d *Daemon) KnowledgeDir() string {
	return knowledge.Resolve(d.Config.Current().Knowledge.Dir, d.userHome)
}

// Knowledge — папки коннекторов в папке знаний с признаком «поставлен».
func (d *Daemon) Knowledge() ([]knowledge.Folder, error) {
	list, err := knowledge.List(d.KnowledgeDir())
	if err != nil {
		return nil, err
	}
	return list, nil
}

// knowledgeRoutes — API владельца: папка знаний и проверка перед публикацией.
func (d *Daemon) knowledgeRoutes(mux *http.ServeMux) {
	owner := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !PrincipalFrom(r.Context()).IsOwner() {
				WriteError(w, 403, "owner_only", "only the owner sees the knowledge folder")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /v1/knowledge", owner(func(w http.ResponseWriter, r *http.Request) {
		list, err := d.Knowledge()
		if err != nil {
			WriteError(w, 500, "knowledge", err.Error())
			return
		}
		installed := d.installedSources()
		out := make([]map[string]any, 0, len(list))
		for _, f := range list {
			row := map[string]any{"name": f.Name, "path": f.Path, "connector": f.Connector, "sites": f.Sites(), "git": f.Git, "state": f.State,
				"installed": installed[f.Path] || (f.Connector != "" && d.isInstalled(f.Connector)), "publish": knowledge.HowToPublish(f, "")}
			if f.Recipe != nil {
				row["recipe"] = f.Recipe
			}
			if len(f.Problems) > 0 {
				row["problems"] = f.Problems
			}
			out = append(out, row)
		}
		WriteJSON(w, 200, map[string]any{"dir": d.KnowledgeDir(), "folders": out})
	}))
	mux.HandleFunc("GET /v1/sites/lookup", owner(func(w http.ResponseWriter, r *http.Request) {
		dom := strings.TrimSpace(r.URL.Query().Get("domain"))
		if dom == "" {
			WriteError(w, 400, "bad_request", "domain is required")
			return
		}
		WriteJSON(w, 200, d.SitesLookup(dom, r.URL.Query().Get("agent")))
	}))
	mux.HandleFunc("POST /v1/knowledge/check", owner(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := readJSON(r, &req); err != nil || req.Name == "" {
			WriteError(w, 400, "bad_request", "name is required")
			return
		}
		f, ok := knowledge.Find(d.KnowledgeDir(), req.Name)
		if !ok {
			WriteError(w, 404, "not_found", fmt.Sprintf("no %s in %s", req.Name, d.KnowledgeDir()))
			return
		}
		rep := knowledge.Check(f.Path)
		WriteJSON(w, 200, map[string]any{"report": rep, "publish": knowledge.HowToPublish(f, "")})
	}))
}

// installedSources — папки исходников, из которых поставлены коннекторы (dir:<путь>).
func (d *Daemon) installedSources() map[string]bool {
	out := map[string]bool{}
	list, _ := store.List(d.ConnectorsDir)
	for _, it := range list {
		if strings.HasPrefix(it.Info.Source, store.SourceDir) {
			out[strings.TrimPrefix(it.Info.Source, store.SourceDir)] = true
		}
	}
	return out
}

func (d *Daemon) isInstalled(name string) bool {
	_, ok := d.Registry.Get(name)
	return ok && !slices.Contains(BuiltinConnectors, name)
}

// SitesLookup — что gate4ai знает о сайте: поставленные коннекторы, магазин,
// папка знаний, вход (учётки владельца с этим origin в bind и коннекторы входа)
// и подсказка следующего шага агенту. agent — чей профиль (фильтр коннекторов).
func (d *Daemon) SitesLookup(raw, agent string) map[string]any {
	host := strings.ToLower(strings.TrimSuffix(domain.HostOf(raw), "."))
	if host == "" {
		host = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(raw), "https://"), "http://"), "/"))
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
	}
	cfg := d.Config.Current()
	prof, hasProf := cfg.Profiles[cfg.Agents[agent].Profile]
	visible := func(name string) bool {
		return agent == "" || !hasProf || prof.HasConnector(name) || policy.Everyone[name]
	}
	out := map[string]any{"domain": host}

	// Поставленные коннекторы с этим сайтом.
	var installed []map[string]any
	var login []map[string]any
	for _, m := range d.Registry.Manifests() {
		if slices.Contains(BuiltinConnectors, m.Connector) {
			continue
		}
		sites := cfg.ConnectorSites(m.Connector)
		if sites == nil {
			sites = m.Sites
		}
		matches := false
		for _, s := range sites {
			if domain.HostMatches(host, s.Domain) || domain.HostMatches(s.Domain, host) {
				matches = true
				break
			}
		}
		if isLoginConnector(m) && (matches || bindMatches(host, slotBinds(m))) {
			row := map[string]any{"connector": m.Connector, "kind": "login_connector", "installed": true, "visible": visible(m.Connector)}
			login = append(login, row)
		}
		if !matches {
			continue
		}
		row := map[string]any{"connector": m.Connector, "version": m.Version, "actions": d.actionRuns(m), "visible": visible(m.Connector)}
		if isLoginConnector(m) {
			row["login_connector"] = true
		}
		if !visible(m.Connector) {
			row["hint"] = fmt.Sprintf("not in your profile: ask the owner with change_policy add_connector %s", m.Connector)
		}
		installed = append(installed, row)
	}
	sort.Slice(installed, func(i, j int) bool { return installed[i]["connector"].(string) < installed[j]["connector"].(string) })
	out["installed"] = nonNil(installed)

	// Магазин: записи индекса, чьи возможности называют этот сайт.
	var inStore []map[string]any
	if ix := d.StoreOptions().CachedIndex(); ix != nil {
		for name, e := range ix.Connectors {
			if d.isInstalled(name) {
				continue
			}
			rel, ok := ix.Latest(name)
			if !ok {
				continue
			}
			if revoked, _ := ix.IsRevoked(name, rel.Version); revoked {
				continue
			}
			for _, c := range rel.Capabilities {
				if !strings.HasPrefix(c, "site ") {
					continue
				}
				dom := strings.TrimPrefix(c, "site ")
				dom, _, _ = strings.Cut(dom, ":")
				dom = strings.TrimSpace(dom)
				if domain.HostMatches(host, dom) || domain.HostMatches(dom, host) {
					inStore = append(inStore, map[string]any{"connector": name, "version": rel.Version, "publisher": e.Publisher, "trust": ix.Trust(name), "description": e.Description, "repo": e.Repo})
					break
				}
			}
		}
	}
	sort.Slice(inStore, func(i, j int) bool { return inStore[i]["connector"].(string) < inStore[j]["connector"].(string) })
	out["store"] = nonNil(inStore)

	// Папка знаний: свои и клонированные папки с этим сайтом.
	var known []map[string]any
	if list, err := d.Knowledge(); err == nil {
		for _, f := range list {
			if !f.MatchesSite(host) {
				continue
			}
			row := map[string]any{"folder": f.Name, "path": f.Path, "connector": f.Connector, "state": f.State,
				"installed": f.Connector != "" && d.isInstalled(f.Connector)}
			if f.Recipe != nil {
				row["tasks"] = f.Recipe.Tasks
				if f.Recipe.Login != nil {
					row["login"] = f.Recipe.Login
				}
			}
			if f.Git.Remote != "" {
				row["repo"] = f.Git.Remote
			}
			known = append(known, row)
		}
	}
	out["knowledge"] = nonNil(known)

	// Вход: учётки владельца, у которых этот origin в bind.
	for alias, cr := range cfg.Credentials {
		if !bindMatches(host, cr.Bind) {
			continue
		}
		row := map[string]any{"account": alias, "kind": "form", "bind": cr.Bind, "available": agent == "" || slices.Contains(cr.Use, cfg.Agents[agent].Profile)}
		if cr.Via != nil {
			row["kind"], row["via"] = "via", cr.Via.Connector
		}
		login = append(login, row)
	}
	sort.Slice(login, func(i, j int) bool {
		return fmt.Sprint(login[i]["account"], login[i]["connector"]) < fmt.Sprint(login[j]["account"], login[j]["connector"])
	})
	out["login"] = nonNil(login)
	out["knowledge_dir"] = d.KnowledgeDir()
	out["hint"] = lookupHint(host, installed, inStore, known, login)
	return out
}

func nonNil(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

// isLoginConnector — у коннектора есть действие login {account}: коннектор входа.
func isLoginConnector(m manifest.Manifest) bool {
	for _, a := range m.Actions {
		if a.Name == "login" {
			return true
		}
	}
	return false
}

func slotBinds(m manifest.Manifest) []string {
	var out []string
	for _, c := range m.Credentials {
		out = append(out, c.Bind...)
	}
	return out
}

// bindMatches — хост совпадает с одним из origin.
func bindMatches(host string, binds []string) bool {
	for _, b := range binds {
		if h := domain.HostOf(b); h != "" && strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// lookupHint — следующий шаг агенту по тому, что нашлось.
func lookupHint(host string, installed, inStore, known, login []map[string]any) string {
	var b strings.Builder
	var site []map[string]any // коннекторы к сайту, не коннекторы входа
	for _, r := range installed {
		if r["login_connector"] != true {
			site = append(site, r)
		}
	}
	switch {
	case len(site) > 0:
		c := site[0]["connector"].(string)
		fmt.Fprintf(&b, "A connector for %s is installed: call its actions (gate4ai %s <action> --help). A draft action (verified_runs < %d) runs with a trace of its browser calls; if it fails, the answer is handoff_to_agent with a browser session that is yours to finish the task in — then fix the connector in its folder.", host, c, pipeline.VerifiedAfter)
	case len(inStore) > 0:
		c := inStore[0]["connector"].(string)
		fmt.Fprintf(&b, "The store has %s for %s: ask the owner to install it — change_policy with [{\"op\":\"install_connector\",\"connector\":\"%s\"}] — then call its actions.", c, host, c)
	case len(known) > 0:
		p := known[0]["path"].(string)
		fmt.Fprintf(&b, "The knowledge folder has %s for %s: read its RECIPE.md; install it with change_policy [{\"op\":\"install_dev_connector\",\"folder\":%q}] (the owner: gate4ai connectors install %s --watch).", known[0]["folder"], host, p, p)
	default:
		name := connectorNameFor(host)
		fmt.Fprintf(&b, "No connector or recipe for %s. Work in a browser session (gate4ai browser open --url https://%s/ …); when the task is done, close it with --save_steps and keep what you learned: gate4ai connectors new %s --site %s --from-session <session> --task <task>.", host, host, name, host)
	}
	if len(login) > 0 {
		l := login[0]
		for _, r := range login { // учётка владельца полезнее коннектора входа без учётки
			if r["account"] != nil {
				l = r
				break
			}
		}
		switch {
		case l["connector"] != nil:
			fmt.Fprintf(&b, " Sign-in on %s: the login connector %s is installed — browser.login with an account that uses it (via).", host, l["connector"])
		case l["via"] != nil:
			fmt.Fprintf(&b, " Sign-in: account %s through the login connector %s — gate4ai browser login --session s-… --account %s.", l["account"], l["via"], l["account"])
		default:
			fmt.Fprintf(&b, " Sign-in: account %s — gate4ai browser login --session s-… --account %s; never ask the user for a password in the chat.", l["account"], l["account"])
		}
	} else {
		b.WriteString(" If the site asks to sign in, do not ask for a password in the chat: add_credential (change_policy) creates an account and the owner puts the secrets into the vault.")
	}
	return b.String()
}

// connectorNameFor — имя коннектора по сайту: ticket.rzd.ru → rzd, app.example.org → example.
func connectorNameFor(host string) string {
	labels := strings.Split(strings.TrimPrefix(host, "www."), ".")
	name := labels[0]
	if len(labels) >= 2 {
		name = labels[len(labels)-2]
	}
	name = strings.ReplaceAll(strings.ToLower(name), "-", "_")
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "site_" + name
	}
	return name
}

// recordRun — прогон действия коннектора закончился (pipeline.RunHook): рецепт
// в папке знаний, если коннектор поставлен из неё, получает статус задачи,
// число подтверждённых прогонов и дату. Файл меняется на месте, без git.
func (d *Daemon) recordRun(action string, ok bool, st pipeline.RunStats) {
	conn, task, _ := strings.Cut(action, ".")
	info, err := d.StoreOptions().Info(conn)
	if err != nil || !strings.HasPrefix(info.Source, store.SourceDir) {
		return
	}
	dir := strings.TrimPrefix(info.Source, store.SourceDir)
	path := filepath.Join(dir, knowledge.RecipeFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	r, body, err := knowledge.ParseRecipe(raw)
	if err != nil {
		return
	}
	t := r.Task(task)
	if t == nil {
		return
	}
	t.VerifiedRuns, t.Status = st.OK, st.Status
	if ok {
		t.LastVerified = d.Pipeline.Now().Format("2006-01-02")
	}
	if out, err := knowledge.FormatRecipe(r, body); err == nil {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			d.Log.Warn("knowledge: recipe not updated", "path", path, "err", err)
		}
	}
}

// actionRuns — статус проверенности действий коннектора для списков и lookup.
func (d *Daemon) actionRuns(m manifest.Manifest) []map[string]any {
	var out []map[string]any
	for _, a := range m.Actions {
		if a.ServiceOnly {
			continue
		}
		st := d.Pipeline.ActionRuns(m.Connector + "." + a.Name)
		out = append(out, map[string]any{"name": a.Name, "class": a.Class, "description": a.Description, "status": st.Status, "verified_runs": st.OK})
	}
	return out
}
