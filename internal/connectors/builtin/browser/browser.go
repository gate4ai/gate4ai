// SPDX-License-Identifier: AGPL-3.0-only

// Package browser — встроенный компонент «Браузер» (runtime: builtin): воркер
// выделенного профиля Chrome (CDP, Stagehand) в процессе daemon. Агенту — чтение,
// снимок и сеанс страницы (open, observe, act, network, login, fill_secret,
// page_screenshot, close) на сайтах по спискам доступа; WASM-коннекторам — те
// же действия с открытой страницей через service_call на их сайтах. LLM внутри
// компонента нет: страницу читает сам агент.
package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"
	"github.com/gate4ai/gate4ai/sdk-go/domain"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"

	"github.com/gate4ai/gate4ai/internal/connectors/builtin/browser/worker"
)

// SessionIdle — страница вызывающего коннектора освобождается, если end_call
// не пришёл (хост упал): иначе воркер ждал бы её вечно.
var SessionIdle = 2 * time.Minute

// AgentIdle — сеанс агента без действий закрывается сам (агент забыл close).
var AgentIdle = 10 * time.Minute

// AgentWait — сколько вызов ждёт без --wait_for_browser: место в браузере,
// занятом другими сеансами, или свой сеанс, пока в нём идёт другое действие
// (дольше — ошибка busy или session_blocked с тем, кто занял, а не зависание).
var AgentWait = 5 * time.Second

// MaxSteps — предел записанных шагов сеанса агента (для фикстур тестов).
const MaxSteps = 500

// Browser — коннектор: действия агента без сеанса (read, screenshot —
// временная вкладка на время действия: открытые сеансы они не трогают), сеансы
// агентов (open → observe / act / network / page_screenshot →
// close; у каждого своя вкладка; сайты — из настроек) и те же действия для
// других коннекторов через service_call (страница на корневое действие,
// сайты — params.sites вызывающего).
type Browser struct {
	W *worker.Service

	mu       sync.Mutex
	sessions map[string]*callSession     // корневое действие → страница
	agents   map[string]*agentSession    // сеанс агента → страница
	closed   map[string]closedInfo       // недавно закрытые сеансы агентов: кто и почему
	create   sync.Mutex                  // страница корневого действия создаётся один раз
	emit     func(kind string, data any) // события хосту (кадры просмотра); nil — некуда
	events   chan event                  // очередь событий к emit (send)
}

type callSession struct {
	s      *worker.Session
	sites  domain.Sites
	opened bool
	url    string                      // адрес страницы после последнего действия (под mu)
	shown  atomic.Pointer[string]      // тот же адрес для владельца — без mu, который держит идущее действие
	stop   atomic.Pointer[chan string] // идущий handoff: ответ человека из просмотра (Owner "handoff")
	sid    string                      // id сеанса агента ("" — страница вызывающего коннектора): снимки — в файлы
	shots  int                         // снимков сеанса (номер в имени файла)
	timer  *time.Timer
	mu     sync.Mutex // действия одной страницы — по очереди
	owner  string     // кто держит страницу (для текста busy)
	since  time.Time
}

// agentSession — страница агента между его вызовами и записанные шаги.
type agentSession struct {
	callSession
	id        string
	agent     string // агент-владелец (ExecRequest.Agent): чужой агент сеанс не видит
	instance  string // его экземпляр (ExecRequest.AgentInstance): у сессий агента одного типа имя одно
	steps     []map[string]any
	truncated bool
	lock      chan struct{} // действия сеанса — по одному (lockAgent)

	// под Browser.mu
	closed  bool               // страница освобождена
	cancel  context.CancelFunc // отмена идущего действия
	running string             // идущее действие ("" — нет)
	started time.Time          // когда оно началось
	last    time.Time          // последнее действие: сеанс закроется в last + AgentIdle
	held    time.Time          // с какого времени сеанс держит владелец (нулевое — не держит)
	view    *sessionView       // просмотр владельцем (nil — не смотрят)
}

// setURL — адрес страницы после действия (под cs.mu).
func (cs *callSession) setURL(u string) {
	cs.url = u
	cs.shown.Store(&u)
}

// shownURL — последний адрес страницы (без cs.mu).
func (cs *callSession) shownURL() string {
	if u := cs.shown.Load(); u != nil {
		return *u
	}
	return ""
}

// Name — имя встроенного браузера (действия browser.*).
const Name = "browser"

// New — коннектор с воркером по умолчанию.
func New() *Browser { return &Browser{W: worker.New()} }

//go:embed manifest.yaml
var manifestYAML []byte

// Manifest — манифест встроенного браузера.
func Manifest() manifest.Manifest {
	m, err := manifest.Parse(manifestYAML)
	if err != nil {
		panic(err)
	}
	return m
}

// Close останавливает запущенный воркером Chrome (остановка daemon).
func (b *Browser) Close() error { return b.W.Close() }

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

// settings — настройки воркера из настроек коннектора.
func settings(r abi.ExecRequest) worker.Settings {
	s := r.Settings
	st := worker.Settings{Engine: str(s, "engine"), CDPURL: str(s, "cdp_url"), ChromePath: str(s, "chrome_path"),
		ProfileDir: str(s, "profile_dir")}
	st.MaxTextChars = num(s, "max_text_chars")
	st.RedactParams = strs(s["redact_params"])
	st.DownloadDir = str(s, "download_dir")
	st.Display = str(s, "display")
	return st
}

// downloadRequest — browser.download из аргументов и пределов владельца
// (max_download_bytes, download_types).
func downloadRequest(r abi.ExecRequest) worker.DownloadRequest {
	return worker.DownloadRequest{Selector: str(r.Args, "selector"), Text: str(r.Args, "text"), URL: str(r.Args, "url"),
		ResponseURLContains: str(r.Args, "response_url_contains"), TimeoutMS: num(r.Args, "timeout_ms"),
		MaxBytes: num(r.Settings, "max_download_bytes"), Types: strs(r.Settings["download_types"])}
}

// downloadResult — файл агенту: сам файл двоичным resource (как files.read),
// имя, тип, размер и sha256; копия — в доказательства (evidence).
func downloadResult(f worker.File, page string) abi.ExecResult {
	out := map[string]any{"name": f.Name, "mime_type": f.MIME, "size": len(f.Data), "sha256": f.SHA256, "via": f.Via}
	uri := "download:" + url.PathEscape(f.Name)
	if f.URL != "" {
		out["url"] = cleanURL(f.URL)
		uri = cleanURL(f.URL)
	}
	return abi.ExecResult{Structured: out, Content: []abi.Content{abi.Resource(uri, f.MIME, "", f.Data)},
		Evidence:  []abi.EvidenceItem{{Name: f.Name, MIME: f.MIME, Data: f.Data}},
		ResultRef: fmt.Sprintf("%s, %d bytes, sha256:%s", f.Name, len(f.Data), f.SHA256), Source: "web:" + cleanURL(page)}
}

// num — числовой аргумент: float64 из JSON, int / int64 (вызов из Go),
// json.Number (декодер с UseNumber); иное — 0.
func num(m map[string]any, k string) int {
	switch n := m[k].(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		f, _ := n.Float64()
		return int(f)
	}
	return 0
}

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
		return domain.Sites{Domains: strs(m[domainsKey]), Blocked: blocked}, nil
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

// Коды ошибок браузера сверх abi.
const (
	CodeRedirected = "redirected" // пока ждали (wait_for_*), страница ушла на страницу или сайт входа
	// CodeDialogOpen — на странице открыт JS-диалог (alert, confirm, prompt,
	// beforeunload): сообщение начинается с {type, message}; ответ — browser.dialog.
	CodeDialogOpen = "dialog_open"
	// CodeTimeout — действие не закончилось за свой срок (timeout_ms или
	// значение по умолчанию); в сообщении — состояние страницы.
	CodeTimeout = "timeout"
	// CodeSessionBlocked — в сеансе ещё идёт другое действие (или оно зависло):
	// кто, с какого времени, что делать.
	CodeSessionBlocked = "session_blocked"
	// CodeUnavailable — браузер на компьютере не запускается (нет Chrome,
	// дисплея, Chrome падает): чинит владелец, повтор не поможет.
	CodeUnavailable = "browser_unavailable"
	// CodeHeldByUser — сеанс взял под управление владелец (просмотр в Web UI):
	// действия агента в нём ждут, пока он не вернёт сеанс.
	CodeHeldByUser = "session_held_by_user"
)

// fail — ошибка воркера в коде ABI.
func fail(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, worker.ErrUnavailable):
		return abi.Errorf(CodeUnavailable, "%v. Do not retry: tell the user — they check and fix it with `gate4ai browser setup` on the computer with gate4ai", err)
	case errors.Is(err, worker.ErrDomain), errors.Is(err, worker.ErrFileType):
		return abi.Errorf(abi.CodeCapabilityDenied, "%v", err)
	case errors.Is(err, worker.ErrTooLarge):
		return abi.Errorf(abi.CodeLimit, "%v", err)
	case errors.Is(err, worker.ErrNotFound), errors.Is(err, worker.ErrNoEffect):
		return abi.Errorf(abi.CodeNotFound, "%v", err)
	case errors.Is(err, context.Canceled):
		return abi.Errorf(abi.CodeCancelled, "%v", err)
	case errors.Is(err, worker.ErrNoDialog):
		return abi.Errorf(abi.CodeNotFound, "%v", err)
	case errors.As(err, new(*worker.DialogError)):
		var de *worker.DialogError
		errors.As(err, &de)
		d, _ := json.Marshal(de.Dialog)
		return abi.Errorf(CodeDialogOpen, "%s — %v", d, de)
	case errors.Is(err, worker.ErrRedirected):
		return abi.Errorf(CodeRedirected, "%s", strings.TrimPrefix(err.Error(), worker.ErrRedirected.Error()+": "))
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
	case "read", "screenshot":
		res, err := b.agent(ctx, r)
		return res, fail(b.busy(r, siteHint(err)))
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
	switch r.Action { // аргументы — до страницы и сеанса
	case "get", "head", "post", "put", "patch", "delete":
		if r.Caller != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "browser.%s is for agents; a connector makes requests with http_request", r.Action)
		}
		if _, err := fetchRequest(r); err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
	case "act":
		if err := worker.CheckAct(actRequest(r.Args)); err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
	case "download":
		if err := worker.CheckDownload(downloadRequest(r)); err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
	case "handoff":
		if r.Caller != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "handoff is for agents")
		}
		if err := worker.CheckHandoff(handoffRequest(r.Args)); err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
	}
	if r.Caller == nil {
		res, err := b.agentPage(ctx, r)
		return res, fail(b.busy(r, siteHint(err)))
	}
	res, err := b.service(ctx, r)
	return res, fail(b.busy(r, err))
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

// agent — действие агента: временная вкладка того же профиля на время
// действия (страница, адрес и состояние сеансов не меняются).
func (b *Browser) agent(ctx context.Context, r abi.ExecRequest) (abi.ExecResult, error) {
	sites, err := agentSites(r)
	if err != nil {
		return abi.ExecResult{}, err
	}
	s, err := b.W.Tab(ctx, settings(r), AgentWait)
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
		if raw, ok, err := s.Raw(ctx, num(r.Args, "max_chars")); err != nil {
			return abi.ExecResult{}, err
		} else if ok {
			// JSON, текст: тело как прислал сервер, без просмотрщика Chrome.
			return abi.ExecResult{Structured: rawResult(page.URL, page.Title, raw), Content: []abi.Content{abi.Text(raw.Body)}, ResultRef: page.URL, Source: "web:" + page.URL}, nil
		}
		p, err := s.Observe(ctx, num(r.Args, "max_chars"))
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
	return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "unknown action %s", r.Action)
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
		return nil, abi.Errorf(abi.CodeCapabilityDenied, "%s allows no sites (its sites list is empty)", r.Caller.Connector)
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
	cs := &callSession{s: s, sites: sites, owner: "connector " + r.Caller.Connector, since: time.Now()}
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
	var res abi.ExecResult
	var err error
	switch r.Action {
	case "dialog", "handoff":
		// Ответ на диалог — при открытом диалоге; handoff ждёт человека, который
		// в окне Chrome сам ответит на диалог.
		res, err = cs.do(ctx, r)
	default:
		// Открытый диалог отвечает dialog_open сразу, открывшийся во время
		// действия прерывает его; beforeunload при open / reload, о которых
		// просит вызывающий, принимается.
		err = cs.s.Run(ctx, r.Action == "open" || r.Action == "reload", func(ctx context.Context) error {
			var err error
			res, err = cs.do(ctx, r)
			return err
		})
	}
	if errors.Is(err, worker.ErrRedirected) {
		if u, uerr := cs.s.Engine.URL(ctx); uerr == nil {
			cs.setURL(u)
			if acc := loginRequired(ctx, cs.s, u, r.Accounts); acc != "" {
				err = fmt.Errorf("%w; account %s is available: browser.login --account %s", err, acc, acc)
			}
		}
	}
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
		cs.opened = true
		cs.setURL(p.URL)
		w := waitOf(r.Args, str(r.Args, "url"))
		if err := cs.s.WaitFor(ctx, w); err != nil {
			return abi.ExecResult{}, err
		}
		res := map[string]any{"url": cleanURL(p.URL), "title": p.Title}
		if p.StillLoading && w.Selector == "" && w.Text == "" && w.URL == "" {
			res["still_loading"] = true // дождаться содержимого: observe --wait_for_text / --wait_for_selector
		}
		return abi.ExecResult{Structured: res, ResultRef: cleanURL(p.URL)}, nil
	case "observe":
		if err := cs.s.WaitFor(ctx, waitOf(r.Args, cs.url)); err != nil {
			return abi.ExecResult{}, err
		}
		if raw, ok, err := cs.s.Raw(ctx, num(r.Args, "max_chars")); err != nil {
			return abi.ExecResult{}, err
		} else if ok {
			// JSON, текст: тело как прислал сервер (элементов и структуры у него нет).
			u, _ := cs.s.Engine.URL(ctx)
			title, _ := cs.s.Engine.Title(ctx)
			cs.setURL(u)
			return abi.ExecResult{Structured: rawResult(cleanURL(u), title, raw), ResultRef: cleanURL(u), Source: "web:" + cleanURL(u)}, nil
		}
		p, err := cs.s.Observe(ctx, num(r.Args, "max_chars"))
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.setURL(p.URL)
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
		if el, _ := r.Args["elements"].(bool); el || len(strs(r.Args["kinds"])) > 0 {
			els, err := cs.s.Elements(ctx, root, strs(r.Args["kinds"]))
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
		shot, _ := r.Args["screenshot"].(bool)
		res, err := cs.s.Act(ctx, actRequest(r.Args))
		if errors.Is(err, worker.ErrNoEffect) || shot && errors.Is(err, worker.ErrNotFound) {
			return abi.ExecResult{}, cs.failShot(ctx, err)
		}
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.setURL(res.URL)
		out := map[string]any{"url": cleanURL(res.URL), "text": res.Text}
		if res.Value != nil {
			out["value"] = res.Value
		}
		if res.Changes != nil {
			out["changes"] = res.Changes
		}
		ex := abi.ExecResult{Structured: out, ResultRef: cleanURL(res.URL)}
		if shot {
			info, sr, err := cs.capture(ctx, worker.ShotRequest{})
			if err != nil {
				return abi.ExecResult{}, err
			}
			out["screenshot"] = info
			ex.Content, ex.Evidence = sr.Content, sr.Evidence
		}
		return ex, nil
	case "reload":
		u, err := cs.s.Reload(ctx)
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.setURL(u)
		if err := cs.s.WaitFor(ctx, waitOf(r.Args, u)); err != nil {
			return abi.ExecResult{}, err
		}
		title, _ := cs.s.Engine.Title(ctx)
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(u), "title": title}, ResultRef: cleanURL(u)}, nil
	case "handoff":
		hr := handoffRequest(r.Args)
		if pre, _ := r.Args["precheck"].(bool); pre {
			// хост проверяет до уведомления человека: сеанс и страница есть
			return abi.ExecResult{Structured: map[string]any{"url": cleanURL(cs.url)}}, worker.CheckHandoff(hr)
		}
		cs.s.Activate(ctx) // человек видит вкладку сеанса
		stop := make(chan string, 1)
		cs.stop.Store(&stop)
		defer cs.stop.Store(nil)
		hr.Stop = stop
		res, err := cs.s.Handoff(ctx, hr)
		if err != nil {
			return abi.ExecResult{}, err
		}
		cs.setURL(res.URL)
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(res.URL), "status": res.Status}, ResultRef: res.Status}, nil
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
	case "get", "head", "post", "put", "patch", "delete":
		fr, err := fetchRequest(r)
		if err != nil {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "%v", err)
		}
		res, err := cs.s.FetchPage(ctx, fr)
		if err != nil {
			return abi.ExecResult{}, err
		}
		out := map[string]any{"status": res.Status, "url": cleanURL(res.URL), "size": res.Size}
		if len(res.Headers) > 0 {
			out["headers"] = res.Headers
		}
		if res.Redirect {
			out["redirect"] = true
		}
		bodyFields(out, res.Body)
		// Квитанция: метод, адрес, статус и размер ответа.
		ref := fmt.Sprintf("%s %s → %d, %d bytes", fr.Method, cleanURL(fr.URL), res.Status, res.Size)
		return abi.ExecResult{Structured: out, ResultRef: ref, Source: "web:" + cleanURL(res.URL)}, nil
	case "download":
		f, err := cs.s.Download(ctx, downloadRequest(r))
		if u, uerr := cs.s.Engine.URL(ctx); uerr == nil {
			cs.setURL(u)
		}
		if err != nil {
			return abi.ExecResult{}, err
		}
		return downloadResult(f, cs.url), nil
	case "page_screenshot":
		sel := str(r.Args, "selector")
		view, _ := r.Args["viewport"].(bool)
		full, _ := r.Args["full_page"].(bool)
		if n := btoi(sel != "") + btoi(view) + btoi(full); n > 1 {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "pass one of selector, viewport and full_page")
		}
		info, res, err := cs.capture(ctx, worker.ShotRequest{Selector: sel, FullPage: full})
		if err != nil {
			return abi.ExecResult{}, err
		}
		info["url"] = cleanURL(cs.url)
		res.Structured = info
		return res, nil
	case "dialog":
		accept, _ := r.Args["accept"].(bool)
		dismiss, _ := r.Args["dismiss"].(bool)
		if accept == dismiss {
			return abi.ExecResult{}, abi.Errorf(abi.CodeBadRequest, "dialog: pass --accept or --dismiss")
		}
		d, u, err := cs.s.HandleDialog(ctx, accept, str(r.Args, "text"))
		if err != nil {
			return abi.ExecResult{}, err
		}
		if u != "" {
			cs.setURL(u)
		}
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(cs.url), "dialog": d, "accepted": accept}, ResultRef: d.Type}, nil
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
		cs.setURL(res.URL)
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
		cs.setURL(res.URL)
		return abi.ExecResult{Structured: map[string]any{"url": cleanURL(res.URL), "origin": res.Origin}, ResultRef: res.Origin}, nil
	}
	return abi.ExecResult{}, fmt.Errorf("unknown action %s", r.Action)
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
	form := worker.LoginForm{Bind: acc.Bind, SuccessPrefix: acc.Success, Timeout: time.Duration(num(r.Args, "timeout_ms")) * time.Millisecond}
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
		Value: str(a, "value"), DelayMS: num(a, "delay_ms"), Submit: str(a, "submit"), TimeoutMS: num(a, "timeout_ms"),
		ExpectSelector: str(a, "expect_selector"), ExpectText: str(a, "expect_text"), ExpectURLChange: urlChange, ExpectMS: num(a, "expect_timeout_ms")}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func handoffRequest(a map[string]any) worker.HandoffRequest {
	change, _ := a["until_url_change"].(bool)
	return worker.HandoffRequest{UntilURLChange: change, UntilSelector: str(a, "until_selector"),
		Timeout: time.Duration(num(a, "timeout_ms")) * time.Millisecond}
}

// PageURL — адрес открытой страницы: сеанса агента session или страницы
// корневого вызова коннектора call (pipeline.URLResolver); "" — страницы нет.
func (b *Browser) PageURL(session, call string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if session != "" {
		if as := b.agents[session]; as != nil && !as.closed {
			return as.url
		}
		return ""
	}
	if cs := b.sessions[call]; cs != nil {
		return cs.url
	}
	return ""
}

// EndCall освобождает страницу корневого действия (wasm.CallEnder).
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

// siteHint — отказ агенту по списку сайтов с командой для владельца и
// запросом доступа для агента (сайты агента — настройки браузера; сайты
// коннектора — его манифест).
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
	return fmt.Errorf("%w (the owner opens the site: gate4ai access sites set %s allow — or Web UI → Browser; an agent asks the owner for it: "+
		`gate4ai gate4ai change_policy --summary "open %s" --changes '[{"op":"set_site_access","domain":"%s","access":"allow"}]'`+
		"; the sites open now: gate4ai gate4ai policy_summary)", err, host, host, host)
}
