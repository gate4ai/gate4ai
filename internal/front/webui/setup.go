// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/front/agentsetup"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/sdk-go/access"
)

// Мастер первого запуска (/setup): 1) какой ассистент, 2) что ему можно —
// папки, сайты и решения по группам действий, 3) проверка — живая лента с
// первым подтверждением, каналы на телефоне. Свежая установка без агентов
// открывает его сама, пока владелец его не пропустил (cookie setupCookie).

const setupCookie = "g4a_setup"

// classOrder — порядок групп действий.
var classOrder = []string{"read", "write", "external", "money"}

// groupTitles — подписи групп действий официальных коннекторов (иначе classVerbs).
var groupTitles = map[string]map[string]string{
	"files":   {"read": "Read files", "write": "Create and change files"},
	"browser": {"read": "Read pages", "write": "Click, type and sign in"},
}

// groupWarns — предупреждения к группам действий: что значит «разрешать».
// Самонастройка: правило allow на change_policy не действует (policy.OwnerOnly) —
// правку подтверждает владелец.
var groupWarns = map[string]map[string]string{
	daemon.SelfConnector: {"write": "A change of gate4.ai settings by an agent — sites, rules, accounts, connectors — always asks you: “Allow” has no effect here, “Deny” forbids asking."},
}

// groupView — группа действий коннектора одного класса и решение для неё в профиле.
type groupView struct {
	Connector, Class, Title string
	Effect                  string   // allow | ask | deny; "mixed" — разные решения или правила с условием
	Actions                 []string // полные имена
	Scoped                  bool     // решение действует в выбранных папках (правила с условием «внутри папки»)
	Warn                    string   // что значит «разрешать» для этой группы (groupWarns)
}

// foldersOnly — условие правила состоит только из «путь внутри папки …» через «или».
var foldersOnly = regexp.MustCompile(`^glob\(path, "[^"]+"\)( \|\| glob\(path, "[^"]+"\))*$`)

// groupDecision — решение профиля для одного действия в группе: правила точно на
// это действие без условия или с условием «внутри выбранных папок»; иначе mixed.
func groupDecision(p config.Profile, action, class string) (string, bool) {
	var effs []string
	scoped := false
	for _, r := range p.Rules {
		if r.Action != action {
			continue
		}
		w := strings.TrimSpace(r.When)
		if w != "" && !foldersOnly.MatchString(w) {
			return decisionMixed, false
		}
		scoped = scoped || w != ""
		effs = append(effs, r.Effect)
	}
	if len(effs) == 0 {
		return defaultEffect(class), false
	}
	for _, e := range effs[1:] {
		if e != effs[0] {
			return decisionMixed, false
		}
	}
	return effs[0], scoped
}

// groups — группы действий коннектора conn с решениями профиля prof.
func (u *UI) groups(conn string, prof config.Profile) []groupView {
	c, ok := u.D.Registry.Get(conn)
	if !ok {
		return nil
	}
	by := map[string]*groupView{}
	for _, a := range c.Manifest().Actions {
		if a.ServiceOnly {
			continue
		}
		g, ok := by[a.Class]
		if !ok {
			g = &groupView{Connector: conn, Class: a.Class, Title: or(groupTitles[conn][a.Class], classVerbs[a.Class]), Warn: groupWarns[conn][a.Class]}
			by[a.Class] = g
		}
		full := conn + "." + a.Name
		g.Actions = append(g.Actions, full)
		eff, scoped := groupDecision(prof, full, a.Class)
		g.Scoped = g.Scoped || scoped
		switch {
		case len(g.Actions) == 1:
			g.Effect = eff
		case g.Effect != eff:
			g.Effect = decisionMixed
		}
	}
	var out []groupView
	for _, cl := range classOrder {
		if g, ok := by[cl]; ok {
			out = append(out, *g)
		}
	}
	return out
}

// setGroupRules — одно решение для всех действий группы: правило на каждое
// действие; прежние правила без условия или «в выбранных папках» заменяются,
// папки сохраняются.
func setGroupRules(c *config.Config, profile string, actions []string, effect string) error {
	p := c.Profiles[profile]
	when := map[string]string{}
	p.Rules = slices.DeleteFunc(p.Rules, func(x config.Rule) bool {
		w := strings.TrimSpace(x.When)
		if !slices.Contains(actions, x.Action) || (w != "" && !foldersOnly.MatchString(w)) {
			return false
		}
		if w != "" {
			when[x.Action] = w
		}
		return true
	})
	c.Profiles[profile] = p
	for _, a := range actions {
		id := c.Profiles[profile].UniqueRuleID(strings.ReplaceAll(a, ".", "-") + "-" + effect)
		if err := c.PutRule(profile, config.Rule{ID: id, Action: a, When: when[a], Effect: effect}); err != nil {
			return err
		}
	}
	return nil
}

func validEffect(e string) bool {
	return e == config.EffectAllow || e == config.EffectAsk || e == config.EffectDeny
}

// setGroup — решение для группы действий коннектора в профиле (страницы агента и коннектора).
func (u *UI) setGroup(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	prof, conn, class, effect := f.Get("profile"), f.Get("connector"), f.Get("class"), f.Get("effect")
	back := or(f.Get("back"), "/connectors/"+url.PathEscape(conn))
	if !validEffect(effect) {
		return redirectErr(back, M("Unknown decision %q", effect))
	}
	cfg := u.D.Config.Current()
	p, ok := cfg.Profiles[prof]
	if !ok {
		return redirectErr(back, M("Unknown agent permissions %q", prof))
	}
	var acts []string
	var title string
	for _, g := range u.groups(conn, p) {
		if g.Class == class {
			acts, title = g.Actions, g.Title
		}
	}
	if len(acts) == 0 {
		return redirectErr(back, M("%s has no such actions", conn))
	}
	if err := u.edit("decision for "+conn+" "+class+" in "+prof, func(c *config.Config) error {
		if err := c.SetProfileConnector(prof, conn, true); err != nil {
			return err
		}
		return setGroupRules(c, prof, acts, effect)
	}); err != nil {
		return redirectErr(back, err)
	}
	if w := groupWarns[conn][class]; w != "" && effect == config.EffectAllow {
		return redirect(back, M("%s: %s. %s", Word(title), Word(effectWords[effect]), Word(w)))
	}
	return redirect(back, M("%s: %s.", Word(title), Word(effectWords[effect])))
}

// ---- Шаги мастера ----

// setupClient — плитка шага 1.
type setupClient struct {
	ID, Title, Note string
	Found, Cloud    bool
}

func (u *UI) setupClients() []setupClient {
	var found, other []setupClient
	for _, c := range agentsetup.Clients {
		sc := setupClient{ID: c.ID, Title: c.Title, Found: u.Clients.Installed(c.ID)}
		if sc.Found {
			found = append(found, sc)
		} else {
			other = append(other, sc)
		}
	}
	cloud := setupClient{ID: "cloud", Title: "Claude.ai or ChatGPT", Note: "in the browser — needs internet access to this computer", Cloud: true}
	cli := setupClient{ID: "cli", Title: "A script or another agent", Note: "a token and the gate4ai command"}
	// Найденные — сверху; облачный — всегда на виду; остальные — под «Другие».
	return append(append(found, cloud), append(other, cli)...)
}

func defaultAgentName(client string) string {
	if c, ok := agentsetup.Find(client); ok {
		return c.Agent
	}
	switch client {
	case "cloud":
		return "claude-ai"
	case "cli":
		return "my-agent"
	}
	return ""
}

func clientTitle(client string) string {
	if c, ok := agentsetup.Find(client); ok {
		return c.Title
	}
	switch client {
	case "cloud":
		return "Claude.ai / ChatGPT"
	case "cli":
		return "script"
	}
	return client
}

// uniqueAgent — свободное имя агента на основе base.
func uniqueAgent(cfg *config.Config, base string) string {
	name := base
	for i := 2; ; i++ {
		_, a := cfg.Agents[name]
		_, p := cfg.Profiles[name]
		if !a && !p {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func (u *UI) setupPage(r *http.Request) (string, map[string]any, error) {
	q := r.URL.Query()
	clients := u.setupClients()
	client := q.Get("client")
	if client == "" {
		for _, c := range clients {
			if c.Found {
				client = c.ID
				break
			}
		}
	}
	return "setup", map[string]any{"Step": 1, "Clients": clients, "Client": client, "Name": q.Get("name")}, nil
}

// setupAccess — данные шага 2: что можно агенту.
type setupConn struct {
	Name, Title, Desc string
	On                bool
	Groups            []groupView
	Folders, Sites    string // files: папки, browser: сайты
}

func (u *UI) setupAccessPage(r *http.Request) (string, map[string]any, error) {
	l := u.lang(r)
	q := r.URL.Query()
	client := q.Get("client")
	if client == "" {
		return "", map[string]any{"redirect": "/setup"}, nil
	}
	cfg := u.D.Config.Current()
	name := strings.TrimSpace(q.Get("name"))
	if name == "" {
		name = uniqueAgent(cfg, defaultAgentName(client))
	}
	var conns []setupConn
	for _, cn := range u.connectorNames() {
		c, _ := u.D.Registry.Get(cn)
		m := localized(c.Manifest())
		sc := setupConn{Name: cn, Title: m.TitleIn(l), Desc: m.DescriptionIn(l), On: cn == "files" || cn == "browser"}
		// Предложение решений: чтение — само, остальное — спрашивать.
		for _, g := range u.groups(cn, config.Profile{}) {
			g.Effect = defaultEffect(g.Class)
			sc.Groups = append(sc.Groups, g)
		}
		switch cn {
		case "files":
			var folders []string
			for _, f := range cfg.Access.Folders {
				if f.Access != access.Deny {
					folders = append(folders, f.Path)
				}
			}
			if len(folders) == 0 {
				folders = []string{"~/Documents"}
			}
			sc.Folders = strings.Join(folders, "\n")
		case "browser":
			var sites []string
			for _, st := range cfg.Access.Sites {
				if st.Access != access.Deny && st.Domain != "*" {
					sites = append(sites, st.Domain)
				}
			}
			sc.Sites = strings.Join(sites, ", ")
		}
		conns = append(conns, sc)
	}
	auto := false
	if u.Autostart != nil {
		auto, _ = u.Autostart.State()["installed"].(bool)
	}
	return "setup", map[string]any{"Step": 2, "Client": client, "ClientTitle": clientTitle(client), "Name": name, "Conns": conns,
		"Autostart": u.Autostart != nil, "AutostartOn": auto, "Cloud": client == "cloud", "PublicURL": u.D.PublicURL()}, nil
}

// folderWhen — условие «путь внутри одной из папок».
func folderWhen(dirs []string) string {
	parts := make([]string, len(dirs))
	for i, d := range dirs {
		parts[i] = "glob(path, " + celString(strings.TrimSuffix(strings.TrimSuffix(d, "/**"), "/")+"/**") + ")"
	}
	return strings.Join(parts, " || ")
}

// setupSave — шаг 2: агент, его права, папки и сайты, автоподключение клиента,
// адрес в интернете для облачного агента, автозапуск.
func (u *UI) setupSave(w http.ResponseWriter, r *http.Request) map[string]any {
	f := r.PostForm
	client := f.Get("client")
	cfg := u.D.Config.Current()
	name := strings.TrimSpace(f.Get("name"))
	if name == "" {
		name = uniqueAgent(cfg, defaultAgentName(client))
	}
	back := "/setup/access?" + url.Values{"client": {client}, "name": {name}}.Encode()
	if _, ok := cfg.Agents[name]; ok {
		return redirectErr(back, M("An agent named %s already exists — choose another name", name))
	}
	on := f["conn"]
	folders := splitItems(f.Get("folders"))
	sites := splitItems(f.Get("sites"))
	if slices.Contains(on, "files") && len(folders) == 0 {
		return redirectErr(back, M("Choose at least one folder for files, or turn files off"))
	}
	effects := map[string]string{}
	err := u.edit("setup: add agent "+name, func(c *config.Config) error {
		if err := c.EnsureProfile(name); err != nil {
			return err
		}
		for _, cn := range on {
			if !slices.Contains(u.connectorNames(), cn) {
				continue
			}
			if err := c.SetProfileConnector(name, cn, true); err != nil {
				return err
			}
			for _, g := range u.groups(cn, config.Profile{}) {
				eff := f.Get("effect." + cn + "." + g.Class)
				if !validEffect(eff) {
					eff = defaultEffect(g.Class)
				}
				effects[cn+"."+g.Class] = eff
				if cn != "files" && cn != "browser" {
					if err := setGroupRules(c, name, g.Actions, eff); err != nil {
						return err
					}
				}
			}
			// Браузер и Файлы: папки и сайты идут в общие списки доступа с режимом по
			// выбранным решениям; правила на них не пишутся.
			switch cn {
			case "files":
				mode := accessMode(effects["files.read"], effects["files.write"], true)
				for _, d := range folders {
					if err := c.SetFolderAccess(strings.TrimSuffix(strings.TrimSuffix(d, "/**"), "/"), mode); err != nil {
						return err
					}
				}
			case "browser":
				mode := accessMode(effects["browser.read"], effects["browser.write"], false)
				for _, x := range sites {
					if err := c.SetSiteAccess(x, mode); err != nil {
						return err
					}
				}
			}
		}
		return c.SetAgent(name, name, "")
	})
	if err != nil {
		return redirectErr(back, err)
	}
	var notes []Msg
	detail := ""
	if f.Get("autostart") == "1" && u.Autostart != nil {
		if inst, _ := u.Autostart.State()["installed"].(bool); !inst {
			if err := u.Autostart.Enable(u.bin()); err != nil {
				notes = append(notes, M("Autostart was not turned on: %s", err))
			}
		}
	}
	if c, ok := agentsetup.Find(client); ok {
		res, err := u.autoConnect(name, client)
		if err == nil {
			detail = connectDetail(res)
		}
		if err != nil {
			notes = append(notes, M("%s was not connected automatically: %s. Use the manual instructions on the agent page.", c.Title, err))
		}
	}
	if client == "cloud" && u.D.PublicURL() == "" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		_, err := relayclient.RegisterDevice(ctx, u.D, owner, relayclient.DefaultURL, "", false)
		cancel()
		if err != nil {
			notes = append(notes, M("Internet access was not turned on: %s", err))
		}
	}
	to := "/setup/check?agent=" + url.QueryEscape(name) + "&client=" + url.QueryEscape(client)
	if len(notes) > 0 {
		return map[string]any{"redirect": to, "error": msgs(notes), "detail": detail}
	}
	return redirectDetail(to, M("%s is connected to gate4.ai.", clientTitle(client)), detail)
}

// msgs — несколько сообщений одним (каждое переводится отдельно).
type msgs []Msg

func (m msgs) Error() string { return m.in(langEN) }

func (m msgs) in(l string) string {
	parts := make([]string, len(m))
	for i, x := range m {
		parts[i] = x.In(l)
	}
	return strings.Join(parts, "\n")
}

// connectDetail — где записана конфигурация клиента (для «Details»).
func connectDetail(res agentsetup.Result) string {
	d := res.Path
	if res.Backup != "" {
		d += " (backup: " + res.Backup + ")"
	}
	if len(res.Notes) > 0 {
		d += "; " + strings.Join(res.Notes, "; ")
	}
	return d
}

func (u *UI) setupCheckData(r *http.Request) map[string]any {
	l := u.lang(r)
	q := r.URL.Query()
	agent, client := q.Get("agent"), q.Get("client")
	cfg := u.D.Config.Current()
	data := map[string]any{"Step": 3, "Agent": agent, "Client": client, "ClientTitle": clientTitle(client), "Cloud": client == "cloud",
		"PublicURL": u.D.PublicURL(), "Relay": u.relay()}
	if c, ok := agentsetup.Find(client); ok {
		data["Restart"] = c.Restart
	}
	prof := cfg.Profiles[cfg.Agents[agent].Profile]
	// Что попросить агента: дописать строку в файл выбранной папки — это изменение,
	// значит, придёт первое подтверждение.
	switch {
	case prof.HasConnector("files"):
		dir := "~/Documents"
		for _, rl := range prof.Rules {
			if m := folderRe.FindStringSubmatch(rl.When); m != nil && rl.Effect != config.EffectDeny {
				dir = m[1]
				break
			}
		}
		data["Prompt"] = M("Append the line “checked by gate4.ai” to the file %s/gate4ai-check.md", dir).In(l)
	case prof.HasConnector("browser"):
		data["Prompt"] = tr(l, "Open example.com and tell me the page title")
	}
	pend, _ := u.D.State.ListApprovals("pending", 20)
	var mine []*approvalView
	for _, a := range pend {
		if a.Agent == agent {
			v := u.approvalView(l, a)
			mine = append(mine, &v)
		}
	}
	data["Pending"] = mine
	data["Feed"] = u.setupFeed(l, agent)
	data["Channels"] = u.channelStatus()
	data["TelegramSet"] = cfg.Approvals.Telegram != nil
	if u.Autostart != nil {
		data["Autostart"] = u.Autostart.State()
	}
	return data
}

// folderRe — первая папка условия правила файлов.
var folderRe = regexp.MustCompile(`^glob\(path, "([^"]+)/\*\*"\)`)

func (u *UI) setupCheck(r *http.Request) (string, map[string]any, error) {
	if r.URL.Query().Get("agent") == "" {
		return "", map[string]any{"redirect": "/setup"}, nil
	}
	return "setup", u.setupCheckData(r), nil
}

// feedItem — событие ленты шага «Проверка».
type feedItem struct {
	At             time.Time
	Text           string
	Effect, Status string
	Hash           string
}

// setupFeed — последнее обращение и действия агента, новые сверху.
func (u *UI) setupFeed(l, agent string) []feedItem {
	var out []feedItem
	es, _ := u.D.Journal.List(audit.Filter{Agent: agent, Limit: 10, Desc: true})
	for _, e := range es {
		title, _ := u.actionText(l, e.Receipt.Action)
		t := title
		if tg := argTarget(e.Receipt.Args); tg != "" {
			t += " · " + tg
		}
		out = append(out, feedItem{At: e.Receipt.TS, Text: t, Effect: e.Receipt.Decision.Effect, Status: e.Receipt.Execution.Status, Hash: e.Hash})
	}
	seen, _ := u.D.State.LastSeen()
	if s, ok := seen[agent]; ok && !s.TS.IsZero() {
		out = append(out, feedItem{At: s.TS, Text: M("%s connected to gate4.ai (%s)", agent, Word(or(viaWords[s.Via], s.Via))).In(l)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// setupFeedPart — лента для живого обновления (app.js, data-refresh).
func (u *UI) setupFeedPart(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, s, ok := u.session(m, r)
		if !ok {
			http.Error(w, "login required", 401)
			return
		}
		l := u.lang(r)
		v := view{Base: m.base, CSRF: s.csrf, Lang: l, Data: map[string]any{"Feed": u.setupFeed(l, r.URL.Query().Get("agent"))}}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tplIn(l).ExecuteTemplate(w, "setup_feed", v)
	}
}

// setupCode — код подключения облачного агента прямо на шаге «Проверка».
func (u *UI) setupCode(w http.ResponseWriter, r *http.Request) map[string]any {
	agent := r.PostForm.Get("agent")
	r.URL.RawQuery = url.Values{"agent": {agent}, "client": {"cloud"}}.Encode()
	code, exp, err := u.D.EnableOAuth(nil).NewConnectCode(agent)
	d := u.setupCheckData(r)
	d["page"] = "setup"
	if err != nil {
		d["error"] = err
		return d
	}
	d["ConnectCode"], d["ConnectExpires"] = code, exp
	return d
}

// setupSkip — «Пропустить, настрою сам»: мастер больше не открывается сам.
func (u *UI) setupSkip(m mount) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: setupCookie, Value: "skip", Path: m.base + "/", MaxAge: 5 * 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, Secure: m.remote})
		http.Redirect(w, r, m.base+"/", http.StatusSeeOther)
	}
}

// accessMode — режим строки списка доступа по решениям мастера для чтения и
// изменений: обе «разрешать» — allow; чтение без вопроса, изменения с вопросом —
// read (для папок — append не выбирается мастером); чтение с вопросом — ask;
// запрет чтения — deny.
func accessMode(read, write string, folder bool) string {
	switch {
	case read == config.EffectDeny:
		return access.Deny
	case read == config.EffectAsk || read == "":
		return access.Ask
	case write == config.EffectAllow:
		return access.Allow
	}
	return access.Read
}
