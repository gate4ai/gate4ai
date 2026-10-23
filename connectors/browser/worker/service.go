// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/domain"
)

// Settings — настройки браузера (connectors.browser в конфигурации gate4ai).
type Settings struct {
	Engine       string // stagehand (по умолчанию) | cdp
	CDPURL       string // уже запущенный Chrome; иначе воркер запускает его сам
	ChromePath   string
	ProfileDir   string // выделенный профиль (по умолчанию ~/.gate4ai-chrome)
	Model        string // модель для extract, например "openai/gpt-5.4-mini"
	ModelKey     string // значение ключа модели (хост передаёт его по capabilities.reveal)
	MaxTextChars int
	RedactParams []string // параметры адресов и поля JSON, скрытые в network
}

// EngineFactory создаёт движок (подменяется в тестах).
type EngineFactory func(ctx context.Context, s Settings, cdpURL string) (Engine, error)

// DefaultEngine — Stagehand или прямой CDP.
func DefaultEngine(ctx context.Context, s Settings, cdpURL string) (Engine, error) {
	if s.Engine == "cdp" {
		return DialCDP(ctx, cdpURL)
	}
	return DialStagehand(ctx, StagehandConfig{CDPURL: cdpURL, ModelName: s.Model, APIKey: s.ModelKey})
}

// Service — браузерный воркер: один Chrome и движок между вызовами. Сессии
// (сеанс агента, корневой вызов коннектора) не делят страницу: первая
// занимает стартовую вкладку движка, следующие — свои вкладки того же Chrome
// и профиля, не больше MaxSessions одновременно; одноразовые вызовы — во
// временных вкладках (Tab), не больше MaxTabs.
type Service struct {
	NewEngine EngineFactory
	Launch    func(ctx context.Context, chrome, profile string, extra ...string) (string, *os.Process, error)

	mu       sync.Mutex
	exec     *Executor
	key      string
	cdp      string // адрес DevTools текущего Chrome
	proc     *os.Process
	gen      int           // поколение Chrome: растёт при каждом подключении заново
	mainBusy bool          // стартовую вкладку держит сессия
	sessions int           // открытые сессии (стартовая вкладка и свои)
	tabs     int           // временные вкладки одноразовых вызовов
	freed    chan struct{} // закрывается, когда освобождается сессия или вкладка
}

// New — воркер с движками по умолчанию.
func New() *Service {
	return &Service{NewEngine: DefaultEngine, Launch: Launch}
}

// normalize — значения по умолчанию.
func normalize(st Settings) Settings {
	if st.MaxTextChars <= 0 {
		st.MaxTextChars = 30000
	}
	if st.ProfileDir == "" {
		home, _ := os.UserHomeDir()
		st.ProfileDir = filepath.Join(home, ".gate4ai-chrome")
	}
	return st
}

// settingsKey — настройки, при смене которых движок и Chrome пересоздаются.
func settingsKey(st Settings) string {
	return fmt.Sprint(st.CDPURL, st.ChromePath, st.ProfileDir, st.Engine, st.Model, st.ModelKey)
}

// ErrSettingsChanged — настройки браузера сменили, пока открыты сессии или
// вкладки на прежнем Chrome: перезапуск оборвал бы их.
var ErrSettingsChanged = errors.New("the browser settings changed while sessions are open on the browser started with the old ones")

// executor — движок стартовой вкладки (через него открываются остальные);
// Chrome запускается или подключается заново, если его нет или он не
// отвечает. others — кроме вызывающего, открыты сессии или вкладки. Под s.mu.
func (s *Service) executor(ctx context.Context, st Settings, others bool) (*Executor, error) {
	key := settingsKey(st)
	reuse := ""
	if s.exec != nil && s.key == key {
		if alive(ctx, s.exec.Engine) {
			return s.exec, nil
		}
		// Вкладку или окно Chrome закрыли, Chrome упал: подключение мёртвое.
		// Сначала — к тому же Chrome (закрыта только вкладка), иначе запуск заново.
		reuse = s.cdp
	} else if s.exec != nil && others {
		return nil, ErrSettingsChanged
	}
	if s.exec != nil {
		_ = s.exec.Engine.Close()
		s.exec = nil
	}
	s.gen++ // прежние вкладки сессий — у прежнего подключения
	if reuse != "" {
		if eng, err := s.NewEngine(ctx, st, reuse); err == nil {
			s.exec = &Executor{Engine: eng}
			return s.exec, nil
		}
	}
	cdp := st.CDPURL
	if cdp == "" {
		chrome := st.ChromePath
		if chrome == "" {
			var err error
			if chrome, err = FindChrome(); err != nil {
				return nil, err
			}
		}
		if s.proc != nil {
			// Chrome запущен с другими настройками (движок, профиль) или не
			// отвечает: профиль занят им, новый запуск передал бы адрес ему и закрылся.
			StopChrome(s.proc, 3*time.Second)
			s.proc = nil
		}
		u, proc, err := s.Launch(ctx, chrome, st.ProfileDir, chromeFlags(st)...)
		if err != nil {
			return nil, err
		}
		cdp, s.proc = u, proc
	}
	eng, err := s.NewEngine(ctx, st, cdp)
	if err != nil {
		return nil, err
	}
	s.exec = &Executor{Engine: eng}
	s.key, s.cdp = key, cdp
	return s.exec, nil
}

// ErrBusy — все MaxSessions сессий заняты дольше, чем вызов готов ждать.
var ErrBusy = errors.New("the browser is busy with other sessions")

// MaxSessions — сколько сессий (вкладок сессий) открыто одновременно.
var MaxSessions = 4

// MaxTabs — сколько временных вкладок (одноразовые read / screenshot /
// extract) открыто одновременно, кроме вкладок сессий.
var MaxTabs = 4

// ErrTabLimit — все временные вкладки заняты дольше, чем вызов готов ждать.
var ErrTabLimit = errors.New("too many temporary browser tabs are open")

// Tabber — движок умеет открыть ещё одну вкладку того же Chrome
// (background — в фоне).
type Tabber interface {
	NewTab(ctx context.Context, background bool) (Engine, error)
}

// pinger — движок проверяет, что Chrome жив, запросом к браузеру, а не к
// странице (открытый на ней диалог не мешает).
type pinger interface {
	Ping(ctx context.Context) error
}

// activator — движок выводит свою вкладку вперёд.
type activator interface {
	Activate(ctx context.Context) error
}

// freedCh — канал освобождения (под s.mu).
func (s *Service) freedCh() chan struct{} {
	if s.freed == nil {
		s.freed = make(chan struct{})
	}
	return s.freed
}

// release — сессия или вкладка освобождена: ждущие пробуют снова.
func (s *Service) release(f func()) {
	s.mu.Lock()
	f()
	close(s.freedCh())
	s.freed = make(chan struct{})
	s.mu.Unlock()
}

// reserve ждёт места (ok(s) под s.mu — есть место, и занимает его), не дольше
// wait (0 — до конца ctx); иначе full.
func (s *Service) reserve(ctx context.Context, wait time.Duration, full error, ok func() bool) error {
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	for {
		s.mu.Lock()
		if ok() {
			s.mu.Unlock()
			return nil
		}
		ch := s.freedCh()
		s.mu.Unlock()
		select {
		case <-ch:
		case <-timeout:
			return full
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", full, ctx.Err())
		}
	}
}

// newTab — ещё одна вкладка того же Chrome от движка стартовой вкладки.
func (s *Service) newTab(ctx context.Context, st Settings, background bool) (Engine, int, error) {
	s.mu.Lock()
	x, err := s.executor(ctx, st, true)
	gen := s.gen
	s.mu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	t, ok := x.Engine.(Tabber)
	if !ok {
		return nil, 0, errors.New("the browser engine cannot open another tab")
	}
	eng, err := t.NewTab(ctx, background)
	return eng, gen, err
}

// Tab — временная вкладка того же Chrome и профиля для одноразового вызова:
// вкладки сессий не занимает и не трогает; Release закрывает её. Пока заняты
// все MaxTabs вкладок, ждёт не дольше wait (ErrTabLimit; 0 — до конца ctx).
func (s *Service) Tab(ctx context.Context, st Settings, wait time.Duration) (*Session, error) {
	if err := s.reserve(ctx, wait, ErrTabLimit, func() bool {
		if s.tabs >= MaxTabs {
			return false
		}
		s.tabs++
		return true
	}); err != nil {
		return nil, err
	}
	st = normalize(st)
	eng, gen, err := s.newTab(ctx, st, true)
	if err != nil {
		s.release(func() { s.tabs-- })
		return nil, err
	}
	if d, ok := eng.(Dialoger); ok {
		d.AutoDialogs() // во временной вкладке спросить некого: диалоги закрываются сами
	}
	return &Session{Executor: &Executor{Engine: eng, RedactParams: st.RedactParams}, s: s, max: st.MaxTextChars, kind: tabTemp, gen: gen}, nil
}

// ProbeTimeout — сколько ждать ответа Chrome при проверке, что он жив.
var ProbeTimeout = 3 * time.Second

// alive — движок отвечает (Chrome не закрыт, соединение живо).
func alive(ctx context.Context, e Engine) bool {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	if p, ok := e.(pinger); ok {
		return p.Ping(ctx) == nil
	}
	_, err := e.URL(ctx)
	return err == nil
}

// Acquire — сессия с настройками st: стартовая вкладка, если она свободна,
// иначе своя вкладка; Release освобождает её. Пока открыты MaxSessions
// сессий, ждёт не дольше wait (ErrBusy; 0 — до конца ctx).
func (s *Service) Acquire(ctx context.Context, st Settings, wait time.Duration) (*Session, error) {
	main := false
	if err := s.reserve(ctx, wait, ErrBusy, func() bool {
		if s.sessions >= MaxSessions {
			return false
		}
		s.sessions++
		main = !s.mainBusy
		s.mainBusy = s.mainBusy || main
		return true
	}); err != nil {
		return nil, err
	}
	st = normalize(st)
	undo := func() {
		s.release(func() {
			s.sessions--
			if main {
				s.mainBusy = false
			}
		})
	}
	if !main {
		eng, gen, err := s.newTab(ctx, st, false)
		if err != nil {
			undo()
			return nil, err
		}
		return &Session{Executor: &Executor{Engine: eng, RedactParams: st.RedactParams}, s: s, max: st.MaxTextChars, kind: tabSession, gen: gen}, nil
	}
	s.mu.Lock()
	x, err := s.executor(ctx, st, s.sessions > 1 || s.tabs > 0)
	gen := s.gen
	s.mu.Unlock()
	if err != nil {
		undo()
		return nil, err
	}
	x.RedactParams = st.RedactParams
	return &Session{Executor: x, s: s, max: st.MaxTextChars, gen: gen}, nil
}

// Close закрывает движок и Chrome, который воркер запустил сам (чужой Chrome
// по cdp_url остаётся): пакет останавливают — его окно не должно висеть.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exec != nil {
		if s.proc != nil {
			// Штатно: Chrome сохраняет cookies (сессию входа) на диск.
			if q, ok := s.exec.Engine.(interface{ QuitBrowser(context.Context) }); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				q.QuitBrowser(ctx)
				cancel()
			}
		}
		_ = s.exec.Engine.Close()
		s.exec = nil
	}
	if s.proc != nil {
		StopChrome(s.proc, 3*time.Second)
		s.proc = nil
	}
	return nil
}

// Вид страницы сессии.
const (
	tabMain    = iota // стартовая вкладка движка
	tabSession        // своя вкладка сессии
	tabTemp           // временная вкладка одноразового вызова
)

// Session — страница одного вызова: действия Executor (Act, Extract, Network,
// Login…) и освобождение страницы.
type Session struct {
	*Executor
	s    *Service
	max  int
	done bool
	kind int
	gen  int // поколение Chrome, в котором открыта страница
}

// Gone — Chrome сессии закрыт или упал (её вкладки больше нет): действия
// бесполезны, сессию надо закрыть.
func (ss *Session) Gone(ctx context.Context) bool {
	ss.s.mu.Lock()
	gen := ss.s.gen
	ss.s.mu.Unlock()
	return gen != ss.gen || !alive(ctx, ss.Executor.Engine)
}

// Activate — вкладка сессии впереди (handoff: человек видит её).
func (ss *Session) Activate(ctx context.Context) {
	if a, ok := ss.Executor.Engine.(activator); ok {
		_ = a.Activate(ctx)
	}
}

// Dialog — открытый на странице диалог (nil — нет).
func (ss *Session) Dialog() *Dialog {
	if d, ok := ss.Executor.Engine.(Dialoger); ok {
		return d.OpenDialog()
	}
	return nil
}

// Open переходит по URL; sites — разрешённые сайты, итоговый адрес после
// редиректов тоже проверяется.
func (ss *Session) Open(ctx context.Context, url string, sites domain.Sites) (Page, error) {
	ss.Executor.Sites = sites
	final, err := ss.Executor.Open(ctx, url)
	if err != nil {
		return Page{}, err
	}
	title, _ := ss.Executor.Engine.Title(ctx)
	return Page{URL: final, Title: title, StillLoading: ss.Executor.StillLoading}, nil
}

// Observe — адрес, заголовок и текст страницы (до max символов: 0 — предел
// настроек, больше MaxResponseChars — граница).
func (ss *Session) Observe(ctx context.Context, max int) (Page, error) {
	max, _ = Limits(max, ss.max)
	u, title, text, trunc, err := ss.Executor.Observe(ctx, max)
	return Page{URL: u, Title: title, Text: text, Truncated: trunc}, err
}

// Raw — верхний документ не HTML (JSON, текст): его тело до max символов
// (0 — по умолчанию: текст — предел настроек, JSON — не меньше
// DefaultJSONChars; больше MaxResponseChars — граница); ok — false у HTML-страницы.
func (ss *Session) Raw(ctx context.Context, max int) (Body, bool, error) {
	return ss.Executor.RawDocument(ctx, max, ss.max)
}

// FetchPage — запрос со страницы сеанса; тело ответа — до Max символов (0 —
// по умолчанию, как у Raw).
func (ss *Session) FetchPage(ctx context.Context, r FetchRequest) (FetchResponse, error) {
	r.TextMax = ss.max
	return ss.Executor.Fetch(ctx, r)
}

// ScreenshotElement — снимок элемента по CSS-селектору.
func (ss *Session) ScreenshotElement(ctx context.Context, selector string) ([]byte, error) {
	return ss.Executor.Screenshot(ctx, selector)
}

// SetLoginOrigins — origin страниц входа учёток вызывающего (разрешены вне сайтов).
func (ss *Session) SetLoginOrigins(origins []string) { ss.Executor.LoginOrigins = origins }

// Present — на странице есть видимый элемент по селектору.
func (ss *Session) Present(ctx context.Context, sel string) bool {
	ok, err := ss.Executor.present(ctx, sel, "")
	return err == nil && ok
}

// Release освобождает сессию: своя и временная вкладки закрываются,
// стартовая остаётся (диалог на ней закрывается — следующей сессии он не
// достанется).
func (ss *Session) Release() {
	if ss.done {
		return
	}
	ss.done = true
	if d, ok := ss.Executor.Engine.(Dialoger); ok {
		d.AutoDialogs()
	}
	switch ss.kind {
	case tabTemp:
		_ = ss.Executor.Engine.Close()
		ss.s.release(func() { ss.s.tabs-- })
	case tabSession:
		_ = ss.Executor.Engine.Close()
		ss.s.release(func() { ss.s.sessions-- })
	default:
		if d, ok := ss.Executor.Engine.(interface{ ManualDialogs() }); ok {
			d.ManualDialogs() // стартовая вкладка остаётся: диалоги снова ждут ответа
		}
		ss.Executor.Sites, ss.Executor.LoginOrigins = domain.Sites{}, nil
		ss.s.release(func() { ss.s.sessions--; ss.s.mainBusy = false })
	}
}

// chromeFlags — флаги запуска Chrome под движок. Расширение Stagehand
// подключается к DevTools со своим origin (chrome-extension://<id>), а Chrome
// с версии 111 такие подключения отклоняет; id расширения меняется при каждой
// распаковке, поэтому разрешить можно только все origin. Порт отладки слушает
// только 127.0.0.1, адрес websocket содержит случайный id браузера, который
// чужая страница прочитать не может (/json/version без CORS).
func chromeFlags(st Settings) []string {
	if st.Engine == "cdp" {
		return nil
	}
	return []string{"--remote-allow-origins=*"}
}
