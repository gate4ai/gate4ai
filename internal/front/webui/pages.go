// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/front/skill"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/platform/autostart"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
	"github.com/gate4ai/gate4ai/internal/platform/version"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/internal/state"
)

// ---- Общее ----

// redirect — переход после действия формы с сообщением (flash) или ошибкой.
func redirect(to, flash string) map[string]any {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	if flash == "" {
		return map[string]any{"redirect": to}
	}
	return map[string]any{"redirect": to + sep + "flash=" + urlq(flash)}
}

func redirectErr(to string, err error) map[string]any {
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	return map[string]any{"redirect": to + sep + "error=" + urlq(err.Error())}
}

func (u *UI) edit(comment string, f func(c *config.Config) error) error {
	_, err := u.D.Config.Edit(owner, "web: "+comment, f)
	return err
}

// tools — каталог действий (вместе с gate4ai.* — на них тоже пишутся правила).
func (u *UI) tools() []daemon.Tool { return u.D.Tools(owner) }

func (u *UI) bin() string {
	if u.Bin != "" {
		return u.Bin
	}
	b, err := os.Executable()
	if err != nil {
		return "gate4ai"
	}
	if r, err := filepath.EvalSymlinks(b); err == nil {
		return r
	}
	return b
}

func (u *UI) relay() relayclient.Status {
	if u.Relay == nil {
		return relayclient.Status{}
	}
	return u.Relay()
}

// installed — установленные коннекторы (имя → запись).
func (u *UI) installed() map[string]store.Installed {
	out := map[string]store.Installed{}
	list, _ := store.List(u.D.ConnectorsDir)
	for _, it := range list {
		out[it.Name] = it
	}
	return out
}

// connectorNames — коннекторы, доступные для профилей (загруженные, кроме встроенных).
func (u *UI) connectorNames() []string {
	var out []string
	for _, m := range u.D.Registry.Manifests() {
		if policy.Implicit[m.Connector] || policy.Everyone[m.Connector] {
			continue
		}
		out = append(out, m.Connector)
	}
	sort.Strings(out)
	return out
}

// ---- Обзор ----

// step — пункт чек-листа первого запуска.
type step struct {
	Title, Text, Link, Button string
	Done                      bool
}

func (u *UI) overview(m mount) func(r *http.Request) (string, map[string]any, error) {
	return func(r *http.Request) (string, map[string]any, error) {
		cfg := u.D.Config.Current()
		pend, err := u.D.State.ListApprovals(state.Pending, 50)
		recent, _ := u.D.Journal.List(audit.Filter{Limit: 8, Desc: true})
		seen, _ := u.D.State.LastSeen()
		data := map[string]any{"Pending": pend, "Recent": recent, "Seen": seen, "Agents": cfg.Agents}
		if m.remote {
			return "overview", data, err
		}
		conns := u.connectorNames()
		anySeen := false
		for a := range cfg.Agents {
			if !seen[a].TS.IsZero() {
				anySeen = true
			}
		}
		channelOK := cfg.ChannelOn("telegram") || cfg.ChannelOn("webpush")
		rs := u.relay()
		steps := []step{
			{Title: "Поставьте коннектор", Text: "Почта, файлы, браузер — то, к чему агенту нужен доступ. Установка в один клик.", Link: "/connectors", Button: "Магазин коннекторов", Done: len(conns) > 0},
			{Title: "Добавьте агента", Text: "Claude Code, Claude Desktop, Cursor, VS Code, Codex, Gemini CLI или облачный агент. Выберите, что ему можно.", Link: "/agents/new", Button: "Добавить агента", Done: len(cfg.Agents) > 0},
			{Title: "Проверьте, что агент подключился", Text: "Здесь появится время последнего обращения агента к gate4.ai.", Link: "/agents", Button: "Агенты", Done: anySeen},
			{Title: "Включите подтверждения на телефоне", Text: "Telegram или браузерный push — чтобы разрешать действия, когда вы не у компьютера.", Link: "/approvals#channels", Button: "Каналы уведомлений", Done: channelOK},
			{Title: "Облачные агенты: включите relay", Text: "Нужно только для Claude.ai, ChatGPT и других агентов не на этом компьютере.", Link: "/settings#relay", Button: "Настройки relay", Done: rs.Configured || u.D.PublicURL() != ""},
		}
		left := 0
		for _, s := range steps {
			if !s.Done {
				left++
			}
		}
		data["Steps"], data["StepsLeft"] = steps, left
		data["Relay"] = rs
		data["PublicURL"] = u.D.PublicURL()
		if u.Channels != nil {
			data["Channels"] = u.Channels.Status()
		}
		_, n := u.D.Config.CurrentRaw()
		data["ConfigVersion"] = n
		data["Chain"] = u.D.Journal.VerifyChain() == nil
		return "overview", data, err
	}
}

// ---- Подтверждения ----

func (u *UI) approvalsPage(m mount) func(r *http.Request) (string, map[string]any, error) {
	return func(r *http.Request) (string, map[string]any, error) {
		pend, err := u.D.State.ListApprovals(state.Pending, 50)
		status := r.URL.Query().Get("status")
		recent, _ := u.D.State.ListApprovals(status, 50)
		recent = slices.DeleteFunc(recent, func(a *state.Approval) bool { return a.Status == state.Pending })
		data := map[string]any{"Pending": pend, "Recent": recent, "Status": status}
		cfg := u.D.Config.Current()
		data["Timeout"] = config.FormatDuration(cfg.Approvals.Timeout.D())
		if u.Channels != nil {
			data["Channels"] = u.Channels.Status()
			if u.Channels.Push != nil {
				data["PushKey"] = u.Channels.Push.PublicKey()
			}
		}
		data["PushOn"] = cfg.ChannelOn("webpush")
		return "approvals", data, err
	}
}

// decide — решение по запросу; канал в квитанции различает локальный и удалённый вход.
func (u *UI) decide(m mount) func(w http.ResponseWriter, r *http.Request) map[string]any {
	channel := "webui"
	if m.remote {
		channel = "webui-remote"
	}
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		approve := r.PostForm.Get("decision") == "approve" || r.PostForm.Get("decision") == "approve_rule"
		a, _ := u.D.State.GetApproval(r.PathValue("id"))
		res, err := u.D.Pipeline.Decide(r.Context(), r.PathValue("id"), approve, channel, "owner")
		back := or(r.PostForm.Get("back"), "/approvals")
		if err != nil {
			return redirectErr(back, err)
		}
		verb := "Отклонено"
		if approve {
			verb = "Разрешено"
		}
		msg := verb
		if a != nil {
			msg += ": " + a.Agent + " → " + a.Action
		}
		msg += " · результат " + res.Status
		if res.Receipt != "" {
			msg += " #" + audit.Short(res.Receipt)
		}
		if r.PostForm.Get("decision") == "approve_rule" && a != nil && !m.remote {
			return redirect("/policy/rule?approval="+url.QueryEscape(a.ID), msg+". Теперь можно создать правило, чтобы не спрашивать в следующий раз.")
		}
		return redirect(back, msg)
	}
}

// pushSubscribe — подписка браузера на push (JSON из app.js).
func (u *UI) pushSubscribe(m mount) func(w http.ResponseWriter, r *http.Request) map[string]any {
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		w.Header().Set("Content-Type", "application/json")
		var sub state.PushSub
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&sub); err != nil {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad subscription"})
			return nil
		}
		if r.URL.Query().Get("remove") == "1" {
			_ = u.D.State.RemovePushSub(sub.Endpoint)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return nil
		}
		sub.Label = m.base
		if err := u.D.State.AddPushSub(sub); err != nil {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return nil
		}
		enabled := u.D.Config.Current().ChannelOn("webpush")
		if !enabled && !m.remote {
			enabled = u.edit("enable browser push", func(c *config.Config) error { c.SetChannel("webpush", true); return nil }) == nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel_on": enabled})
		return nil
	}
}

func (u *UI) toggleChannel(w http.ResponseWriter, r *http.Request) map[string]any {
	name, on := r.PathValue("name"), r.PostForm.Get("on") == "1"
	if name == "telegram" && on && u.D.Config.Current().Approvals.Telegram == nil {
		return redirect("/approvals/telegram", "")
	}
	if err := u.edit("approval channel "+name, func(c *config.Config) error { c.SetChannel(name, on); return nil }); err != nil {
		return redirectErr("/approvals#channels", err)
	}
	return redirect("/approvals#channels", map[bool]string{true: "Канал включён", false: "Канал выключен"}[on])
}

func (u *UI) testChannel(w http.ResponseWriter, r *http.Request) map[string]any {
	if u.Channels == nil {
		return redirectErr("/approvals#channels", errors.New("channels are not attached"))
	}
	if err := u.Channels.Test(r.Context(), r.PathValue("name")); err != nil {
		return redirectErr("/approvals#channels", err)
	}
	return redirect("/approvals#channels", "Пробное уведомление отправлено — проверьте, пришло ли оно.")
}

func (u *UI) saveApprovalSettings(w http.ResponseWriter, r *http.Request) map[string]any {
	d, err := config.ParseDuration(strings.TrimSpace(r.PostForm.Get("timeout")))
	if err != nil || d <= 0 {
		return redirectErr(or(r.PostForm.Get("back"), "/approvals#channels"), errors.New("timeout: use e.g. 30m, 2h"))
	}
	if err := u.edit("approval timeout", func(c *config.Config) error { c.Approvals.Timeout = config.Duration(d); return nil }); err != nil {
		return redirectErr("/approvals#channels", err)
	}
	return redirect(or(r.PostForm.Get("back"), "/approvals#channels"), "Сохранено")
}

// ---- Telegram: мастер ----

func (u *UI) telegramPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	_, tokErr := u.D.Vault.Get("@telegram.bot")
	data := map[string]any{"HasToken": tokErr == nil, "Bot": r.URL.Query().Get("bot"), "Telegram": cfg.Approvals.Telegram,
		"On": cfg.ChannelOn("telegram"), "Public": u.D.PublicURL() != ""}
	return "telegram", data, nil
}

func (u *UI) telegramStep(w http.ResponseWriter, r *http.Request) map[string]any {
	if u.Channels == nil {
		return redirectErr("/approvals/telegram", errors.New("channels are not attached"))
	}
	ctx := r.Context()
	switch r.PostForm.Get("step") {
	case "token":
		tok := strings.TrimSpace(r.PostForm.Get("token"))
		if tok == "" {
			return redirectErr("/approvals/telegram", errors.New("paste the token from @BotFather"))
		}
		name, err := u.Channels.TelegramBot(ctx, tok)
		if err != nil {
			return redirectErr("/approvals/telegram", fmt.Errorf("Telegram did not accept the token: %v", err))
		}
		if err := u.D.Vault.Set("@telegram.bot", tok); err != nil {
			return redirectErr("/approvals/telegram", err)
		}
		return redirect("/approvals/telegram?bot="+url.QueryEscape(name), "Бот @"+name+" найден, токен сохранён в хранилище секретов.")
	case "find":
		tok, err := u.D.Vault.Get("@telegram.bot")
		if err != nil {
			return redirectErr("/approvals/telegram", errors.New("save the bot token first"))
		}
		users, err := u.Channels.TelegramWriters(ctx, tok)
		data, _ := func() (map[string]any, error) { _, d, e := u.telegramPage(r); return d, e }()
		data["page"] = "telegram"
		data["Users"] = users
		data["Searched"] = true
		if err != nil {
			data["error"] = "Telegram: " + err.Error()
		}
		return data
	case "owner":
		id, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("owner_id")), 10, 64)
		if err != nil || id == 0 {
			return redirectErr("/approvals/telegram", errors.New("owner id must be a number"))
		}
		if err := u.edit("telegram approvals", func(c *config.Config) error {
			t := &config.Telegram{Token: "@telegram.bot", OwnerID: id}
			if c.Approvals.Telegram != nil {
				t.Webhook = c.Approvals.Telegram.Webhook
			}
			t.Webhook = r.PostForm.Get("webhook") == "1"
			c.Approvals.Telegram = t
			c.SetChannel("telegram", true)
			return nil
		}); err != nil {
			return redirectErr("/approvals/telegram", err)
		}
		// Канал перезапускается подпиской на конфигурацию; пробное сообщение — сразу.
		if err := u.Channels.Test(ctx, "telegram"); err != nil {
			return redirectErr("/approvals#channels", fmt.Errorf("Telegram is on, but the test message failed: %v", err))
		}
		return redirect("/approvals#channels", "Telegram включён — пробное сообщение отправлено вам в чат с ботом.")
	}
	return redirectErr("/approvals/telegram", errors.New("unknown step"))
}

// ---- Агенты ----

// agentRow — агент в списке.
type agentRow struct {
	Name    string
	Agent   config.Agent
	Seen    state.AgentSeen
	Tokens  []state.TokenInfo
	Clients []string // подключённые клиенты (автоподключение)
	Active  int
	Profile config.Profile
}

func (u *UI) agentRows() []agentRow {
	cfg := u.D.Config.Current()
	seen, _ := u.D.State.LastSeen()
	toks, _ := u.D.State.Tokens()
	var rows []agentRow
	for name, a := range cfg.Agents {
		row := agentRow{Name: name, Agent: a, Seen: seen[name], Profile: cfg.Profiles[a.Profile]}
		for _, t := range toks {
			if t.Agent != name {
				continue
			}
			row.Tokens = append(row.Tokens, t)
			if !t.Revoked && t.Kind != "refresh" {
				row.Active++
				if c, ok := strings.CutPrefix(t.Label, "auto:"); ok && !slices.Contains(row.Clients, c) {
					row.Clients = append(row.Clients, c)
				}
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

func (u *UI) agents(r *http.Request) (string, map[string]any, error) {
	return "agents", map[string]any{"Rows": u.agentRows()}, nil
}

// clientTile — плитка клиента в мастере.
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

func (u *UI) agentNew(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	q := r.URL.Query()
	kind := q.Get("client")
	name := q.Get("name")
	if c, ok := agentsetup.Find(kind); ok && name == "" {
		name = c.Agent
	}
	switch kind {
	case "cloud":
		name = or(name, "claude-ai")
	case "cli":
		name = or(name, "my-agent")
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for p := range cfg.Profiles {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	return "agent_new", map[string]any{"Clients": u.clientTiles(), "Client": kind, "Name": name, "Profiles": profiles,
		"Connectors": u.connectorNames(), "Templates": Templates}, nil
}

func (u *UI) createAgent(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	name, client := strings.TrimSpace(f.Get("name")), f.Get("client")
	profile := f.Get("profile")
	back := "/agents/new?client=" + url.QueryEscape(client) + "&name=" + url.QueryEscape(name)
	if _, exists := u.D.Config.Current().Agents[name]; exists {
		return redirectErr(back, fmt.Errorf("agent %q already exists", name))
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
					return fmt.Errorf("template «%s»: %w", t.Title, err)
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
	return redirect(to, "Агент "+name+" добавлен. Теперь подключите его.")
}

func (u *UI) agentPage(r *http.Request) (string, map[string]any, error) {
	name := r.PathValue("name")
	cfg := u.D.Config.Current()
	a, ok := cfg.Agents[name]
	if !ok {
		return "agents", map[string]any{"Rows": u.agentRows()}, fmt.Errorf("agent %q not found", name)
	}
	var row agentRow
	for _, x := range u.agentRows() {
		if x.Name == name {
			row = x
		}
	}
	journal, _ := u.D.Journal.List(audit.Filter{Agent: name, Limit: 10, Desc: true})
	profiles := make([]string, 0, len(cfg.Profiles))
	for p := range cfg.Profiles {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	client := r.URL.Query().Get("client")
	if client == "" {
		if len(row.Clients) > 0 {
			client = row.Clients[0]
		} else if _, ok := agentsetup.Find(name); ok {
			client = name
		}
	}
	data := map[string]any{"Name": name, "Agent": a, "Row": row, "Journal": journal, "Profiles": profiles, "Client": client,
		"Clients": u.clientTiles(), "Bin": u.bin(), "PublicURL": u.D.PublicURL(), "Relay": u.relay(), "Profile": cfg.Profiles[a.Profile]}
	if c, ok := agentsetup.Find(client); ok {
		data["ClientInfo"] = c
		p, snippet := u.Clients.Snippet(client, u.bin(), "<токен>")
		data["SnippetPath"], data["Snippet"] = p, snippet
	}
	return "agent", data, nil
}

func agentPrincipal(agent string) auth.Principal { return auth.AgentVia(agent, "webui") }

// skillMD — SKILL.md для агента (его каталог).
func (u *UI) skillMD(agent string) string {
	var tools []skill.Tool
	b, _ := json.Marshal(u.D.Tools(agentPrincipal(agent)))
	_ = json.Unmarshal(b, &tools)
	return skill.Generate(tools)
}

func (u *UI) agentPageWith(r *http.Request, extra map[string]any) map[string]any {
	_, d, err := u.agentPage(r)
	for k, v := range extra {
		d[k] = v
	}
	if err != nil {
		d["error"] = err.Error()
	}
	d["page"] = "agent"
	return d
}

func (u *UI) token(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	if _, ok := u.D.Config.Current().Agents[name]; !ok {
		return redirectErr("/agents", fmt.Errorf("unknown agent %s", name))
	}
	tok, err := u.D.State.IssueToken(name, r.PostForm.Get("label"), "agent")
	if err != nil {
		return redirectErr("/agents/"+url.PathEscape(name), err)
	}
	extra := map[string]any{"NewToken": tok}
	if c := r.PostForm.Get("client"); c != "" {
		if _, ok := agentsetup.Find(c); ok {
			p, snippet := u.Clients.Snippet(c, u.bin(), tok)
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
	return redirect("/agents/"+url.PathEscape(name), fmt.Sprintf("Отозвано токенов: %d. Агент %s больше не может обращаться к gate4.ai, пока вы не подключите его снова.", n, name))
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
	return redirect("/agents", "Агент "+name+" удалён, его токены отозваны.")
}

func (u *UI) setAgentProfile(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	cur := u.D.Config.Current().Agents[name]
	if err := u.edit("agent "+name+" profile", func(c *config.Config) error {
		return c.SetAgent(name, r.PostForm.Get("profile"), cur.Description)
	}); err != nil {
		return redirectErr("/agents/"+url.PathEscape(name), err)
	}
	return redirect("/agents/"+url.PathEscape(name), "Профиль изменён")
}

// connectClient — автоподключение: токен + запись в конфигурацию клиента.
func (u *UI) connectClient(w http.ResponseWriter, r *http.Request) map[string]any {
	name, client := r.PathValue("name"), r.PostForm.Get("client")
	back := "/agents/" + url.PathEscape(name) + "?client=" + url.QueryEscape(client)
	c, ok := agentsetup.Find(client)
	if !ok {
		return redirectErr(back, fmt.Errorf("unknown client %q", client))
	}
	if _, ok := u.D.Config.Current().Agents[name]; !ok {
		return redirectErr("/agents", fmt.Errorf("unknown agent %s", name))
	}
	_, _ = u.D.State.RevokeLabel(name, "auto:"+client) // прежний токен этого клиента
	tok, err := u.D.State.IssueToken(name, "auto:"+client, "agent")
	if err != nil {
		return redirectErr(back, err)
	}
	res, err := u.Clients.Connect(client, u.bin(), tok, u.skillMD(name))
	if err != nil {
		_, _ = u.D.State.RevokeLabel(name, "auto:"+client)
		return redirectErr(back, err)
	}
	msg := fmt.Sprintf("%s подключён: сервер gate4ai записан в %s", c.Title, res.Path)
	if res.Backup != "" {
		msg += " (прежняя версия — " + res.Backup + ")"
	}
	if len(res.Notes) > 0 {
		msg += "; " + strings.Join(res.Notes, "; ")
	}
	return redirect(back, msg+". "+c.Restart)
}

func (u *UI) disconnectClient(w http.ResponseWriter, r *http.Request) map[string]any {
	name, client := r.PathValue("name"), r.PostForm.Get("client")
	back := "/agents/" + url.PathEscape(name)
	res, err := u.Clients.Disconnect(client)
	if err != nil {
		return redirectErr(back, err)
	}
	n, _ := u.D.State.RevokeLabel(name, "auto:"+client)
	return redirect(back, fmt.Sprintf("Сервер gate4ai убран из %s, токенов отозвано: %d", res.Path, n))
}

// ---- Коннекторы ----

// storeItem — карточка магазина.
type storeItem struct {
	Name, Description, Publisher, Trust, License, Repo, Version string
	Capabilities                                                []string
	Installed                                                   string // установленная версия
	Loaded, Update, Revoked                                     bool
	Profiles                                                    []string
}

func (u *UI) storeItems(ctx context.Context, refresh bool) ([]storeItem, string) {
	opts := u.D.StoreOptions()
	ix := opts.CachedIndex()
	var ierr string
	if (ix == nil || refresh) && opts.Registry != "" {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		var err error
		if nix, err2 := opts.RefreshIndex(cctx); err2 == nil {
			ix = nix
		} else {
			err = err2
		}
		cancel()
		if err != nil {
			ierr = err.Error()
		}
	} else if opts.Registry == "" {
		ierr = "the store is not configured: set the registry address in Settings"
	}
	inst := u.installed()
	cfg := u.D.Config.Current()
	profilesOf := func(conn string) []string {
		var out []string
		for n, p := range cfg.Profiles {
			if p.HasConnector(conn) {
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return out
	}
	seen := map[string]bool{}
	var items []storeItem
	if ix != nil {
		for name, e := range ix.Connectors {
			it := storeItem{Name: name, Description: e.Description, Publisher: e.Publisher, License: e.License, Repo: e.Repo}
			if p, ok := ix.Publishers[e.Publisher]; ok {
				it.Trust = p.Trust
			}
			if _, rel, err := ix.Resolve(name, ""); err == nil {
				it.Version, it.Capabilities = rel.Version, rel.Capabilities
			}
			if in, ok := inst[name]; ok {
				it.Installed = in.Manifest.Version
				_, it.Loaded = u.D.Registry.Get(name)
				it.Update = it.Version != "" && it.Version != it.Installed
				it.Revoked, _ = ix.IsRevoked(name, it.Installed)
			}
			it.Profiles = profilesOf(name)
			seen[name] = true
			items = append(items, it)
		}
	}
	for name, in := range inst {
		if seen[name] {
			continue
		}
		_, loaded := u.D.Registry.Get(name)
		items = append(items, storeItem{Name: name, Description: in.Manifest.Description, Publisher: in.Info.Publisher, Trust: in.Info.Trust,
			Installed: in.Manifest.Version, Version: in.Manifest.Version, Loaded: loaded, Capabilities: in.Info.Capabilities, Profiles: profilesOf(name)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, ierr
}

func (u *UI) connectorsPage(r *http.Request) (string, map[string]any, error) {
	items, ierr := u.storeItems(r.Context(), false)
	cfg := u.D.Config.Current()
	profiles := make([]string, 0, len(cfg.Profiles))
	for p := range cfg.Profiles {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	data := map[string]any{"Items": items, "IndexError": ierr, "Profiles": profiles, "Registry": cfg.Store.Registry}
	if src := r.URL.Query().Get("install"); src != "" {
		plan, err := u.D.PrepareConnector(r.Context(), src)
		if err != nil {
			return "connectors", data, fmt.Errorf("%s: %v", src, err)
		}
		data["Plan"] = plan
	}
	return "connectors", data, nil
}

func (u *UI) installConnector(w http.ResponseWriter, r *http.Request) map[string]any {
	src := r.PostForm.Get("source")
	consent := r.PostForm["consent"]
	if consent == nil {
		consent = []string{}
	}
	info, plan, warn, err := u.D.InstallConnector(r.Context(), src, consent)
	if errors.Is(err, store.ErrConsent) {
		return redirect("/connectors?install="+url.QueryEscape(src), "Возможности коннектора изменились — подтвердите заново.")
	}
	if err != nil {
		return redirectErr("/connectors", err)
	}
	_ = plan
	profs := r.PostForm["profiles"]
	if len(profs) > 0 {
		if err := u.edit("connector "+info.Connector+" for profiles", func(c *config.Config) error {
			for _, p := range profs {
				if err := c.SetProfileConnector(p, info.Connector, true); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			warn = strings.TrimSpace(warn + " " + err.Error())
		}
	}
	msg := fmt.Sprintf("Коннектор %s %s установлен.", info.Connector, info.Version)
	if warn != "" {
		msg += " Внимание: " + warn
	}
	return redirect("/connectors/"+url.PathEscape(info.Connector), msg+" Настройте его ниже.")
}

func (u *UI) refreshIndex(w http.ResponseWriter, r *http.Request) map[string]any {
	if _, ierr := u.storeItems(r.Context(), true); ierr != "" {
		return redirectErr("/connectors", errors.New(ierr))
	}
	return redirect("/connectors", "Каталог обновлён")
}

func (u *UI) connectorPage(r *http.Request) (string, map[string]any, error) {
	name := r.PathValue("name")
	c, ok := u.D.Registry.Get(name)
	if !ok {
		items, ierr := u.storeItems(r.Context(), false)
		return "connectors", map[string]any{"Items": items, "IndexError": ierr}, fmt.Errorf("connector %q is not installed", name)
	}
	m := c.Manifest()
	cfg := u.D.Config.Current()
	fields := settingFields(m, cfg.Connectors[name])
	for i, f := range fields {
		if f.Kind == fieldSecret && f.Alias != "" {
			_, err := u.D.Vault.Get(f.Alias)
			fields[i].HasSecret = err == nil
		}
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for p := range cfg.Profiles {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	in := u.installed()[name]
	data := map[string]any{"Name": name, "Manifest": m, "Fields": fields, "Profiles": profiles, "Config": cfg, "Info": in.Info,
		"Builtin": in.Name == ""}
	if name == "email" {
		data["Providers"] = mailProviders
	}
	return "connector", data, nil
}

func (u *UI) saveConnectorSettings(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	back := "/connectors/" + url.PathEscape(name)
	c, ok := u.D.Registry.Get(name)
	if !ok {
		return redirectErr("/connectors", fmt.Errorf("connector %q is not installed", name))
	}
	f := r.PostForm
	cur := map[string]any{}
	for k, v := range u.D.Config.Current().Connectors[name] {
		cur[k] = v
	}
	// Почтовый сервис: серверы по умолчанию.
	if p := f.Get("provider"); p != "" {
		for _, mp := range mailProviders {
			if mp.ID == p {
				if f.Get("imap.host") == "" {
					f.Set("imap.host", mp.IMAP)
				}
				if f.Get("smtp.host") == "" {
					f.Set("smtp.host", mp.SMTP)
				}
				if f.Get("imap.username") == "" {
					f.Set("imap.username", f.Get("address"))
				}
			}
		}
	}
	for _, fl := range settingFields(c.Manifest(), cur) {
		raw := f.Get(fl.Key)
		if fl.Kind == fieldSecret {
			if strings.TrimSpace(raw) == "" {
				continue // пусто — оставить как есть
			}
			alias := fl.Alias
			if alias == "" {
				alias = "@" + name + "." + fl.Key
			}
			if err := u.D.Vault.Set(alias, raw); err != nil {
				return redirectErr(back, err)
			}
			setPath(cur, fl.Key, alias)
			continue
		}
		v, err := fieldValue(fl, raw)
		if err != nil {
			return redirectErr(back, err)
		}
		setPath(cur, fl.Key, v)
	}
	if err := u.edit("connector "+name+" settings", func(c *config.Config) error {
		c.SetConnectorSettings(name, cur)
		return nil
	}); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Настройки сохранены")
}

func (u *UI) saveConnectorProfiles(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	on := r.PostForm["profiles"]
	if err := u.edit("connector "+name+" profiles", func(c *config.Config) error {
		for p := range c.Profiles {
			if err := c.SetProfileConnector(p, name, slices.Contains(on, p)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return redirectErr("/connectors/"+url.PathEscape(name), err)
	}
	return redirect("/connectors/"+url.PathEscape(name), "Доступ сохранён")
}

func (u *UI) removeConnector(w http.ResponseWriter, r *http.Request) map[string]any {
	name := r.PathValue("name")
	// Сначала убираем коннектор из профилей и правил: иначе политика ссылается на
	// действия, которых нет.
	_ = u.edit("remove connector "+name, func(c *config.Config) error {
		for pn, p := range c.Profiles {
			_ = c.SetProfileConnector(pn, name, false)
			p = c.Profiles[pn]
			p.Rules = slices.DeleteFunc(p.Rules, func(x config.Rule) bool { return strings.HasPrefix(x.Action, name+".") })
			p.Limits = slices.DeleteFunc(p.Limits, func(x config.Limit) bool { return strings.HasPrefix(x.Action, name+".") })
			c.Profiles[pn] = p
		}
		return nil
	})
	warn, err := u.D.RemoveConnector(name)
	if err != nil {
		return redirectErr("/connectors/"+url.PathEscape(name), err)
	}
	return redirect("/connectors", strings.TrimSpace("Коннектор "+name+" удалён. "+warn))
}

// ---- Правила ----

func (u *UI) policyPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	profiles := make([]string, 0, len(cfg.Profiles))
	for p := range cfg.Profiles {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	prof := r.URL.Query().Get("profile")
	if _, ok := cfg.Profiles[prof]; !ok && len(profiles) > 0 {
		prof = profiles[0]
		// Профиль первого агента — самый вероятный.
		names := make([]string, 0, len(cfg.Agents))
		for a := range cfg.Agents {
			names = append(names, a)
		}
		sort.Strings(names)
		if len(names) > 0 {
			prof = cfg.Agents[names[0]].Profile
		}
	}
	p := cfg.Profiles[prof]
	var agents []string
	for a, ag := range cfg.Agents {
		if ag.Profile == prof {
			agents = append(agents, a)
		}
	}
	sort.Strings(agents)
	// Что будет по умолчанию с каждым действием профиля.
	type actRow struct {
		Tool    daemon.Tool
		Default string
		Rules   []string
	}
	var acts []actRow
	for _, t := range u.tools() {
		if !p.HasConnector(t.Connector) {
			continue
		}
		row := actRow{Tool: t, Default: defaultEffect(t.Class)}
		for _, rl := range p.Rules {
			if ok, _ := path.Match(rl.Action, t.Name); ok || rl.Action == t.Name || rl.Action == "*" {
				row.Rules = append(row.Rules, rl.ID)
			}
		}
		acts = append(acts, row)
	}
	_, n := u.D.Config.CurrentRaw()
	vs, _ := u.D.Config.Versions()
	if len(vs) > 15 {
		vs = vs[len(vs)-15:]
	}
	slices.Reverse(vs)
	data := map[string]any{"Profiles": profiles, "Profile": prof, "P": p, "Agents": agents, "Acts": acts, "Connectors": u.connectorNames(),
		"Lists": cfg.Lists, "Version": n, "Versions": vs, "AllAgents": cfg.Agents, "Tools": u.tools()}
	u.simData(r.URL.Query().Get("sim"), data)
	return "policy", data, nil
}

func (u *UI) simData(action string, data map[string]any) {
	tools := u.tools()
	if action == "" && len(tools) > 0 {
		action = tools[0].Name
	}
	if t, ok := toolSchema(tools, action); ok {
		data["SimTool"] = t
	}
	data["SimAction"] = action
}

func (u *UI) yamlPage(r *http.Request) (string, map[string]any, error) {
	return "policy_yaml", u.yamlData(""), nil
}

func (u *UI) yamlData(y string) map[string]any {
	raw, n := u.D.Config.CurrentRaw()
	if y == "" {
		y = string(raw)
	}
	vs, _ := u.D.Config.Versions()
	slices.Reverse(vs)
	return map[string]any{"YAML": y, "Version": n, "Versions": vs, "page": "policy_yaml"}
}

func (u *UI) savePolicy(w http.ResponseWriter, r *http.Request) map[string]any {
	y := r.PostForm.Get("yaml")
	v, err := u.D.Config.Put(owner, []byte(y), r.PostForm.Get("comment"))
	if err != nil {
		d := u.yamlData(y)
		d["error"] = err.Error()
		return d
	}
	return redirect("/policy/yaml", "Сохранена версия "+strconv.Itoa(v.N))
}

func (u *UI) rollback(w http.ResponseWriter, r *http.Request) map[string]any {
	n, _ := strconv.Atoi(r.PostForm.Get("version"))
	v, err := u.D.Config.Rollback(owner, n)
	back := or(r.PostForm.Get("back"), "/policy")
	if err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, fmt.Sprintf("Возвращена версия %d (сохранена как %d)", n, v.N))
}

// simulate — «что будет, если агент попробует…».
func (u *UI) simulate(w http.ResponseWriter, r *http.Request) map[string]any {
	req := &http.Request{URL: &url.URL{RawQuery: url.Values{"profile": {r.PostForm.Get("profile")}, "sim": {r.PostForm.Get("action")}}.Encode()}}
	_, d, _ := u.policyPage(req)
	d["page"] = "policy"
	agent, action := r.PostForm.Get("agent"), r.PostForm.Get("action")
	t, ok := toolSchema(u.tools(), action)
	if !ok {
		d["error"] = "unknown action " + action
		return d
	}
	var args map[string]any
	var err error
	if raw := strings.TrimSpace(r.PostForm.Get("args")); raw != "" {
		err = json.Unmarshal([]byte(raw), &args)
	} else {
		args, err = argsFromForm(t, r.PostForm.Get)
	}
	if err != nil {
		d["error"] = "arguments: " + err.Error()
		return d
	}
	_, act, _ := u.D.Registry.Lookup(action)
	nargs, err := act.Normalize(args)
	if err != nil {
		d["error"] = err.Error()
		return d
	}
	dec := u.D.Pipeline.Engine().Decide(policy.Request{Agent: agent, Action: action, Class: act.Class, Args: nargs, Now: u.Now()}, u.D.State.Counters())
	sa := map[string]string{}
	for k, v := range r.PostForm {
		if strings.HasPrefix(k, "arg.") && len(v) > 0 {
			sa[k] = v[0]
		}
	}
	d["Sim"], d["SimAgent"], d["SimArgs"] = dec, agent, sa
	return d
}

// ruleForm — данные формы правила.
type ruleForm struct {
	Profile, ID, Action, Effect, Reason, When string
	Conds                                     []Cond
}

func (u *UI) rulePage(r *http.Request) (string, map[string]any, error) {
	q := r.URL.Query()
	cfg := u.D.Config.Current()
	f := ruleForm{Profile: q.Get("profile"), ID: q.Get("id"), Action: q.Get("action"), Effect: or(q.Get("effect"), "allow")}
	var note string
	// Правка существующего правила.
	if f.ID != "" {
		for _, rl := range cfg.Profiles[f.Profile].Rules {
			if rl.ID == f.ID {
				f.Action, f.Effect, f.Reason, f.When = rl.Action, rl.Effect, rl.Reason, rl.When
			}
		}
	}
	// На основе запроса подтверждения или квитанции журнала.
	var agent string
	var args map[string]any
	if id := q.Get("approval"); id != "" {
		if a, err := u.D.State.GetApproval(id); err == nil {
			agent, f.Action, args = a.Agent, a.Action, a.Args
			note = "Правило на основе запроса " + a.Agent + " → " + a.Action + ". Уточните условия, чтобы не разрешить лишнего."
		}
	}
	if h := q.Get("receipt"); h != "" {
		if e, err := u.D.Journal.Find(h); err == nil {
			agent, f.Action, args = e.Receipt.Agent, e.Receipt.Action, e.Receipt.Args
			note = "Правило на основе квитанции #" + audit.Short(e.Hash) + ". Уточните условия, чтобы не разрешить лишнего."
		}
	}
	if agent != "" && f.Profile == "" {
		f.Profile = cfg.Agents[agent].Profile
	}
	if f.Profile == "" {
		for p := range cfg.Profiles {
			f.Profile = p
			break
		}
	}
	tools := u.tools()
	t, hasTool := toolSchema(tools, f.Action)
	if args != nil && hasTool {
		f.Conds = suggestConds(t, args)
	}
	for len(f.Conds) < 3 {
		f.Conds = append(f.Conds, Cond{})
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for p := range cfg.Profiles {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	data := map[string]any{"F": f, "Tools": tools, "Profiles": profiles, "Kinds": condKinds, "Lists": cfg.Lists, "Note": note}
	if hasTool {
		data["Tool"], data["Args"] = t, actionArgs(t)
	}
	return "rule", data, nil
}

// suggestConds — условия по аргументам конкретного вызова.
func suggestConds(t daemon.Tool, args map[string]any) []Cond {
	var out []Cond
	for _, a := range actionArgs(t) {
		v, ok := args[a.Name]
		if !ok {
			continue
		}
		s := fmt.Sprint(v)
		switch {
		case a.Format == "path":
			out = append(out, Cond{Kind: "in_folder", Arg: a.Name, Value: path.Dir(strings.ReplaceAll(s, `\`, "/"))})
		case a.Format == "url":
			if pu, err := url.Parse(s); err == nil && pu.Host != "" {
				out = append(out, Cond{Kind: "domain", Arg: a.Name, Value: pu.Hostname()})
			}
		case a.Array && a.Format == "email":
			out = append(out, Cond{Kind: "in_list", Arg: a.Name, Value: "contacts_trusted"})
		case a.Type == "number" || a.Type == "integer":
			out = append(out, Cond{Kind: "lte", Arg: a.Name, Value: s})
		}
	}
	return out
}

func (u *UI) saveRule(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	form := ruleForm{Profile: f.Get("profile"), ID: strings.TrimSpace(f.Get("id")), Action: f.Get("action"), Effect: f.Get("effect"), Reason: strings.TrimSpace(f.Get("reason"))}
	back := "/policy/rule?" + url.Values{"profile": {form.Profile}, "action": {form.Action}, "id": {form.ID}, "effect": {form.Effect}}.Encode()
	t, ok := toolSchema(u.tools(), form.Action)
	var args []argInfo
	if ok {
		args = actionArgs(t)
	}
	var conds []Cond
	kinds, argsF, vals := f["cond_kind"], f["cond_arg"], f["cond_value"]
	for i := range kinds {
		c := Cond{Kind: kinds[i]}
		if i < len(argsF) {
			c.Arg = argsF[i]
		}
		if i < len(vals) {
			c.Value = vals[i]
		}
		c.Not = f.Get(fmt.Sprintf("cond_not_%d", i)) == "1"
		conds = append(conds, c)
	}
	when, err := BuildWhen(conds, args)
	if err != nil {
		return redirectErr(back, err)
	}
	// Новые списки из блоков «из списка»: элементы — в поле list_items_<i>.
	newLists := map[string][]string{}
	for i, c := range conds {
		if c.Kind == "in_list" {
			if items := splitItems(f.Get(fmt.Sprintf("list_items_%d", i))); len(items) > 0 {
				newLists[c.Value] = items
			}
		}
	}
	if raw := strings.TrimSpace(f.Get("when")); raw != "" {
		if when != "" {
			when = "(" + raw + ") && " + when
		} else {
			when = raw
		}
	}
	err = u.edit("rule in "+form.Profile, func(c *config.Config) error {
		for name, items := range newLists {
			if err := c.SetList(name, append(c.Lists[name], items...)); err != nil {
				return err
			}
		}
		id := form.ID
		if id == "" {
			id = c.Profiles[form.Profile].UniqueRuleID(strings.NewReplacer(".", "-", "*", "all").Replace(form.Action) + "-" + form.Effect)
		}
		// Действие могло ещё не быть доступно профилю.
		if conn := strings.SplitN(form.Action, ".", 2)[0]; conn != "*" {
			_ = c.SetProfileConnector(form.Profile, conn, true)
		}
		return c.PutRule(form.Profile, config.Rule{ID: id, Action: form.Action, When: when, Effect: form.Effect, Reason: form.Reason})
	})
	if err != nil {
		return redirectErr(back, err)
	}
	return redirect("/policy?profile="+url.QueryEscape(form.Profile), "Правило сохранено")
}

func (u *UI) deleteRule(w http.ResponseWriter, r *http.Request) map[string]any {
	p, id := r.PostForm.Get("profile"), r.PostForm.Get("id")
	back := "/policy?profile=" + url.QueryEscape(p)
	if err := u.edit("remove rule "+id, func(c *config.Config) error { return c.RemoveRule(p, id) }); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Правило "+id+" удалено")
}

func (u *UI) moveRule(w http.ResponseWriter, r *http.Request) map[string]any {
	p, id := r.PostForm.Get("profile"), r.PostForm.Get("id")
	delta, _ := strconv.Atoi(r.PostForm.Get("delta"))
	back := "/policy?profile=" + url.QueryEscape(p)
	if err := u.edit("move rule "+id, func(c *config.Config) error { return c.MoveRule(p, id, delta) }); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "")
}

// templateView — шаблон с доступностью для профиля.
type templateView struct {
	Template
	Missing []string
}

func (u *UI) templatesPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	prof := r.URL.Query().Get("profile")
	tools := u.tools()
	var tv []templateView
	for _, t := range Templates {
		v := templateView{Template: t}
		for _, need := range t.Needs {
			if _, ok := toolSchema(tools, need); !ok {
				v.Missing = append(v.Missing, strings.SplitN(need, ".", 2)[0])
			}
		}
		tv = append(tv, v)
	}
	profiles := make([]string, 0, len(cfg.Profiles))
	for p := range cfg.Profiles {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	return "templates", map[string]any{"Templates": tv, "Profile": prof, "Profiles": profiles}, nil
}

func (u *UI) applyTemplate(w http.ResponseWriter, r *http.Request) map[string]any {
	prof, id := r.PostForm.Get("profile"), r.PostForm.Get("template")
	t, ok := findTemplate(id)
	if !ok {
		return redirectErr("/policy/templates", fmt.Errorf("unknown template %q", id))
	}
	params := map[string]string{}
	for _, p := range t.Params {
		params[p.Name] = r.PostForm.Get(p.Name)
	}
	if err := u.edit("template "+id+" for "+prof, func(c *config.Config) error {
		if _, ok := c.Profiles[prof]; !ok {
			return fmt.Errorf("profile %q not found", prof)
		}
		return t.Apply(c, prof, params, u.tools())
	}); err != nil {
		return redirectErr("/policy/templates?profile="+url.QueryEscape(prof), err)
	}
	return redirect("/policy?profile="+url.QueryEscape(prof), "Шаблон «"+t.Title+"» применён")
}

func (u *UI) createProfile(w http.ResponseWriter, r *http.Request) map[string]any {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if err := u.edit("add profile "+name, func(c *config.Config) error {
		if _, ok := c.Profiles[name]; ok {
			return fmt.Errorf("profile %q already exists", name)
		}
		return c.EnsureProfile(name)
	}); err != nil {
		return redirectErr("/policy", err)
	}
	return redirect("/policy?profile="+url.QueryEscape(name), "Профиль "+name+" создан")
}

func (u *UI) saveProfileConnectors(w http.ResponseWriter, r *http.Request) map[string]any {
	prof := r.PostForm.Get("profile")
	on := r.PostForm["connectors"]
	back := "/policy?profile=" + url.QueryEscape(prof)
	if err := u.edit("profile "+prof+" connectors", func(c *config.Config) error {
		for _, conn := range u.connectorNames() {
			if err := c.SetProfileConnector(prof, conn, slices.Contains(on, conn)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Доступ к коннекторам сохранён")
}

func (u *UI) saveList(w http.ResponseWriter, r *http.Request) map[string]any {
	name := strings.TrimSpace(r.PostForm.Get("name"))
	items := splitItems(r.PostForm.Get("items"))
	back := or(r.PostForm.Get("back"), "/policy") + "#lists"
	if err := u.edit("list "+name, func(c *config.Config) error { return c.SetList(name, items) }); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Список «"+name+"» сохранён")
}

// ---- Журнал ----

func (u *UI) journal(r *http.Request) (string, map[string]any, error) {
	q := r.URL.Query()
	es, err := u.D.Journal.List(audit.Filter{Agent: q.Get("agent"), Action: q.Get("action"), Effect: q.Get("effect"), Limit: 200, Desc: true})
	verr := u.D.Journal.VerifyChain()
	chain := "ok"
	if verr != nil {
		chain = verr.Error()
	}
	return "journal", map[string]any{"Entries": es, "Q": q, "Chain": chain}, err
}

func (u *UI) receipt(r *http.Request) (string, map[string]any, error) {
	e, err := u.D.Journal.Find(r.PathValue("hash"))
	return "receipt", map[string]any{"Entry": e}, err
}

// ---- Секреты ----

func (u *UI) vaultPage(r *http.Request) (string, map[string]any, error) {
	raw, _ := u.D.Config.CurrentRaw()
	used := map[string]bool{}
	for _, a := range u.D.Vault.Aliases() {
		if strings.Contains(string(raw), `"`+a+`"`) || strings.Contains(string(raw), " "+a+"\n") || strings.Contains(string(raw), " "+a+",") {
			used[a] = true
		}
	}
	return "vault", map[string]any{"Aliases": u.D.Vault.Aliases(), "Used": used}, nil
}

func (u *UI) setSecret(w http.ResponseWriter, r *http.Request) map[string]any {
	if err := u.D.Vault.Set(r.PostForm.Get("alias"), r.PostForm.Get("value")); err != nil {
		return redirectErr("/vault", err)
	}
	return redirect("/vault", "Сохранён "+r.PostForm.Get("alias"))
}

func (u *UI) deleteSecret(w http.ResponseWriter, r *http.Request) map[string]any {
	if err := u.D.Vault.Delete(r.PostForm.Get("alias")); err != nil {
		return redirectErr("/vault", err)
	}
	return redirect("/vault", "Удалён "+r.PostForm.Get("alias"))
}

// ---- Настройки ----

// Autostart — автозапуск daemon при входе (подменяется в тестах).
type Autostart interface {
	State() map[string]any
	Enable(bin string) error
	Disable() error
}

type systemAutostart struct{}

func (systemAutostart) State() map[string]any {
	h, _ := os.UserHomeDir()
	return autostart.State(h)
}

func (systemAutostart) Enable(bin string) error {
	h, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	_, err = autostart.Install(autostart.Spec{Bin: bin, Tray: autostart.FindTray(bin), Log: paths.DaemonLog(), Home: h, NoStart: true})
	return err
}

func (systemAutostart) Disable() error {
	h, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	_, _, err = autostart.Uninstall(h, true)
	return err
}

func (u *UI) settingsPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	data := map[string]any{"Relay": u.relay(), "RelayDefault": relayclient.DefaultURL, "PublicURL": u.D.PublicURL(), "Config": cfg,
		"Timeout": config.FormatDuration(cfg.Approvals.Timeout.D()), "Version": version.Version, "Home": u.D.Home, "Log": paths.DaemonLog(),
		"Retention": map[string]string{"receipts": config.FormatDuration(cfg.Retention.Receipts.D()), "denied": config.FormatDuration(cfg.Retention.Denied.D()),
			"evidence_other": config.FormatDuration(cfg.Retention.EvidenceOther.D()), "evidence_money": config.FormatDuration(cfg.Retention.EvidenceMoney.D())}}
	if u.Autostart != nil {
		data["Autostart"] = u.Autostart.State()
	}
	if link := r.URL.Query().Get("link"); link == "1" {
		if l, err := u.RemoteLoginURL(); err == nil {
			data["RemoteLink"] = l
			if code, err := qr.Encode(l, qr.M); err == nil {
				data["RemoteQR"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
			}
		} else {
			data["RemoteLinkError"] = err.Error()
		}
	}
	return "settings", data, nil
}

func (u *UI) registerRelay(w http.ResponseWriter, r *http.Request) map[string]any {
	addr := strings.TrimRight(strings.TrimSpace(or(r.PostForm.Get("url"), relayclient.DefaultURL)), "/")
	if !strings.HasPrefix(addr, "https://") {
		addr = "https://" + strings.TrimPrefix(addr, "http://")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := relayclient.RegisterDevice(ctx, u.D, owner, addr, "", false)
	if err != nil {
		return redirectErr("/settings#relay", fmt.Errorf("relay %s: %v", addr, err))
	}
	return redirect("/settings#relay", fmt.Sprintf("Устройство подключено к relay: %v. Адрес для облачных агентов: %v", res["host"], res["mcp_url"]))
}

func (u *UI) toggleAutostart(w http.ResponseWriter, r *http.Request) map[string]any {
	if u.Autostart == nil {
		return redirectErr("/settings#autostart", errors.New("autostart is not available"))
	}
	var err error
	on := r.PostForm.Get("on") == "1"
	if on {
		err = u.Autostart.Enable(u.bin())
	} else {
		err = u.Autostart.Disable()
	}
	if err != nil {
		return redirectErr("/settings#autostart", err)
	}
	return redirect("/settings#autostart", map[bool]string{true: "Автозапуск включён: gate4.ai будет запускаться при входе в систему.",
		false: "Автозапуск выключен. Работающий daemon продолжает работать до выхода из системы."}[on])
}

func (u *UI) saveGeneral(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	err := u.edit("settings", func(c *config.Config) error {
		if f.Has("registry") {
			c.Store.Registry = strings.TrimSpace(f.Get("registry"))
			c.Store.RegistryKeys = splitItems(f.Get("registry_keys"))
		}
		for _, k := range []string{"receipts", "denied", "evidence_other", "evidence_money"} {
			if !f.Has("ret_" + k) {
				continue
			}
			var d time.Duration
			if v := strings.TrimSpace(f.Get("ret_" + k)); v != "" && v != "0s" {
				var err error
				if d, err = config.ParseDuration(v); err != nil {
					return fmt.Errorf("%s: %v", k, err)
				}
			}
			dd := config.Duration(d)
			switch k {
			case "receipts":
				c.Retention.Receipts = dd
			case "denied":
				c.Retention.Denied = dd
			case "evidence_other":
				c.Retention.EvidenceOther = dd
			case "evidence_money":
				c.Retention.EvidenceMoney = dd
			}
		}
		return nil
	})
	if err != nil {
		return redirectErr("/settings", err)
	}
	return redirect("/settings", "Настройки сохранены")
}

func (u *UI) remoteLink(w http.ResponseWriter, r *http.Request) map[string]any {
	return redirect("/settings?link=1#remote", "")
}

// serviceWorker — Service Worker для браузерного push (область — база Web UI).
func serviceWorker(w http.ResponseWriter, r *http.Request) {
	b, _ := staticFS.ReadFile("static/sw.js")
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(b)
}
