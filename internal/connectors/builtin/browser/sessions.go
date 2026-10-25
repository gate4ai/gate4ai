// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser/worker"
)

// Сроки действий сеанса агента: действие целиком (переход, ожидание,
// диалоги) не дольше своего срока — по его истечении ответ timeout с
// состоянием страницы, а не зависание.
var (
	// DefaultActionTimeout — срок действия без timeout_ms.
	DefaultActionTimeout = 45 * time.Second
	// ActionGrace — сколько после срока ждать, что действие ответит на отмену;
	// не ответило — ответ timeout, а сеанс занят им (session_blocked), пока оно не кончится.
	ActionGrace = 3 * time.Second
	// StateTimeout — сколько ждать состояния страницы для ответа timeout.
	StateTimeout = 3 * time.Second
	// MaxBrowserWait — предел open --wait_for_browser (меньше limits.timeout_s пакета).
	MaxBrowserWait = 9 * time.Minute
	// ClosedKeep — сколько помнить, кто и почему закрыл сеанс.
	ClosedKeep = time.Hour
)

// actionTimeout — срок действия сеанса: timeout_ms (у act — ещё ожидание
// результата и само действие, у download — ещё ожидание элемента), иначе
// DefaultActionTimeout; handoff ждёт человека, вход —
// сайт входа.
func actionTimeout(r abi.ExecRequest) time.Duration {
	ms := time.Duration(num(r.Args, "timeout_ms")) * time.Millisecond
	switch r.Action {
	case "handoff":
		if ms <= 0 {
			ms = worker.HandoffDefault
		}
		return ms + 30*time.Second
	case "login", "fill_secret":
		return ms + 90*time.Second
	case "act":
		if ms <= 0 {
			ms = 10 * time.Second
		}
		exp := time.Duration(num(r.Args, "expect_timeout_ms")) * time.Millisecond
		if exp <= 0 {
			exp = 5 * time.Second
		}
		return ms + exp + 20*time.Second
	case "download":
		if ms <= 0 {
			ms = worker.DefaultDownloadWait
		}
		return worker.DownloadElementWait + ms + 20*time.Second
	case "dialog":
		return 15 * time.Second
	}
	if ms > 0 {
		return ms
	}
	return DefaultActionTimeout
}

// browserWait — сколько open ждёт места в браузере: --wait_for_browser
// (10m, 90s, число — секунды), иначе AgentWait.
func browserWait(a map[string]any) (time.Duration, error) {
	v := strings.TrimSpace(str(a, "wait_for_browser"))
	if v == "" {
		return AgentWait, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		var n float64
		if _, serr := fmt.Sscanf(v, "%g", &n); serr != nil || fmt.Sprint(n) != v {
			return 0, fmt.Errorf("wait_for_browser: %q is not a duration (e.g. 10m, 90s)", v)
		}
		d = time.Duration(n * float64(time.Second))
	}
	if d < 0 || d > MaxBrowserWait {
		return 0, fmt.Errorf("wait_for_browser must be 0..%s", MaxBrowserWait)
	}
	return d, nil
}

// closedInfo — кто и почему закрыл сеанс: ответ на его id после закрытия
// (только владельцу — тому же агенту и его экземпляру).
type closedInfo struct {
	agent, instance, reason string
	at                      time.Time
}

// ownedBy — сеанс вызывающего: тот же агент и тот же его экземпляр (сессия
// Claude Code, процесс агента). Экземпляр вызывающего неизвестен (удалённый
// вызов, скрипт с токеном, владелец от имени агента) — сеансы всех
// экземпляров агента.
func (as *agentSession) ownedBy(r abi.ExecRequest) bool {
	return as.agent == r.Agent && (r.AgentInstance == "" || as.instance == r.AgentInstance)
}

// who — агент и его экземпляр для текста ошибок: «claude-code (session 3b68c888…)».
func who(agent, instance string) string {
	if agent == "" {
		agent = "an agent"
	}
	if instance == "" {
		return agent
	}
	k, v, _ := strings.Cut(instance, ":")
	if len(v) > 8 {
		v = v[:8] + "…"
	}
	return agent + " (" + strings.TrimSpace(k+" "+v) + ")"
}

func clock(t time.Time) string { return t.Local().Format("15:04:05") }

// agentPage — сеанс агента: open без session начинает сеанс (своя вкладка,
// пока агент не вызовет close или сеанс не простоит AgentIdle), с session —
// переходит на его странице; остальные действия — на странице сеанса, по
// одному и не дольше своего срока. Каждое действие записывается шагом: close
// отдаёт шаги для фикстур тестов коннектора (hosttest.Replay).
func (b *Browser) agentPage(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	id := str(r.Args, "session")
	if all, _ := r.Args["all"].(bool); r.Action == "close" && all {
		return b.closeAll(r), nil
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
		if as != nil && !as.ownedBy(r) {
			as = nil // сеанс другого агента или другой его сессии: как неизвестный
		}
		b.mu.Unlock()
		if as == nil {
			return abi.ExecResult{}, b.closedErr(id, r)
		}
		if err := b.heldErr(as); err != nil {
			return abi.ExecResult{}, err
		}
		if r.Action == "close" {
			return b.closeSession(as, r.Args, "closed by browser.close")
		}
		if err := b.lockAgent(as); err != nil {
			return abi.ExecResult{}, err
		}
	}
	// Сеанс заперт (lockAgent или newAgentSession): действие идёт в своей
	// горутине; отпирает сеанс тот, кто получил результат, — этот вызов или,
	// если он уже ответил timeout, сама горутина, когда действие кончится.
	timeout := actionTimeout(r)
	actx, cancel := context.WithTimeout(ctx, timeout)
	now := time.Now()
	b.mu.Lock()
	as.cancel, as.running, as.started, as.last = cancel, r.Action, now, now
	as.timer.Reset(AgentIdle)
	b.mu.Unlock()
	b.stateChanged(as)
	finish := func() {
		b.mu.Lock()
		as.cancel, as.running, as.last = nil, "", time.Now()
		b.mu.Unlock()
		cancel()
		b.stateChanged(as) // до освобождения: следующее действие сеанса — уже после события
		<-as.lock
	}
	type outcome struct {
		res abi.ExecResult
		err error
	}
	done := make(chan outcome, 1)
	var hand sync.Mutex
	abandoned := false
	go func() {
		res, err := as.page(actx, r)
		if err != nil && errors.Is(actx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			err = b.timeoutErr(as, r, timeout)
		}
		as.record(r, res, err)
		hand.Lock()
		defer hand.Unlock()
		if abandoned {
			finish()
			return
		}
		done <- outcome{res, err}
	}()
	var o outcome
	select {
	case o = <-done:
	case <-actx.Done():
		t := time.NewTimer(ActionGrace)
		select {
		case o = <-done:
		case <-t.C:
			hand.Lock()
			select {
			case o = <-done:
			default:
				abandoned = true
			}
			hand.Unlock()
		}
		t.Stop()
		if abandoned {
			// Действие не отвечает на отмену: сеанс занят им, пока оно не
			// кончится (следующие вызовы — session_blocked с причиной).
			return abi.ExecResult{}, abi.Errorf(CodeTimeout, "browser.%s did not finish in %s and does not respond; the session %s is blocked until it ends: "+
				"try again later, or close it with browser.close --session %s and start a new one", r.Action, timeout, as.id, as.id)
		}
	}
	finish()
	if o.err != nil {
		if id == "" {
			b.closeAgent(as, "its first browser.open failed") // сеанс не начался
		} else if gone := b.gone(as, o.err); gone != nil {
			return abi.ExecResult{}, gone
		}
		return abi.ExecResult{}, o.err
	}
	if r.Action == "open" {
		o.res.Structured.(map[string]any)["session"] = as.id
	}
	return o.res, nil
}

// timeoutErr — действие не уложилось в срок: состояние страницы или
// открытый на ней диалог.
func (b *Browser) timeoutErr(as *agentSession, r abi.ExecRequest, timeout time.Duration) error {
	if d := as.s.Dialog(); d != nil {
		return abi.Errorf(CodeTimeout, "browser.%s did not finish in %s: %v", r.Action, timeout, &worker.DialogError{Dialog: *d})
	}
	ctx, cancel := context.WithTimeout(context.Background(), StateTimeout)
	defer cancel()
	return abi.Errorf(CodeTimeout, "browser.%s did not finish in %s; the page: %s — the session still works: look with browser.observe, "+
		"or give the action more time (timeout_ms)", r.Action, timeout, as.s.State(ctx))
}

// gone — после ошибки действия: Chrome закрыли или он упал — сеанс
// закрывается, ответ говорит об этом (nil — Chrome жив).
func (b *Browser) gone(as *agentSession, err error) error {
	var ae *abi.Error
	if errors.As(err, &ae) || errors.Is(err, context.Canceled) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), worker.ProbeTimeout+time.Second)
	defer cancel()
	if !as.s.Gone(ctx) {
		return nil
	}
	reason := "the browser was closed or crashed"
	b.closeAgent(as, reason)
	return abi.Errorf(abi.CodeNotFound, "browser session %s is closed: %s (%v); start a new one with browser.open", as.id, reason, err)
}

// lockAgent — очередь действий сеанса: ждёт не дольше AgentWait, пока
// кончится идущее действие; дольше — session_blocked с тем, что его держит.
func (b *Browser) lockAgent(as *agentSession) error {
	t := time.NewTimer(AgentWait)
	defer t.Stop()
	select {
	case as.lock <- struct{}{}:
	case <-t.C:
		return b.blockedErr(as)
	}
	b.mu.Lock()
	closed := as.closed
	b.mu.Unlock()
	if closed {
		// close --all, простой или падение браузера закрыли сеанс, пока действие ждало.
		<-as.lock
		return b.closedErr(as.id, abi.ExecRequest{Agent: as.agent, AgentInstance: as.instance})
	}
	return nil
}

// blockedErr — сеанс занят действием: каким, с какого времени, диалог.
func (b *Browser) blockedErr(as *agentSession) error {
	b.mu.Lock()
	running, started := as.running, as.started
	b.mu.Unlock()
	what := "another action"
	if running != "" {
		what = fmt.Sprintf("browser.%s, running since %s (%s)", running, clock(started), time.Since(started).Round(time.Second))
	}
	msg := fmt.Sprintf("browser session %s is busy with %s", as.id, what)
	if d := as.s.Dialog(); d != nil {
		msg += "; " + (&worker.DialogError{Dialog: *d}).Error()
	}
	return abi.Errorf(CodeSessionBlocked, "%s: wait for it and try again, or close the session with browser.close --session %s (it cancels the action)", msg, as.id)
}

func (b *Browser) newAgentSession(ctx context.Context, r abi.ExecRequest) (*agentSession, error) {
	sites, err := agentSites(r)
	if err != nil {
		return nil, err
	}
	wait, err := browserWait(r.Args)
	if err != nil {
		return nil, abi.Errorf(abi.CodeBadRequest, "%v", err)
	}
	s, err := b.W.Acquire(ctx, settings(r), wait)
	if err != nil {
		return nil, err
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	now := time.Now()
	id := "s-" + hex.EncodeToString(raw[:])
	as := &agentSession{callSession: callSession{s: s, sites: sites, sid: id, owner: who(r.Agent, r.AgentInstance), since: now},
		id: id, agent: r.Agent, instance: r.AgentInstance, lock: make(chan struct{}, 1), last: now}
	as.lock <- struct{}{} // первое действие (open) — у создателя
	as.timer = time.AfterFunc(AgentIdle, func() { b.closeIdle(as) })
	b.mu.Lock()
	if b.agents == nil {
		b.agents = map[string]*agentSession{}
	}
	b.agents[as.id] = as
	b.mu.Unlock()
	return as, nil
}

// closeIdle — сеанс простоял AgentIdle без действий (агент забыл close).
func (b *Browser) closeIdle(as *agentSession) {
	b.mu.Lock()
	running := as.running != "" || !as.held.IsZero() || as.view != nil
	if running {
		// Идёт долгое действие (handoff), сеанс держит или смотрит владелец: это не простой.
		as.timer.Reset(AgentIdle)
	}
	b.mu.Unlock()
	if !running {
		b.closeAgent(as, fmt.Sprintf("closed after %s without actions (idle)", AgentIdle))
	}
}

// closedErr — сеанса нет: закрыт (кем и почему — если его закрыли у этого
// агента), неизвестен или принадлежит другому агенту (ответ один и тот же —
// чужие id не подтверждаются).
func (b *Browser) closedErr(id string, r abi.ExecRequest) error {
	b.mu.Lock()
	ci, ok := b.closed[id]
	b.mu.Unlock()
	if ok && (&agentSession{agent: ci.agent, instance: ci.instance}).ownedBy(r) {
		return abi.Errorf(abi.CodeNotFound, "browser session %s was closed at %s: %s; start a new one with browser.open", id, clock(ci.at), ci.reason)
	}
	return abi.Errorf(abi.CodeNotFound, "browser session %s is closed or unknown; start a new one with browser.open", id)
}

// closeSession — browser.close: идущее действие отменяется; ответ — не позже
// AgentWait, а страница освобождается только после действия (освобождать её
// под идущим действием — гонка за Executor и Session).
func (b *Browser) closeSession(as *agentSession, args map[string]any, reason string) (abi.ExecResult, error) {
	b.mu.Lock()
	if as.cancel != nil {
		as.cancel()
	}
	b.mu.Unlock()
	t := time.NewTimer(AgentWait)
	defer t.Stop()
	select {
	case as.lock <- struct{}{}:
		b.closeAgent(as, reason)
		res, err := as.closeResult(args)
		<-as.lock
		return res, err
	case <-t.C:
	}
	b.markClosed(as, reason)
	go func() {
		as.lock <- struct{}{}
		as.timer.Stop()
		as.s.Release()
		<-as.lock
	}()
	return as.closeResult(args)
}

// closeAll закрывает сеансы вызывающего (ownedBy: этот экземпляр агента;
// потерянный id, зависший сеанс); сеансы других агентов и других экземпляров
// того же агента (вторая сессия Claude Code) не трогает. Идущие действия отменяются.
func (b *Browser) closeAll(r abi.ExecRequest) abi.ExecResult {
	b.mu.Lock()
	var mine []*agentSession
	for _, as := range b.agents {
		if as.ownedBy(r) {
			mine = append(mine, as)
		}
	}
	b.mu.Unlock()
	for _, as := range mine {
		_, _ = b.closeSession(as, nil, "closed by browser.close --all of "+who(r.Agent, r.AgentInstance))
	}
	return abi.ExecResult{Structured: map[string]any{"closed": true, "sessions": len(mine)}}
}

// closeAgent освобождает страницу сеанса агента (close, close --all, простой,
// неудачный open, падение браузера) и запоминает, кто и почему его закрыл.
func (b *Browser) closeAgent(as *agentSession, reason string) {
	if !b.markClosed(as, reason) {
		return
	}
	as.timer.Stop()
	as.s.Release()
}

// markClosed — сеанс больше не виден агенту (список, причина для closedErr);
// false — уже был закрыт. Страницу освобождает closeAgent.
func (b *Browser) markClosed(as *agentSession, reason string) bool {
	b.mu.Lock()
	if b.agents[as.id] == as {
		delete(b.agents, as.id)
	}
	if as.closed {
		b.mu.Unlock()
		return false
	}
	as.closed = true
	if as.cancel != nil {
		as.cancel()
	}
	if b.closed == nil {
		b.closed = map[string]closedInfo{}
	}
	now := time.Now()
	for id, ci := range b.closed {
		if now.Sub(ci.at) > ClosedKeep {
			delete(b.closed, id)
		}
	}
	b.closed[as.id] = closedInfo{agent: as.agent, instance: as.instance, reason: reason, at: now}
	b.mu.Unlock()
	// Владельцу: просмотр закрыт, сеанса больше нет.
	b.stopView(as)
	b.send("closed", map[string]any{"session": as.id, "reason": reason})
	return true
}

// busy — подсказка, кто держит браузер (чьи сеансы, с какого времени, когда
// закроются), и что сделать вместо ожидания.
func (b *Browser) busy(r abi.ExecRequest, err error) error {
	if errors.Is(err, worker.ErrTabLimit) {
		return abi.Errorf(abi.CodeLimit, "%v (%d at most; read and screenshot each use one while they run): try again when they finish, "+
			"or use your open session: browser.get --session <id> --url <url> (a request with the session's cookies) or "+
			"browser.open --session <id> --url <url> then browser.observe / page_screenshot", err, worker.MaxTabs)
	}
	if !errors.Is(err, worker.ErrBusy) {
		return err
	}
	b.mu.Lock()
	var held []string
	for _, as := range b.agents {
		whose := as.owner
		if as.ownedBy(r) && r.Caller == nil {
			whose = "yours (" + as.id + ")" // свой сеанс: можно продолжить в нём или закрыть
		}
		h := fmt.Sprintf("a session of %s open since %s, closes at %s if idle", whose, clock(as.since), clock(as.last.Add(AgentIdle)))
		if as.running != "" {
			h += fmt.Sprintf(" (browser.%s running since %s)", as.running, clock(as.started))
		}
		held = append(held, h)
	}
	for _, cs := range b.sessions {
		held = append(held, fmt.Sprintf("the page of %s since %s", cs.owner, clock(cs.since)))
	}
	b.mu.Unlock()
	sort.Strings(held)
	return abi.Errorf(abi.CodeLimit, "%v (%d at most): %s. Wait in the queue: browser.open --wait_for_browser 10m --url <url>; "+
		"or browser.read / screenshot --url <url> without session (a temporary tab); close your own with browser.close --session <id> (or --all)",
		err, worker.MaxSessions, strings.Join(held, "; "))
}
