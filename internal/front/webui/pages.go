// SPDX-License-Identifier: AGPL-3.0-only

package webui

import (
	"context"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/connectors/store"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/relayclient"
	"github.com/gate4ai/gate4ai/internal/state"
)

// ---- Общее ----

// redirect — переход после действия формы с сообщением (flash): Msg, Word,
// английская строка (переводится по каталогу) или nil. Сообщение уходит в сессию.
func redirect(to string, flash any) map[string]any {
	if s, ok := flash.(string); ok && s == "" {
		flash = nil
	}
	return map[string]any{"redirect": to, "flash": flash}
}

// redirectDetail — переход с сообщением и подробностями под «Details» (пути файлов).
func redirectDetail(to string, flash any, detail string) map[string]any {
	return map[string]any{"redirect": to, "flash": flash, "detail": detail}
}

func redirectErr(to string, err error) map[string]any {
	return map[string]any{"redirect": to, "error": err}
}

// withParam добавляет параметр запроса к адресу перехода — перед «#…», иначе
// он попал бы во фрагмент и до сервера не дошёл.
func withParam(to, k, v string) string {
	path, frag, hasFrag := strings.Cut(to, "#")
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	path += sep + k + "=" + template.URLQueryEscaper(v)
	if hasFrag {
		path += "#" + frag
	}
	return path
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

// ---- Главная ----

// step — пункт чек-листа первого запуска (для тех, кто пропустил мастер).
type step struct {
	Title, Text, Link, Button string
	Done, Optional            bool
}

// setupSkipped — владелец пропустил мастер первого запуска (cookie браузера).
func setupSkipped(r *http.Request) bool {
	c, err := r.Cookie(setupCookie)
	return err == nil && c.Value == "skip"
}

// entryView — действие из журнала одной строкой: «Read a file · todo.md».
type entryView struct {
	audit.Entry
	Title, Target string
}

func (u *UI) entryViews(l string, es []audit.Entry) []entryView {
	out := make([]entryView, len(es))
	for i, e := range es {
		t, _ := u.actionText(l, e.Receipt.Action)
		out[i] = entryView{Entry: e, Title: t, Target: argTarget(e.Receipt.Args)}
	}
	return out
}

func (u *UI) overview(m mount) func(r *http.Request) (string, map[string]any, error) {
	return func(r *http.Request) (string, map[string]any, error) {
		l := u.lang(r)
		cfg := u.D.Config.Current()
		// Свежая установка: агентов нет — мастер первого запуска (пока его не пропустили).
		if !m.remote && len(cfg.Agents) == 0 && !setupSkipped(r) {
			return "", map[string]any{"redirect": "/setup"}, nil
		}
		pend, err := u.D.State.ListApprovals(state.Pending, 50)
		recent, _ := u.D.Journal.List(audit.Filter{Limit: 8, Desc: true})
		seen, _ := u.D.State.LastSeen()
		data := map[string]any{"Pending": u.approvalViews(l, pend), "Recent": u.entryViews(l, recent), "Seen": seen, "Pairings": u.pairingViews()}
		bctx, bcancel := context.WithTimeout(r.Context(), 2*time.Second)
		if bs, berr := u.D.BrowserStatus(bctx); berr == nil {
			data["Browser"] = bs
		}
		bcancel()
		if m.remote {
			return "overview", data, err
		}
		data["Agents"] = u.agentRows(l)
		anySeen := false
		for a := range cfg.Agents {
			if !seen[a].TS.IsZero() {
				anySeen = true
			}
		}
		channelOK := cfg.ChannelOn("telegram") || cfg.ChannelOn("webpush")
		rs := u.relay()
		steps := []step{
			{Title: "Connect an AI assistant", Text: "Claude Code, Claude Desktop, Cursor, VS Code, Codex, Gemini CLI or Claude.ai / ChatGPT — and choose what it may do.", Link: "/setup", Button: "Start the setup", Done: len(cfg.Agents) > 0},
			{Title: "Check that it works", Text: "Ask the assistant to do something: its first request shows up here.", Link: "/agents", Button: "Agents", Done: anySeen},
			{Title: "Get approvals on your phone", Text: "Telegram or browser push — to approve actions when you are away from the computer.", Link: "/settings/notifications", Button: "Notifications", Done: channelOK},
			{Title: "Claude.ai or ChatGPT: turn on internet access", Text: "Only for assistants that do not run on this computer.", Link: "/settings/internet", Button: "Internet access", Done: rs.Configured || u.D.PublicURL() != "", Optional: true},
		}
		left := 0
		for _, s := range steps {
			if !s.Done && !s.Optional {
				left++
			}
		}
		data["Steps"], data["StepsLeft"] = steps, left
		data["Relay"] = rs
		data["PublicURL"] = u.D.PublicURL()
		data["Channels"] = u.channelStatus()
		data["Chain"] = u.D.Journal.VerifyChain() == nil
		return "overview", data, err
	}
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
	return "journal", map[string]any{"Entries": u.entryViews(u.lang(r), es), "Q": q, "Chain": chain}, err
}

func (u *UI) receipt(r *http.Request) (string, map[string]any, error) {
	e, err := u.D.Journal.Find(r.PathValue("hash"))
	data := map[string]any{"Entry": e}
	if err == nil {
		title, desc := u.actionText(u.lang(r), e.Receipt.Action)
		data["Title"], data["Desc"], data["Target"] = title, desc, argTarget(e.Receipt.Args)
	}
	return "receipt", data, err
}
