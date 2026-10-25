// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/front/skill"
	"github.com/gate4ai/gate4ai/internal/platform/agentkeys"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/gate4ai/sdk-go/access"
)

// ---- Агенты ----

// connRef — коннектор в списке: имя и человеческое название.
type connRef struct{ Name, Title string }

// agentRow — агент в списке.
type agentRow struct {
	Name    string
	Agent   config.Agent
	Seen    state.AgentSeen
	Tokens  []state.TokenInfo
	Clients []string // подключённые клиенты (автоподключение), названия
	Active  int
	Profile config.Profile
	Conns   []connRef
	Today   int // действий за сегодня
	Shared  bool
}

func (u *UI) agentRows(l string) []agentRow {
	cfg := u.D.Config.Current()
	seen, _ := u.D.State.LastSeen()
	toks, _ := u.D.State.Tokens()
	users := map[string]int{}
	for _, a := range cfg.Agents {
		users[a.Profile]++
	}
	y, mo, d := u.Now().Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
	var rows []agentRow
	for name, a := range cfg.Agents {
		row := agentRow{Name: name, Agent: a, Seen: seen[name], Profile: cfg.Profiles[a.Profile], Shared: users[a.Profile] > 1}
		for _, c := range row.Profile.Connectors {
			row.Conns = append(row.Conns, connRef{c, u.connTitle(l, c)})
		}
		for _, t := range toks {
			if t.Agent != name {
				continue
			}
			row.Tokens = append(row.Tokens, t)
			if !t.Revoked && t.Kind != "refresh" {
				row.Active++
				if c, ok := strings.CutPrefix(t.Label, "auto:"); ok {
					if title := clientTitle(c); !slices.Contains(row.Clients, title) {
						row.Clients = append(row.Clients, title)
					}
				}
			}
		}
		es, _ := u.D.Journal.List(audit.Filter{Agent: name, Since: today, Limit: 1000})
		row.Today = len(es)
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func (u *UI) agents(r *http.Request) (string, map[string]any, error) {
	return "agents", map[string]any{"Rows": u.agentRows(u.lang(r)), "Pairings": u.pairingViews()}, nil
}

// createAgent — агент с профилем (для форм и сценариев без мастера).
func (u *UI) createAgent(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	name, client := strings.TrimSpace(f.Get("name")), f.Get("client")
	profile := f.Get("profile")
	back := "/setup/access?client=" + url.QueryEscape(client) + "&name=" + url.QueryEscape(name)
	if _, exists := u.D.Config.Current().Agents[name]; exists {
		return redirectErr(back, M("An agent named %s already exists — choose another name", name))
	}
	err := u.edit("add agent "+name, func(c *config.Config) error {
		if profile == "" || profile == "_new" {
			profile = name
			if err := c.EnsureProfile(profile); err != nil {
				return err
			}
			for _, conn := range f["connectors"] {
				if err := c.SetProfileConnector(profile, conn, true); err != nil {
					return err
				}
			}
			if t, ok := findTemplate(f.Get("template")); ok {
				if err := t.Apply(c, profile, map[string]string{"folder": f.Get("folder"), "trusted": f.Get("trusted"), "sites": f.Get("sites"), "n": f.Get("n")}, u.tools()); err != nil {
					return M("Ready-made rules “%s”: %s", Word(t.Title), err)
				}
			}
		}
		return c.SetAgent(name, profile, f.Get("description"))
	})
	if err != nil {
		return redirectErr(back, err)
	}
	to := "/agents/" + url.PathEscape(name)
	if client != "" {
		to += "?client=" + url.QueryEscape(client)
	}
	return redirect(to, M("Agent %s added. Now connect it.", name))
}

func (u *UI) agentPage(r *http.Request) (string, map[string]any, error) {
	l := u.lang(r)
	name := r.PathValue("name")
	cfg := u.D.Config.Current()
	a, ok := cfg.Agents[name]
	if !ok {
		return "agents", map[string]any{"Rows": u.agentRows(l)}, M("Agent %s not found", name)
	}
	var row agentRow
	for _, x := range u.agentRows(l) {
		if x.Name == name {
			row = x
		}
	}
	journal, _ := u.D.Journal.List(audit.Filter{Agent: name, Limit: 10, Desc: true})
	profiles := slices.Sorted(maps.Keys(cfg.Profiles))
	client := r.URL.Query().Get("client")
	if client == "" {
		for _, t := range row.Tokens {
			if c, ok := strings.CutPrefix(t.Label, "auto:"); ok && !t.Revoked {
				client = c
			}
		}
		if client == "" {
			if _, ok := agentsetup.Find(name); ok {
				client = name
			}
		}
	}
	prof := cfg.Profiles[a.Profile]
	type access struct {
		Conn   connRef
		Groups []groupView
	}
	var acc []access
	for _, c := range prof.Connectors {
		acc = append(acc, access{connRef{c, u.connTitle(l, c)}, u.pageGroups(c, prof)})
	}
	data := map[string]any{"Name": name, "Agent": a, "Row": row, "Journal": u.entryViews(l, journal), "Profiles": profiles, "Client": client,
		"Clients": u.clientTiles(), "Bin": u.bin(), "PublicURL": u.D.PublicURL(), "Relay": u.relay(), "Profile": prof, "Access": acc,
		"FilesHome": filesWholeHome(cfg)}
	if c, ok := agentsetup.Find(client); ok {
		data["ClientInfo"] = c
		p, snippet := u.Clients.Snippet(client, u.bin(), name)
		data["SnippetPath"], data["Snippet"] = p, snippet
	}
	return "agent", data, nil
}

// filesWholeHome — коннектору «Файлы» открыт весь домашний каталог.
func filesWholeHome(cfg *config.Config) bool {
	for _, f := range cfg.Access.Folders {
		if (f.Path == "~" || f.Path == "~/" || f.Path == "/") && f.Access != access.Deny {
			return true
		}
	}
	return false
}

// clientTile — клиент для подключения.
type clientTile struct {
	agentsetup.Client
	Installed, Connected bool
	Path                 string
}

func (u *UI) clientTiles() []clientTile {
	var out []clientTile
	for _, c := range agentsetup.Clients {
		out = append(out, clientTile{Client: c, Installed: u.Clients.Installed(c.ID), Connected: u.Clients.Connected(c.ID), Path: u.Clients.ConfigPath(c.ID)})
	}
	return out
}

func (u *UI) agentPageWith(r *http.Request, extra map[string]any) map[string]any {
	_, d, err := u.agentPage(r)
	maps.Copy(d, extra)
	if err != nil {
		d["error"] = err
	}
	d["page"] = "agent"
	return d
}

func (u *UI) token(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	if _, ok := u.D.Config.Current().Agents[name]; !ok {
		return redirectErr("/agents", M("Unknown agent %s", name))
	}
	tok, err := u.D.State.IssueToken(name, r.PostForm.Get("label"), "agent")
	if err != nil {
		return redirectErr("/agents/"+url.PathEscape(name), err)
	}
	extra := map[string]any{"NewToken": tok}
	if c := r.PostForm.Get("client"); c != "" {
		if _, ok := agentsetup.Find(c); ok {
			p, snippet := u.Clients.Snippet(c, u.bin(), name)
			extra["NewSnippetPath"], extra["NewSnippet"] = p, snippet
		}
	}
	return u.agentPageWith(r, extra)
}

func (u *UI) connect(w http.ResponseWriter, r *http.Request) map[string]any {
	code, exp, err := u.D.EnableOAuth(nil).NewConnectCode(r.PathValue("name"))
	if err != nil {
		return redirectErr("/agents/"+url.PathEscape(r.PathValue("name")), err)
	}
	return u.agentPageWith(r, map[string]any{"ConnectCode": code, "ConnectExpires": exp})
}

func (u *UI) revoke(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	n, err := u.D.State.RevokeAgent(name)
	if err != nil {
		return redirectErr("/agents/"+url.PathEscape(name), err)
	}
	return redirect("/agents/"+url.PathEscape(name), M("Tokens revoked: %d. %s can no longer reach gate4.ai until you connect it again.", n, name))
}

func (u *UI) deleteAgent(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	_, _ = u.D.State.RevokeAgent(name)
	for _, c := range agentsetup.Clients {
		if u.Clients.Connected(c.ID) && c.Agent == name {
			_, _ = u.Clients.Disconnect(c.ID)
		}
	}
	if err := u.edit("remove agent "+name, func(c *config.Config) error { return c.RemoveAgent(name) }); err != nil {
		return redirectErr("/agents/"+url.PathEscape(name), err)
	}
	return redirect("/agents", M("Agent %s removed; its tokens are revoked.", name))
}

func (u *UI) setAgentProfile(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	cur := u.D.Config.Current().Agents[name]
	if err := u.edit("agent "+name+" profile", func(c *config.Config) error {
		return c.SetAgent(name, r.PostForm.Get("profile"), cur.Description)
	}); err != nil {
		return redirectErr("/agents/"+url.PathEscape(name), err)
	}
	return redirect("/agents/"+url.PathEscape(name), "Permissions changed")
}

// connectClient — автоподключение: токен + запись в конфигурацию клиента.
func (u *UI) connectClient(w http.ResponseWriter, r *http.Request) map[string]any {
	name, client := r.PathValue("name"), r.PostForm.Get("client")
	back := "/agents/" + url.PathEscape(name) + "?client=" + url.QueryEscape(client)
	c, ok := agentsetup.Find(client)
	if !ok {
		return redirectErr(back, M("Unknown client %q", client))
	}
	if _, ok := u.D.Config.Current().Agents[name]; !ok {
		return redirectErr("/agents", M("Unknown agent %s", name))
	}
	res, err := u.autoConnect(name, client)
	if err != nil {
		return redirectErr(back, err)
	}
	return redirectDetail(back, M("%s is connected. %s", c.Title, Word(c.Restart)), connectDetail(res))
}

// autoConnect — автоподключение клиента: ключ агента (вместо прежнего ключа
// этого клиента) — в каталог ключей, сервер gate4ai — в конфигурацию клиента.
// Владелец подключает сам, поэтому кода для сверки не нужно.
func (u *UI) autoConnect(name, client string) (agentsetup.Result, error) {
	_, _ = u.D.State.RevokeLabel(name, "auto:"+client)
	tok, err := u.D.State.IssueToken(name, "auto:"+client, "agent")
	if err != nil {
		return agentsetup.Result{}, err
	}
	if err := agentkeys.Save(u.KeyDir, agentkeys.Key{Agent: name, Kind: client, Key: tok}); err != nil {
		_, _ = u.D.State.RevokeLabel(name, "auto:"+client)
		return agentsetup.Result{}, err
	}
	res, err := u.Clients.Connect(client, u.bin(), name, skill.Stub())
	if err != nil {
		_, _ = u.D.State.RevokeLabel(name, "auto:"+client)
		agentkeys.Remove(u.KeyDir, name)
		return res, err
	}
	res.Notes = append(res.Notes, "key: "+agentkeys.Path(u.KeyDir, name))
	return res, nil
}

func (u *UI) disconnectClient(w http.ResponseWriter, r *http.Request) map[string]any {
	name, client := r.PathValue("name"), r.PostForm.Get("client")
	back := "/agents/" + url.PathEscape(name)
	res, err := u.Clients.Disconnect(client)
	if err != nil {
		return redirectErr(back, err)
	}
	n, _ := u.D.State.RevokeLabel(name, "auto:"+client)
	for _, k := range agentkeys.Load(u.KeyDir) {
		if k.Agent == name && k.Kind == client {
			agentkeys.Remove(u.KeyDir, name)
		}
	}
	return redirectDetail(back, M("gate4ai is removed from %s; tokens revoked: %d", clientTitle(client), n), res.Path)
}
