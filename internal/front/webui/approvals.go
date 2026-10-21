// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/state"
)

// ---- Подтверждения ----

// argKV — аргумент запроса для показа.
type argKV struct{ K, V string }

// approvalView — запрос подтверждения человеческим языком: что, где, что
// изменится и почему спрашиваем; технические детали — отдельно.
type approvalView struct {
	*state.Approval
	Title, Desc string // действие на языке страницы
	Verb        string // название действия внутри фразы «агент хочет …» (со строчной)
	Target      string // файл, сайт, адресаты
	Preview     string // содержимое, которое запишется или уйдёт
	Append      bool   // дописывается в конец (показывается «+»)
	Args        []argKV
	Why         string
	Always      string // «Always allow in ~/notes»; "" — не предлагать
}

// targetKeys — аргументы, которые называют цель действия, по убыванию важности.
var targetKeys = []string{"path", "url", "to", "recipient", "recipients", "site", "folder", "file", "account", "query"}

// previewKeys — аргументы с содержимым, которое пользователь хочет увидеть до решения.
var previewKeys = []string{"text", "content", "body", "message", "subject"}

// shortPath — путь с ~ вместо домашнего каталога.
func shortPath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && (p == h || strings.HasPrefix(p, h+string(os.PathSeparator))) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func argString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = fmt.Sprint(x)
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// argTarget — цель действия одной строкой: путь, сайт или адресаты.
func argTarget(args map[string]any) string {
	for _, k := range targetKeys {
		v, ok := args[k]
		if !ok {
			continue
		}
		s := argString(v)
		switch k {
		case "path", "folder", "file":
			return shortPath(s)
		case "url", "site":
			if pu, err := url.Parse(s); err == nil && pu.Host != "" {
				return pu.Host + strings.TrimSuffix(pu.Path, "/")
			}
		}
		return s
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Причины решения ядра (английский текст policy.Decide) — словами на языке страницы.
var (
	reDefault   = regexp.MustCompile(`^no rule matched; default for effect class "(\w+)" is (\w+)$`)
	reRule      = regexp.MustCompile(`^rule (\S+): (\w+)$`)
	reNoConn    = regexp.MustCompile(`^connector "([^"]+)" is not available for profile "([^"]+)"$`)
	reDayLimit  = regexp.MustCompile(`^daily limit of (\d+) actions (\S+) reached$`)
	reUnknownAg = regexp.MustCompile(`^unknown agent "([^"]+)"$`)
	reOnlyDir   = regexp.MustCompile(`^Only the folder (.+)$`)
	rePerDay    = regexp.MustCompile(`^At most (\d+) a day$`)
)

// reasonText — причина решения на языке l.
func reasonText(l, s string) string {
	switch {
	case reDefault.MatchString(s):
		m := reDefault.FindStringSubmatch(s)
		return M("No rule for this: “%s” actions are “%s” by default.", Word(or(classWords[m[1]], m[1])), Word(or(effectWords[m[2]], m[2]))).In(l)
	case strings.HasPrefix(s, "nested call: no rule matched"):
		return tr(l, "Part of an action that was already allowed.")
	case reRule.MatchString(s):
		m := reRule.FindStringSubmatch(s)
		return M("Rule %s: %s.", m[1], Word(or(effectWords[m[2]], m[2]))).In(l)
	case reNoConn.MatchString(s):
		m := reNoConn.FindStringSubmatch(s)
		return M("The agent has no access to %s.", m[1]).In(l)
	case reDayLimit.MatchString(s):
		m := reDayLimit.FindStringSubmatch(s)
		return M("The daily limit of %s actions is reached.", m[1]).In(l)
	case reUnknownAg.MatchString(s):
		return M("Unknown agent %s.", reUnknownAg.FindStringSubmatch(s)[1]).In(l)
	case reOnlyDir.MatchString(s):
		return M("Only the folder %s", reOnlyDir.FindStringSubmatch(s)[1]).In(l)
	case rePerDay.MatchString(s):
		return M("At most %s a day", rePerDay.FindStringSubmatch(s)[1]).In(l)
	}
	return tr(l, s)
}

// alwaysScope — что разрешит «Разрешать всегда»: «in ~/notes», «on github.com».
func (u *UI) alwaysScope(a *state.Approval) (Msg, []Cond, bool) {
	t, ok := toolSchema(u.tools(), a.Action)
	if !ok {
		return Msg{}, nil, false
	}
	conds := suggestConds(t, a.Args)
	for _, c := range conds {
		switch c.Kind {
		case "in_folder":
			return M("in %s", shortPath(c.Value)), []Cond{c}, true
		case "domain":
			return M("on %s", c.Value), []Cond{c}, true
		}
	}
	if len(conds) > 0 { // адресаты из списка, суммы — правило без них разрешило бы лишнее
		return Msg{}, nil, false
	}
	return M("this action"), nil, true
}

func (u *UI) approvalView(l string, a *state.Approval) approvalView {
	v := approvalView{Approval: a, Target: argTarget(a.Args)}
	v.Title, v.Desc = u.actionText(l, a.Action)
	v.Verb = lowerFirst(v.Title)
	used := map[string]bool{}
	for _, k := range targetKeys {
		if _, ok := a.Args[k]; ok {
			used[k] = true
			break
		}
	}
	for _, k := range previewKeys {
		if s, ok := a.Args[k].(string); ok && s != "" {
			v.Preview, used[k] = clip(s, 2000), true
			v.Append = strings.HasSuffix(a.Action, ".append")
			break
		}
	}
	if strings.HasSuffix(a.Action, ".change_policy") {
		// Самонастройка: что именно изменится — правки словами, аргументы не нужны.
		v.Preview = strings.Join(changeLines(l, a.Args), "\n")
		for k := range a.Args {
			used[k] = true
		}
	}
	keys := make([]string, 0, len(a.Args))
	for k := range a.Args {
		if !used[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v.Args = append(v.Args, argKV{k, clip(argString(a.Args[k]), 300)})
	}
	if a.Decision.Default {
		v.Why = M("There is no rule for this, so “%s” actions ask you.", Word(or(classWords[a.Class], a.Class))).In(l)
	} else {
		v.Why = reasonText(l, a.Decision.Reason)
	}
	if scope, _, ok := u.alwaysScope(a); ok {
		v.Always = M("Always allow %s", scope).In(l)
	}
	return v
}

func (u *UI) approvalViews(l string, as []*state.Approval) []approvalView {
	out := make([]approvalView, len(as))
	for i, a := range as {
		out[i] = u.approvalView(l, a)
	}
	return out
}

func (u *UI) approvalsPage(m mount) func(r *http.Request) (string, map[string]any, error) {
	return func(r *http.Request) (string, map[string]any, error) {
		l := u.lang(r)
		pend, err := u.D.State.ListApprovals(state.Pending, 50)
		status := r.URL.Query().Get("status")
		recent, _ := u.D.State.ListApprovals(status, 50)
		recent = slices.DeleteFunc(recent, func(a *state.Approval) bool { return a.Status == state.Pending })
		data := map[string]any{"Pending": u.approvalViews(l, pend), "Recent": u.approvalViews(l, recent), "Status": status, "Questions": u.D.Questions()}
		cfg := u.D.Config.Current()
		data["Timeout"] = timeoutWord(cfg.Approvals.Timeout.D())
		data["TelegramSet"] = cfg.Approvals.Telegram != nil
		data["PhoneOK"] = cfg.ChannelOn("telegram") || cfg.ChannelOn("webpush")
		return "approvals", data, err
	}
}

// decide — решение по запросу; канал в квитанции различает локальный и удалённый вход.
// «approve_always» — разрешить и сразу записать правило по аргументам запроса
// (папка, сайт): кнопка называет, что именно будет разрешено.
func (u *UI) decide(m mount) func(w http.ResponseWriter, r *http.Request) map[string]any {
	channel := "webui"
	if m.remote {
		channel = "webui-remote"
	}
	return func(w http.ResponseWriter, r *http.Request) map[string]any {
		dec := r.PostForm.Get("decision")
		approve := dec == "approve" || dec == "approve_rule" || dec == "approve_always"
		a, _ := u.D.State.GetApproval(r.PathValue("id"))
		res, err := u.D.Pipeline.Decide(r.Context(), r.PathValue("id"), approve, channel, "owner")
		back := or(r.PostForm.Get("back"), "/approvals")
		switch {
		case errors.Is(err, state.ErrAlreadyDecided) && a != nil:
			// Устаревшая страница, повторное нажатие или решение в другом канале:
			// это не ошибка — сообщаем, кто и как уже решил.
			if a, gerr := u.D.State.GetApproval(a.ID); gerr == nil {
				return redirect(back, M("Already decided: %s", resolvedText(a)))
			}
			return redirect(back, "Already decided")
		case errors.Is(err, state.ErrNoApproval):
			return redirectErr(back, M("The request was not found"))
		case err != nil:
			return redirectErr(back, err)
		}
		verb := Word("Denied")
		if approve {
			verb = Word("Allowed")
		}
		title := ""
		if a != nil {
			title = a.Action
		}
		msg := M("%s: %s → %s, result %s", verb, agentName(a), title, Word(res.Status))
		detail := ""
		if res.Receipt != "" {
			detail = "#" + audit.Short(res.Receipt)
		}
		if a == nil || m.remote {
			return redirectDetail(back, msg, detail)
		}
		switch dec {
		case "approve_rule":
			return redirect("/policy/rule?approval="+url.QueryEscape(a.ID), M("%s. Now you can create a rule so it is not asked next time.", msg))
		case "approve_always":
			id, err := u.allowAlways(a)
			if err != nil {
				return redirectErr(back, M("%s, but the rule was not saved: %s", msg, err))
			}
			prof := u.D.Config.Current().Agents[a.Agent].Profile
			return redirectDetail(back, M("%s. From now on this is allowed without asking — rule %s in Access → Rules.", msg, id), "/policy?profile="+prof+"#rule-"+id)
		}
		return redirectDetail(back, msg, detail)
	}
}

func agentName(a *state.Approval) string {
	if a == nil {
		return ""
	}
	return a.Agent
}

// allowAlways — правило allow для профиля агента по аргументам запроса.
func (u *UI) allowAlways(a *state.Approval) (string, error) {
	_, conds, ok := u.alwaysScope(a)
	if !ok {
		return "", M("this request has no folder or site to limit the rule to")
	}
	t, _ := toolSchema(u.tools(), a.Action)
	when, err := BuildWhen(conds, actionArgs(t))
	if err != nil {
		return "", err
	}
	prof := u.D.Config.Current().Agents[a.Agent].Profile
	var id string
	err = u.edit("always allow "+a.Action+" for "+a.Agent, func(c *config.Config) error {
		for _, r := range c.Profiles[prof].Rules {
			if r.Action == a.Action && r.When == when && r.Effect == config.EffectAllow {
				id = r.ID
				return nil
			}
		}
		id = c.Profiles[prof].UniqueRuleID(strings.ReplaceAll(a.Action, ".", "-") + "-always")
		return c.PutRule(prof, config.Rule{ID: id, Action: a.Action, When: when, Effect: config.EffectAllow, Reason: "Allowed from an approval"})
	})
	if err != nil {
		return "", err
	}
	if warn := u.overridden(prof, a.Action, config.EffectAllow, a.ID); warn != nil {
		return id, warn
	}
	return id, nil
}

// resolvedText — кто и как решил запрос: «bot → notes.append: allowed in Telegram, just now».
func resolvedText(a *state.Approval) Msg {
	var what Msg
	switch a.Status {
	case state.Pending:
		return M("%s → %s: still waiting", a.Agent, a.Action)
	case state.Timeout:
		return M("%s → %s: nobody answered in time, the action was denied", a.Agent, a.Action)
	case state.Approved, state.Done:
		what = M("allowed")
	default:
		what = M("denied")
	}
	where := Msg{}
	if a.Channel != "" {
		where = M("via %s", Word(or(channelWords[a.Channel], a.Channel)))
	}
	when := ""
	if !a.DecidedAt.IsZero() {
		when = ago(a.DecidedAt)
	}
	return M("%s → %s: %s %s, %s", a.Agent, a.Action, what, where, Word(when))
}

// livePending — очередь подтверждений для живого обновления страницы (app.js):
// карточки ожидающих запросов и итог тех из known, что уже не ждут решения.
func (u *UI) livePending(m mount) http.HandlerFunc {
	type card struct {
		ID   string `json:"id"`
		HTML string `json:"html"`
	}
	type gone struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		_, s, ok := u.session(m, r)
		if !ok {
			http.Error(w, "login required", 401)
			return
		}
		pend, err := u.D.State.ListApprovals(state.Pending, 50)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		back := "/approvals"
		if b := r.URL.Query().Get("back"); b == "/" || strings.HasPrefix(b, "/setup/check?") {
			back = b
		}
		l := u.lang(r)
		v := view{Base: m.base, Remote: m.remote, CSRF: s.csrf, Lang: l}
		out := struct {
			Pending  []card `json:"pending"`
			Resolved []gone `json:"resolved"`
		}{Pending: []card{}, Resolved: []gone{}}
		shown := map[string]bool{}
		for _, a := range pend {
			var b strings.Builder
			if err := tplIn(l).ExecuteTemplate(&b, "approval_card", map[string]any{"V": v, "A": u.approvalView(l, a), "Back": back}); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			shown[a.ID] = true
			out.Pending = append(out.Pending, card{ID: a.ID, HTML: b.String()})
		}
		known := strings.Split(r.URL.Query().Get("known"), ",")
		if len(known) > 100 {
			known = known[:100]
		}
		for _, id := range known {
			if id == "" || shown[id] {
				continue
			}
			a, err := u.D.State.GetApproval(id)
			switch {
			case err != nil:
				out.Resolved = append(out.Resolved, gone{ID: id, Text: tr(l, "The request is no longer waiting.")})
			case a.Status != state.Pending: // ожидающие сверх первых 50 остаются как есть
				out.Resolved = append(out.Resolved, gone{ID: id, Text: M("Already decided: %s", resolvedText(a)).In(l)})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// answer — ответ на вопрос с вводом (код для входа на сайт): значение уходит
// прямо в поле страницы, нигде не показывается и не пишется в журнал.
func (u *UI) answer(w http.ResponseWriter, r *http.Request) map[string]any {
	value := r.PostForm.Get("value")
	if r.PostForm.Get("decision") == "decline" {
		value = ""
	}
	if err := u.D.Answer(r.PathValue("id"), value); err != nil {
		return redirectErr("/approvals", err)
	}
	if value == "" {
		return redirect("/approvals", "Declined: no code entered")
	}
	return redirect("/approvals", "The code was entered into the page field")
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

// ---- Каналы уведомлений (Настройки → Уведомления) ----

const notificationsPage = "/settings/notifications"

func (u *UI) toggleChannel(w http.ResponseWriter, r *http.Request) map[string]any {
	name, on := r.PathValue("name"), r.PostForm.Get("on") == "1"
	back := or(r.PostForm.Get("back"), notificationsPage)
	if name == "telegram" && on && u.D.Config.Current().Approvals.Telegram == nil {
		return redirect("/approvals/telegram", nil)
	}
	if err := u.edit("approval channel "+name, func(c *config.Config) error { c.SetChannel(name, on); return nil }); err != nil {
		return redirectErr(back, err)
	}
	if on {
		return redirect(back, "The channel is on")
	}
	return redirect(back, "The channel is off")
}

func (u *UI) testChannel(w http.ResponseWriter, r *http.Request) map[string]any {
	back := or(r.PostForm.Get("back"), notificationsPage)
	if u.Channels == nil {
		return redirectErr(back, M("Notification channels are not attached"))
	}
	if err := u.Channels.Test(r.Context(), r.PathValue("name")); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "A test notification was sent — check that it arrived.")
}

// timeouts — сроки ожидания решения в выпадающем списке.
var timeouts = []string{"15m", "30m", "1h", "2h", "8h", "1d"}

// timeoutWord — срок ожидания решения в виде значения списка (30m, 2h, 1d).
func timeoutWord(d interface{ Minutes() float64 }) string {
	mins := int(d.Minutes())
	switch {
	case mins <= 0:
		return "30m"
	case mins%(24*60) == 0:
		return strconv.Itoa(mins/(24*60)) + "d"
	case mins%60 == 0:
		return strconv.Itoa(mins/60) + "h"
	}
	return strconv.Itoa(mins) + "m"
}

func (u *UI) saveApprovalSettings(w http.ResponseWriter, r *http.Request) map[string]any {
	back := or(r.PostForm.Get("back"), "/settings")
	d, err := config.ParseDuration(strings.TrimSpace(r.PostForm.Get("timeout")))
	if err != nil || d <= 0 {
		return redirectErr(back, M("Waiting time: use e.g. 30m, 2h, 1d"))
	}
	if err := u.edit("approval timeout", func(c *config.Config) error { c.Approvals.Timeout = config.Duration(d); return nil }); err != nil {
		return redirectErr(back, err)
	}
	return redirect(back, "Saved")
}

// ---- Telegram: мастер ----

func (u *UI) telegramPage(r *http.Request) (string, map[string]any, error) {
	cfg := u.D.Config.Current()
	_, tokErr := u.D.Vault.Get("@telegram.bot")
	p := u.Protection()
	data := map[string]any{"HasToken": tokErr == nil, "Bot": r.URL.Query().Get("bot"), "Telegram": cfg.Approvals.Telegram,
		"On": cfg.ChannelOn("telegram"), "Public": u.D.PublicURL() != "",
		// Мастер открыт из «Входа» (for=login): назад — в Настройки. В конце мастера
		// в любом случае — включение подтверждения входа в Web UI через этого бота.
		"For": r.URL.Query().Get("for"), "LoginTelegram": p.Telegram, "LoginPassword": p.Password != "", "LoginReady": u.confirmer() != nil}
	return "telegram", data, nil
}

// telegramBack — адрес мастера Telegram с тем же for=, что у формы шага.
func telegramBack(r *http.Request, q string) string {
	to := "/approvals/telegram"
	if f := r.PostForm.Get("for"); f == "login" || f == "setup" {
		to = withParam(to, "for", f)
	}
	if q != "" {
		k, v, _ := strings.Cut(q, "=")
		to = withParam(to, k, v)
	}
	return to
}

func (u *UI) telegramStep(w http.ResponseWriter, r *http.Request) map[string]any {
	back := telegramBack(r, "")
	if u.Channels == nil {
		return redirectErr(back, M("Notification channels are not attached"))
	}
	ctx := r.Context()
	switch r.PostForm.Get("step") {
	case "token":
		tok := strings.TrimSpace(r.PostForm.Get("token"))
		if tok == "" {
			return redirectErr(back, M("Paste the token from @BotFather"))
		}
		name, err := u.Channels.TelegramBot(ctx, tok)
		if err != nil {
			return redirectErr(back, M("Telegram did not accept the token: %s", err))
		}
		if err := u.D.Vault.Set("@telegram.bot", tok); err != nil {
			return redirectErr(back, err)
		}
		return redirect(telegramBack(r, "bot="+name), M("Bot @%s found; the token is saved in gate4.ai passwords.", name))
	case "find":
		tok, err := u.D.Vault.Get("@telegram.bot")
		if err != nil {
			return redirectErr(back, M("Save the bot token first"))
		}
		users, err := u.Channels.TelegramWriters(ctx, tok)
		r.URL.RawQuery = url.Values{"for": {r.PostForm.Get("for")}}.Encode()
		data, _ := func() (map[string]any, error) { _, d, e := u.telegramPage(r); return d, e }()
		data["page"] = "telegram"
		data["Users"] = users
		data["Searched"] = true
		if err != nil {
			data["error"] = M("Telegram: %s", err)
		}
		return data
	case "owner":
		id, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("owner_id")), 10, 64)
		if err != nil || id == 0 {
			return redirectErr(back, M("The Telegram ID must be a number"))
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
			return redirectErr(back, err)
		}
		// Канал перезапускается подпиской на конфигурацию; пробное сообщение — сразу.
		// Дальше — шаг «Готово»: там же включается подтверждение входа в Web UI.
		if err := u.Channels.Test(ctx, "telegram"); err != nil {
			return redirectErr(back+"#done", M("Telegram is on, but the test message failed: %s", err))
		}
		return redirect(back+"#done", "Telegram is on — a test message was sent to your chat with the bot.")
	}
	return redirectErr(back, M("Unknown step"))
}

// pathBase — последняя часть пути для коротких подписей.
func pathBase(p string) string { return path.Base(strings.ReplaceAll(p, `\`, "/")) }

// changeLines — правки gate4ai.change_policy словами на языке l (то же, что
// config.Change.Describe, но с переводом).
func changeLines(l string, args map[string]any) []string {
	var out []string
	if s, ok := args["summary"].(string); ok && s != "" {
		out = append(out, s)
	}
	raw, _ := json.Marshal(args["changes"])
	var chs []config.Change
	_ = json.Unmarshal(raw, &chs)
	for _, ch := range chs {
		items := strings.Join(ch.Items, ", ")
		var m Msg
		switch ch.Op {
		case config.OpAddRule:
			m = M("Rule: %s %s", Word(effectWords[ch.Effect]), ch.Action)
			if ch.When != "" {
				w, _ := describeWhen(l, ch.When)
				m = M("Rule: %s %s if %s", Word(effectWords[ch.Effect]), ch.Action, w)
			}
		case config.OpRemoveRule:
			m = M("Remove rule %s", ch.RuleID)
		case config.OpAddListItems:
			m = M("Add to list %s: %s", ch.List, items)
		case config.OpRemoveListItems:
			m = M("Remove from list %s: %s", ch.List, items)
		case config.OpAddConnector:
			m = M("Give access to connector %s", ch.Connector)
		case config.OpInstallConnector:
			m = M("Install connector %s from the store and give access to it", ch.Connector)
		case config.OpInstallDev:
			m = M("Build and install an own build from %s and give access to it", ch.Folder)
		case config.OpConnectorSetting:
			m = M("Settings of connector %s: %s", ch.Connector, ch.SettingsJSON)
		case config.OpAddSettingItems:
			m = M("Add to setting %s.%s: %s", ch.Connector, ch.Key, items)
		case config.OpRemoveSettingItems:
			m = M("Remove from setting %s.%s: %s", ch.Connector, ch.Key, items)
		case config.OpAddCredential:
			m = M("Account %s (secret values stay in the vault, you set them)", ch.Account)
		default:
			m = M("%s", ch.Op)
		}
		line := "• " + m.In(l)
		if ch.Profile != "" {
			line += " " + M("(permissions %s)", ch.Profile).In(l)
		}
		out = append(out, line)
	}
	return out
}

// lowerFirst — «Append to a file» → «append to a file» (внутри фразы).
func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && unicode.IsUpper(r[0]) && !unicode.IsUpper(r[1]) {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}
