// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/domain"

	"github.com/gate4ai/gate4ai/connectors/browser/worker"
)

// SessionIdle — страница вызывающего коннектора освобождается, если end_call
// не пришёл (хост упал): иначе воркер ждал бы её вечно.
var SessionIdle = 2 * time.Minute

// AgentIdle — сеанс агента без действий закрывается сам (агент забыл close).
var AgentIdle = 10 * time.Minute

// AgentWait — сколько действие агента ждёт страницу, занятую другим сеансом
// (дольше — ошибка busy с подсказкой, а не зависание до таймаута вызова).
var AgentWait = 5 * time.Second

// MaxSteps — предел записанных шагов сеанса агента (для фикстур тестов).
const MaxSteps = 500

// Browser — коннектор: действия агента без сеанса (read, screenshot, extract —
// страница на время действия), сеанс агента (open → observe / act / network /
// page_extract / page_screenshot → close; сайты — из настроек) и те же
// действия для других коннекторов через service_call (страница на корневое
// действие, сайты — params.sites вызывающего).
type Browser struct {
	W *worker.Service

	mu       sync.Mutex
	sessions map[string]*callSession  // корневое действие → страница
	agents   map[string]*agentSession // сеанс агента → страница
	create   sync.Mutex               // создание страниц по очереди (воркер — одна сессия за раз)
}

type callSession struct {
	s      *worker.Session
	sites  domain.Sites
	opened bool
	url    string // адрес страницы после последнего действия
	timer  *time.Timer
	mu     sync.Mutex // действия одной страницы — по очереди
}

// agentSession — страница агента между его вызовами и записанные шаги.
type agentSession struct {
	callSession
	id        string
	agent     string // агент-владелец (ExecRequest.Agent): чужой агент сеанс не видит
	steps     []map[string]any
	truncated bool
	closed    bool               // страница освобождена (под mu)
	cancel    context.CancelFunc // отмена идущего действия (под Browser.mu)
}

// New — коннектор с воркером по умолчанию.
func New() *Browser { return &Browser{W: worker.New()} }

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func strs(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			out = append(out, strs(x)...)
		}
		return out
	}
	return nil
}

// settings — настройки воркера из настроек коннектора и значения ключа модели (reveal).
func settings(r abi.ExecRequest) worker.Settings {
	s := r.Settings
	st := worker.Settings{Engine: str(s, "engine"), CDPURL: str(s, "cdp_url"), ChromePath: str(s, "chrome_path"),
		ProfileDir: str(s, "profile_dir"), Model: str(s, "model")}
	if alias := str(s, "model_key"); alias != "" {
		st.ModelKey = r.Revealed[alias]
	}
	if n, ok := s["max_text_chars"].(float64); ok {
		st.MaxTextChars = int(n)
	}
	st.RedactParams = strs(s["redact_params"])
	return st
}

func num(m map[string]any, k string) int { n, _ := m[k].(float64); return int(n) }

// cleanURL — адрес для ответа и журнала: одноразовые коды и токены в query
// (OAuth ?code=…) скрыты.
func cleanURL(u string) string { return worker.RedactURL(u, nil) }

// sitesOf — список сайтов: mode allowlist | blocklist ("" — blocklist, если
// задан blocked, иначе allowlist), domains — разрешённые, blocked — запрещённые.
func sitesOf(m map[string]any, domainsKey string) (domain.Sites, error) {
	mode := str(m, "mode")
	blocked := strs(m["blocked"])
	switch mode {
	case "allowlist":
	case "blocklist":
		return domain.Sites{Blocklist: true, Domains: blocked}, nil
	case "":
		if len(blocked) > 0 {
			return domain.Sites{Blocklist: true, Domains: blocked}, nil
		}
	default:
		return domain.Sites{}, fmt.Errorf("mode %q must be allowlist or blocklist", mode)
	}
	return domain.Sites{Domains: strs(m[domainsKey])}, nil
}

// fail — ошибка воркера в коде ABI.
func fail(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, worker.ErrDomain):
		return abi.Errorf(abi.CodeCapabilityDenied, "%v", err)
	case errors.Is(err, worker.ErrNotFound), errors.Is(err, worker.ErrNoEffect):
		return abi.Errorf(abi.CodeNotFound, "%v", err)
	case errors.Is(err, context.Canceled):
		return abi.Errorf(abi.CodeCancelled, "%v", err)
	}
	for _, e := range []error{worker.ErrWrongOrigin, worker.ErrFormMismatch, worker.ErrSecretOnPage} {
		if errors.Is(err, e) {
			return abi.Errorf(e.Error(), "%s", strings.TrimPrefix(err.Error(), e.Error()+": "))
		}
	}
	var ae *abi.Error
	if errors.As(err, &ae) {
		return err
	}
	return abi.Errorf(abi.CodeIO, "%v", err)
}

// Execute исполняет действие.
func (b *Browser) Execute(ctx context.Context, h *abi.Host, r abi.ExecRequest) (abi.ExecResult, error) {
	switch r.Action {
	case "read", "screenshot", "extract":
		res, err := b.agent(ctx, r)
		return res, fail(b.busy(siteHint(err)))
	}
	if r.Caller != nil && r.Caller.Session != "" {
		// Вход коннектором входа (via) в сеансе агента: страница — его сеанса.
		r.Args["session"] = r.Caller.Session
		r.Args[viaArg] = r.Caller.Connector // шаг коннектора входа — с пометкой в записи
		r.Caller = nil
	}
	if r.Caller != nil && str(r.Args, "session") != "" {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "session is for agents; a connector's page lives for its root call")
	}
	if r.Action == "act" { // аргументы act — до страницы и сеанса
		if err := worker.CheckAct(actRequest(r.Args)); err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
	}
	if r.Caller == nil {
		res, err := b.agentPage(ctx, r)
		return res, fail(b.busy(siteHint(err)))
	}
	res, err := b.service(ctx, r)
	return res, fail(b.busy(err))
}

// busy — подсказка, кто держит страницу.
func (b *Browser) busy(err error) error {
	if !errors.Is(err, worker.ErrBusy) {
		return err
	}
	b.mu.Lock()
	n := len(b.agents)
	b.mu.Unlock()
	if n > 0 {
		return abi.Errorf(abi.CodeLimit, "%v: an agent's browser session is open; close it with browser.close (an idle session closes after %s)", err, AgentIdle)
	}
	return abi.Errorf(abi.CodeLimit, "%v: a connector's action is using the page; try again when it ends", err)
}

// agentSites — сайты агента из настроек.
func agentSites(r abi.ExecRequest) (domain.Sites, error) {
	sites, err := sitesOf(r.Settings, "allowed_domains")
	if err != nil {
		return domain.Sites{}, fmt.Errorf("connectors.browser: %w", err)
	}
	if !sites.Blocklist && len(sites.Domains) == 0 {
		return domain.Sites{}, errors.New("connectors.browser.allowed_domains is empty: no sites are allowed")
	}
	return sites, nil
}

// agent — действие агента: страница на время действия.
func (b *Browser) agent(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	sites, err := agentSites(r)
	if err != nil {
		return abi.ExecResult{}, err
	}
	s, err := b.W.Acquire(ctx, settings(r), AgentWait)
	if err != nil {
		return abi.ExecResult{}, err
	}
	defer s.Release()
	page, err := s.Open(ctx, str(r.Args, "url"), sites)
	if err != nil {
		return abi.ExecResult{}, err
	}
	page.URL = cleanURL(page.URL) // адрес после редиректов: в нём бывают токены
	switch r.Action {
	case "read":
		p, err := s.Observe(ctx, 0)
		if err != nil {
			return abi.ExecResult{}, err
		}
		p.URL = cleanURL(p.URL)
		res := map[string]any{"url": p.URL, "title": p.Title}
		if p.Truncated {
			res["truncated"] = true
		}
		return abi.ExecResult{Structured: res, Content: []abi.Content{abi.Text(p.Text)}, ResultRef: p.URL, Source: "web:" + p.URL}, nil
	case "screenshot":
		sel := str(r.Args, "selector")
		png, err := s.ScreenshotElement(ctx, sel)
		if err != nil {
			return abi.ExecResult{}, err
		}
		return abi.ExecResult{Structured: map[string]any{"url": page.URL, "selector": sel}, Content: []abi.Content{abi.Image("image/png", png)},
			ResultRef: page.URL, Evidence: []abi.EvidenceItem{{Name: "element.png", MIME: "image/png", Data: png}}}, nil
	}
	data, err := s.Extract(ctx, str(r.Args, "instruction"))
	if err != nil {
		return abi.ExecResult{}, err
	}
	return abi.ExecResult{Structured: map[string]any{"url": page.URL, "data": data}, ResultRef: page.URL, Source: "web:" + page.URL}, nil
}

// session — страница корневого действия вызывающего (создаётся при первом обращении).
func (b *Browser) session(ctx context.Context, r abi.ExecRequest) (*callSession, error) {
	b.create.Lock()
	defer b.create.Unlock()
	b.mu.Lock()
	if cs, ok := b.sessions[r.Caller.Call]; ok {
		cs.timer.Reset(SessionIdle)
		b.mu.Unlock()
		return cs, nil
	}
	b.mu.Unlock()
	sites, err := sitesOf(r.Caller.Params, "domains")
	if err != nil {
		return nil, fmt.Errorf("params of %s: %w", r.Caller.Connector, err)
	}
	if !sites.Blocklist && len(sites.Domains) == 0 {
		return nil, abi.Errorf(abi.CodeCapabilityDenied, "%s allows no sites (capabilities.services params.domains is empty)", r.Caller.Connector)
	}
	wait := time.Duration(0)
	b.mu.Lock()
	if len(b.agents) > 0 {
		wait = AgentWait // сеанс агента может держать страницу долго: не ждём его до таймаута
	}
	b.mu.Unlock()
	s, err := b.W.Acquire(ctx, settings(r), wait)
	if err != nil {
		return nil, err
	}
	cs := &callSession{s: s, sites: sites}
	call := r.Caller.Call
	cs.timer = time.AfterFunc(SessionIdle, func() { b.EndCall(call) })
	b.mu.Lock()
	if b.sessions == nil {
		b.sessions = map[string]*callSession{}
	}
	b.sessions[call] = cs
	b.mu.Unlock()
	return cs, nil
}

// service — действие для другого коннектора на странице его корневого действия.
func (b *Browser) service(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	cs, err := b.session(ctx, r)
	if err != nil {
		return abi.ExecResult{}, err
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.page(ctx, r)
}

// page — действие на открытой странице (сеанс агента или корневой вызов коннектора).
func (cs *callSession) page(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	if r.Action != "open" && !cs.opened {
		return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "call browser.open first")
	}
	cs.s.SetLoginOrigins(loginOrigins(r.Accounts))
	res, err := cs.do(ctx, r)
	if err != nil {
		return res, err
	}
	switch r.Action {
	case "open", "observe", "network":
		// Страница на сайте входа доступной учётки: подсказать вызвать login.
		if acc := loginRequired(ctx, cs.s, cs.url, r.Accounts); acc != "" {
			res.Structured.(map[string]any)["login_required"] = map[string]any{"account": acc}
		}
	}
	return res, nil
}

// loginOrigins — origin страниц входа учёток вызывающего.
func loginOrigins(accs []abi.Account) []string {
	var out []string
	for _, a := range accs {
		out = append(out, a.Bind...)
	}
	return out
}

// loginRequired — учётка, на сайте входа которой страница ("" — нет такой).
// Признак страницы входа (login_page учётки) — элемент или начало адреса;
// без него — любая страница сайтов bind.
func loginRequired(ctx context.Context, s *worker.Session, u string, accs []abi.Account) string {
	for _, a := range accs {
		if !domain.OriginIn(u, a.Bind) {
			continue
		}
		lp := a.LoginPage
		if lp == nil || (lp.URLPrefix != "" && strings.HasPrefix(u, lp.URLPrefix)) || (lp.Selector != "" && s.Present(ctx, lp.Selector)) {
			return a.Alias
		}
	}
	return ""
}

// do — действие на открытой странице.
func (cs *callSession) do(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	switch r.Action {
	case "open":
		p, err := cs.s.Open(ctx, str(r.Args, "url"), cs.sites)
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.opened, cs.url = true, p.URL
		if err := cs.s.WaitFor(ctx, str(r.Args, "wait_for_selector"), str(r.Args, "wait_for_text"), num(r.Args, "timeout_ms")); err != nil {
			return abi.ExecResult{}, err
		}
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(p.URL), "title": p.Title}, ResultRef: cleanURL(p.URL)}, nil
	case "observe":
		if err := cs.s.WaitFor(ctx, str(r.Args, "wait_for_selector"), str(r.Args, "wait_for_text"), num(r.Args, "timeout_ms")); err != nil {
			return abi.ExecResult{}, err
		}
		p, err := cs.s.Observe(ctx, num(r.Args, "max_chars"))
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.url = p.URL
		root := str(r.Args, "selector")
		if root != "" {
			// Текст только внутри элемента (модальное окно, карточка).
			if p.Text, err = cs.s.TextIn(ctx, root); err != nil {
				return abi.ExecResult{}, err
			}
			p.Truncated = false
			if rs := []rune(p.Text); num(r.Args, "max_chars") > 0 && len(rs) > num(r.Args, "max_chars") {
				p.Text, p.Truncated = string(rs[:num(r.Args, "max_chars")]), true
			}
		}
		res := map[string]any{"url": cleanURL(p.URL), "title": p.Title, "text": p.Text}
		if p.Truncated {
			res["truncated"] = true
		}
		if el, _ := r.Args["elements"].(bool); el {
			els, err := cs.s.Elements(ctx, root)
			if err != nil {
				return abi.ExecResult{}, err
			}
			res["elements"] = els
		}
		if ol, _ := r.Args["outline"].(bool); ol {
			nodes, err := cs.s.Outline(ctx, root)
			if err != nil {
				return abi.ExecResult{}, err
			}
			res["outline"] = nodes
		}
		return abi.ExecResult{Structured: res, ResultRef: cleanURL(p.URL), Source: "web:" + cleanURL(p.URL)}, nil
	case "act":
		res, err := cs.s.Act(ctx, actRequest(r.Args))
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.url = res.URL
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(res.URL), "text": res.Text}, ResultRef: cleanURL(res.URL)}, nil
	case "network":
		all, _ := r.Args["all_sites"].(bool)
		reqs, err := cs.s.Network(ctx, str(r.Args, "url_contains"), all)
		if err != nil {
			return abi.ExecResult{}, err
		}
		res := map[string]any{"requests": reqs}
		if ids := idParams(reqs); len(ids) > 0 {
			res["hint"] = "the addresses contain identifiers (" + strings.Join(ids, ", ") +
				"): they may be personal data; the owner can hide them with connectors.browser.redact_params"
		}
		return abi.ExecResult{Structured: res, Source: "web:" + cleanURL(cs.url)}, nil
	case "page_extract":
		data, err := cs.s.Extract(ctx, str(r.Args, "instruction"))
		if err != nil {
			return abi.ExecResult{}, err
		}
		return abi.ExecResult{Structured: map[string]any{"data": data}, Source: "web:" + cleanURL(cs.url)}, nil
	case "page_screenshot":
		png, err := cs.s.ScreenshotElement(ctx, str(r.Args, "selector"))
		if err != nil {
			return abi.ExecResult{}, err
		}
		return abi.ExecResult{Content: []abi.Content{abi.Image("image/png", png)}}, nil
	case "login":
		if rec, _ := r.Args["record_only"].(bool); rec {
			// Вход через коннектор входа уже прошёл (хост): шаг для записи сеанса.
			m, _ := r.Args["result"].(map[string]any)
			return abi.ExecResult{Structured: m}, nil
		}
		form, err := loginForm(r)
		if err != nil {
			return abi.ExecResult{}, err
		}
		res, err := cs.s.Login(ctx, form)
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.url = res.URL
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(res.URL), "status": res.Status, "origin": res.Origin}, ResultRef: res.Status}, nil
	case "fill_secret":
		bind, f, err := secretField(r)
		if err != nil {
			return abi.ExecResult{}, err
		}
		fill := cs.s.FillSecret
		if pre, _ := r.Args["precheck"].(bool); pre {
			fill = cs.s.CheckSecret // хост проверяет до вопроса человеку: значения ещё нет
		}
		res, err := fill(ctx, bind, f)
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.url = res.URL
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(res.URL), "origin": res.Origin}, ResultRef: res.Origin}, nil
	}
	return abi.ExecResult{}, fmt.Errorf("unknown action %s", r.Action)
}

// agentPage — сеанс агента: open без session начинает сеанс (страница
// занята, пока агент не вызовет close или сеанс не простоит AgentIdle), с
// session — переходит на его странице; остальные действия — на странице
// сеанса. Каждое действие записывается шагом: close отдаёт шаги для фикстур
// тестов коннектора (hosttest.Replay).
func (b *Browser) agentPage(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	id := str(r.Args, "session")
	if all, _ := r.Args["all"].(bool); r.Action == "close" && all {
		return b.closeAll(r.Agent), nil
	}
	var as *agentSession
	if id == "" {
		if r.Action != "open" {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "pass session from browser.open (it starts a browser session)")
		}
		var err error
		if as, err = b.newAgentSession(ctx, r); err != nil {
			return abi.ExecResult{}, err
		}
	} else {
		b.mu.Lock()
		as = b.agents[id]
		if as != nil && as.agent != r.Agent {
			as = nil // сеанс другого агента: как неизвестный
		}
		if as != nil {
			as.timer.Reset(AgentIdle)
		}
		b.mu.Unlock()
		if as == nil {
			return abi.ExecResult{}, closedErr(id)
		}
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.closed {
		// close --all или простой закрыли сеанс, пока действие ждало страницу.
		return abi.ExecResult{}, closedErr(as.id)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.mu.Lock()
	as.cancel = cancel
	b.mu.Unlock()
	if r.Action == "close" {
		b.closeAgent(as)
		res := map[string]any{"session": as.id, "steps": as.steps}
		if as.truncated {
			res["truncated"] = true
		}
		return abi.ExecResult{Structured: res, Source: "web:session:" + as.id}, nil
	}
	res, err := as.page(ctx, r)
	as.record(r, res, err)
	if err != nil {
		if id == "" {
			b.closeAgent(as) // сеанс не начался
		}
		return abi.ExecResult{}, err
	}
	if r.Action == "open" {
		res.Structured.(map[string]any)["session"] = as.id
	}
	return res, nil
}

func (b *Browser) newAgentSession(ctx context.Context, r abi.ExecRequest) (*agentSession, error) {
	sites, err := agentSites(r)
	if err != nil {
		return nil, err
	}
	b.create.Lock()
	defer b.create.Unlock()
	s, err := b.W.Acquire(ctx, settings(r), AgentWait)
	if err != nil {
		return nil, err
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	as := &agentSession{callSession: callSession{s: s, sites: sites}, id: "s-" + hex.EncodeToString(raw[:]), agent: r.Agent}
	id := as.id
	as.timer = time.AfterFunc(AgentIdle, func() {
		as.mu.Lock()
		defer as.mu.Unlock()
		b.closeAgent(as)
	})
	b.mu.Lock()
	if b.agents == nil {
		b.agents = map[string]*agentSession{}
	}
	b.agents[id] = as
	b.mu.Unlock()
	return as, nil
}

// closedErr — сеанса нет: закрыт, неизвестен или принадлежит другому агенту
// (ответ один и тот же — чужие id не подтверждаются).
func closedErr(id string) error {
	return abi.Errorf(abi.CodeNotFound, "browser session %s is closed or unknown; start a new one with browser.open", id)
}

// closeAll закрывает сеансы агента agent (потерянный id, зависший сеанс):
// идущее действие сеанса отменяется, страница освобождается после его
// завершения — следующий сеанс не получит её посреди чужого действия.
func (b *Browser) closeAll(agent string) abi.ExecResult {
	b.mu.Lock()
	var mine []*agentSession
	for _, as := range b.agents {
		if as.agent != agent {
			continue
		}
		mine = append(mine, as)
		if as.cancel != nil {
			as.cancel()
		}
	}
	b.mu.Unlock()
	for _, as := range mine {
		as.mu.Lock()
		b.closeAgent(as)
		as.mu.Unlock()
	}
	return abi.ExecResult{Structured: map[string]any{"closed": len(mine)}}
}

// closeAgent освобождает страницу сеанса агента (close, close --all, простой,
// неудачный open); вызывающий держит as.mu — действий сеанса в этот момент нет.
func (b *Browser) closeAgent(as *agentSession) {
	b.mu.Lock()
	if b.agents[as.id] == as {
		delete(b.agents, as.id)
	}
	b.mu.Unlock()
	if as.closed {
		return
	}
	as.closed = true
	as.timer.Stop()
	as.s.Release()
}

// record — шаг сеанса в форме hosttest.Step: действие, аргументы без session,
// структурированный результат или ошибка.
func (as *agentSession) record(r abi.ExecRequest, res abi.ExecResult, err error) {
	if len(as.steps) >= MaxSteps {
		as.truncated = true
		return
	}
	// Без session и без пустых значений (CLI передаёт значения по умолчанию):
	// коннектор вызывает те же действия без них, и Replay сравнивает аргументы.
	args := map[string]any{}
	for k, v := range r.Args {
		if k != "session" && k != viaArg && k != "record_only" && k != "result" && k != "precheck" && !zero(v) {
			args[k] = v
		}
	}
	step := map[string]any{"action": r.Action, "args": args}
	if via, _ := r.Args[viaArg].(string); via != "" {
		step["via"] = via // шаг коннектора входа: его фикстура — hosttest.ReplayVia
	}
	if pre, _ := r.Args["precheck"].(bool); pre {
		return // проверка хоста до вопроса человеку — не шаг сеанса
	}
	if err != nil {
		var ae *abi.Error
		if !errors.As(fail(err), &ae) {
			ae = abi.Errorf(abi.CodeIO, "%v", err)
		}
		step["error"] = map[string]any{"code": ae.Code, "message": ae.Message}
	} else if m, ok := res.Structured.(map[string]any); ok {
		step["result"] = m
	}
	as.steps = append(as.steps, step)
}

// account — учётка входа из запроса хоста: хост передаёт её форму, только
// если учётка доступна вызывающему.
func account(r abi.ExecRequest) (*abi.Account, error) {
	acc := r.Login
	if acc == nil {
		return nil, abi.Errorf(abi.CodeSecretDenied, "account %s is not available here", str(r.Args, "account"))
	}
	// Учётку с коннектором входа вводит он сам (fill_secret, доступ проверил
	// хост); простой вход формой (login) у неё не бывает.
	if acc.Via != "" && r.Action == "login" {
		return nil, abi.Errorf("use_connector", "account %s signs in through %s: call browser.login through gate4ai, it runs that connector", acc.Alias, acc.Via)
	}
	return acc, nil
}

// fieldOf — поле формы учётки со значением: константа или секрет из Revealed.
// Правило поля задаёт владелец: sel вызывающего заменяет селектор учётки,
// только если вызывающий — коннектор входа этой учётки (via); если владелец
// селектор не задал — вызывающий выбирает поле, но только input[type=password].
func fieldOf(r abi.ExecRequest, f abi.AccountField, sel string) (worker.LoginField, error) {
	lf := worker.LoginField{Selector: f.Selector, Value: f.Value, AnyType: f.Type == "text"}
	if sel != "" && sel != f.Selector {
		switch {
		case viaCaller(r):
			lf.Selector = sel
		case f.Selector == "":
			lf.Selector, lf.AnyType = sel, false
		default:
			return lf, abi.Errorf(abi.CodeBadRequest, "field %s of account %s: the owner set its selector (%s); omit selector", f.Name, r.Login.Alias, f.Selector)
		}
	}
	if f.Secret != "" {
		v, ok := r.Revealed[f.Secret]
		if !ok || v == "" {
			return lf, abi.Errorf(abi.CodeSecretDenied, "secret %s of account %s is not set (gate4ai vault set %s)", f.Secret, r.Login.Alias, f.Secret)
		}
		lf.Value, lf.Secret = v, true
	}
	if lf.Selector == "" {
		return lf, abi.Errorf(abi.CodeBadRequest, "field %s of account %s has no selector", f.Name, r.Login.Alias)
	}
	return lf, nil
}

// codeField — поле кода, который вводит человек (без value и secret).
func codeField(f abi.AccountField) bool { return f.Value == "" && f.Secret == "" }

// viaCaller — действие вызвал коннектор входа учётки r.Login (её via):
// напрямую (Caller) или в сеансе агента (служебный аргумент viaArg).
func viaCaller(r abi.ExecRequest) bool {
	if r.Login == nil || r.Login.Via == "" {
		return false
	}
	conn := str(r.Args, viaArg)
	if r.Caller != nil {
		conn = r.Caller.Connector
	}
	return conn != "" && strings.HasPrefix(r.Login.Via, conn+".") // via — "<коннектор>.<действие>"
}

// loginForm — простой вход по форме учётки.
func loginForm(r abi.ExecRequest) (worker.LoginForm, error) {
	acc, err := account(r)
	if err != nil {
		return worker.LoginForm{}, err
	}
	t, _ := r.Args["timeout_ms"].(float64)
	form := worker.LoginForm{Bind: acc.Bind, SuccessPrefix: acc.Success, Timeout: time.Duration(t) * time.Millisecond}
	if acc.Submit != nil {
		form.SubmitSelector, form.SubmitPress = acc.Submit.Selector, acc.Submit.Press
	}
	for _, f := range acc.Fields {
		if codeField(f) {
			continue // код вводит человек через fill_secret source: human
		}
		lf, err := fieldOf(r, f, "")
		if err != nil {
			return form, err
		}
		form.Fields = append(form.Fields, lf)
	}
	return form, nil
}

// secretField — поле учётки для fill_secret: по имени (name или алиас
// секрета, в том числе без имени учётки: password для @emias.password) или
// ответ человека (source: human; спрашивает хост, ответ — в Revealed).
func secretField(r abi.ExecRequest) ([]string, worker.LoginField, error) {
	acc, err := account(r)
	if err != nil {
		return nil, worker.LoginField{}, err
	}
	name, sel := str(r.Args, "field"), str(r.Args, "selector")
	if str(r.Args, "source") == "human" {
		v := r.Revealed[abi.HumanValue]
		if pre, _ := r.Args["precheck"].(bool); pre {
			v = "-" // проверка до вопроса: значения ещё нет
		}
		if v == "" {
			return nil, worker.LoginField{}, abi.Errorf(abi.CodeSecretDenied, "no answer from the user")
		}
		// Поле для ответа человека задаёт владелец (поле кода учётки по имени)
		// или коннектор входа учётки (via); иначе — только input[type=password]:
		// агент не выбирает, в какое текстовое поле уйдёт код.
		if viaCaller(r) {
			if sel == "" {
				return nil, worker.LoginField{}, abi.Errorf(abi.CodeBadRequest, "fill_secret with source human needs selector")
			}
			return acc.Bind, worker.LoginField{Selector: sel, Value: v, Secret: true, AnyType: true}, nil
		}
		for _, f := range acc.Fields {
			if name == "" || f.Name != name || !codeField(f) || f.Selector == "" {
				continue
			}
			if sel != "" && sel != f.Selector {
				return nil, worker.LoginField{}, abi.Errorf(abi.CodeBadRequest, "code field %s of account %s: the owner set its selector (%s); omit selector", name, acc.Alias, f.Selector)
			}
			return acc.Bind, worker.LoginField{Selector: f.Selector, Value: v, Secret: true, AnyType: true}, nil
		}
		if sel == "" {
			return nil, worker.LoginField{}, abi.Errorf(abi.CodeBadRequest, "fill_secret with source human: set field (a code field of account %s) or selector of a password field", acc.Alias)
		}
		return acc.Bind, worker.LoginField{Selector: sel, Value: v, Secret: true}, nil
	}
	for _, f := range acc.Fields {
		if name == "" || !(f.Name == name || f.Secret == name || f.Secret == "@"+name || f.Secret == acc.Alias+"."+name) {
			continue
		}
		lf, err := fieldOf(r, f, sel)
		if err != nil {
			return nil, lf, err
		}
		return acc.Bind, lf, nil
	}
	return nil, worker.LoginField{}, abi.Errorf(abi.CodeNotFound, "account %s has no field %q", acc.Alias, name)
}

func actRequest(a map[string]any) worker.ActRequest {
	urlChange, _ := a["expect_url_change"].(bool)
	return worker.ActRequest{Selector: str(a, "selector"), Method: strings.ToLower(str(a, "method")), Text: str(a, "text"),
		Value: str(a, "value"), TimeoutMS: num(a, "timeout_ms"), ExpectSelector: str(a, "expect_selector"),
		ExpectText: str(a, "expect_text"), ExpectURLChange: urlChange, ExpectMS: num(a, "expect_timeout_ms")}
}

// EndCall освобождает страницу корневого действия (native.CallEnder).
func (b *Browser) EndCall(call string) {
	b.mu.Lock()
	cs, ok := b.sessions[call]
	delete(b.sessions, call)
	b.mu.Unlock()
	if !ok {
		return
	}
	cs.timer.Stop()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.s.Release()
}

// Verify — у действий браузера проверки нет (хост её не вызывает).
func (b *Browser) Verify(_ context.Context, _ *abi.Host, r abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, abi.Errorf(abi.CodeBadRequest, "action %s has no verification", r.Action)
}

// zero — пустое значение аргумента: "", 0, false, пустой список или объект.
func zero(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case float64:
		return t == 0
	case int:
		return t == 0
	case bool:
		return !t
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// idParamRe — имена параметров-идентификаторов (ehrId, patient_id, uid).
var idParamRe = regexp.MustCompile(`(?i)(^|[a-z_])(id|uid|guid)$`)

// longNum — значение похоже на номер записи (6 и больше цифр).
var longNum = regexp.MustCompile(`^\d{6,}$`)

// idParams — нескрытые параметры адресов, похожие на личные идентификаторы.
func idParams(reqs []worker.Request) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range reqs {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		for k, vs := range u.Query() {
			if seen[k] || len(vs) == 0 || vs[0] == "***" || vs[0] == "" {
				continue
			}
			if idParamRe.MatchString(k) || longNum.MatchString(vs[0]) {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// viaArg — служебный аргумент: действие пришло от коннектора входа в сеансе агента.
const viaArg = "_via"

// siteHint — отказ агенту по списку сайтов с командой для владельца (сайты
// агента — настройки браузера; сайты коннектора — его манифест).
func siteHint(err error) error {
	if !errors.Is(err, worker.ErrDomain) {
		return err
	}
	msg := err.Error()
	i := strings.LastIndex(msg, ": ")
	host := ""
	if i >= 0 {
		host = msg[i+2:]
	}
	if host == "" || strings.Contains(host, " ") {
		return err
	}
	return fmt.Errorf("%w (the owner adds the site: gate4ai sites allow %s — or Web UI → Connectors → browser)", err, host)
}
