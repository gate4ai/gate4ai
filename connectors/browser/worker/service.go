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
	DownloadDir  string   // куда Chrome молча сохраняет обычные загрузки (по умолчанию — папка загрузок пользователя)
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

// Service — браузерный воркер: один движок между вызовами, одна сессия за
// раз — страница не делится между вызовами; одноразовые вызовы — во временных
// вкладках того же Chrome (Tab), не дольше MaxTabs одновременно.
type Service struct {
	NewEngine EngineFactory
	Launch    func(ctx context.Context, chrome, profile string, extra ...string) (string, *os.Process, error)

	mu   sync.Mutex
	slot chan struct{} // одна сессия за раз
	tabs chan struct{} // временные вкладки одноразовых вызовов (MaxTabs)
	exec *Executor
	key  string
	cdp  string // адрес DevTools текущего Chrome
	proc *os.Process
	dl   *Downloads // загрузки без диалога (download.go); nil — подключиться не удалось
	dlOf string     // Chrome, к которому подключение для загрузок не удалось (сообщено в лог)
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
	if st.DownloadDir == "" {
		st.DownloadDir = UserDownloadDir()
	}
	return st
}

// settingsKey — настройки, при смене которых движок и Chrome пересоздаются.
func settingsKey(st Settings) string {
	return fmt.Sprint(st.CDPURL, st.ChromePath, st.ProfileDir, st.Engine, st.Model, st.ModelKey)
}

func (s *Service) executor(ctx context.Context, st Settings) (*Executor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := settingsKey(st)
	reuse := ""
	if s.exec != nil && s.key == key {
		if alive(ctx, s.exec.Engine) {
			s.downloads(ctx, s.cdp, st.DownloadDir)
			return s.exec, nil
		}
		// Вкладку или окно Chrome закрыли, Chrome упал: подключение мёртвое.
		// Сначала — к тому же Chrome (закрыта только вкладка), иначе запуск заново.
		reuse = s.cdp
	}
	if s.exec != nil {
		_ = s.exec.Engine.Close()
		s.exec = nil
	}
	if reuse != "" {
		if eng, err := s.NewEngine(ctx, st, reuse); err == nil {
			s.exec = &Executor{Engine: eng}
			s.downloads(ctx, reuse, st.DownloadDir)
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
			// Chrome запущен с другими настройками (движок, профиль): профиль
			// занят им, новый запуск передал бы адрес ему и закрылся.
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
	s.downloads(ctx, cdp, st.DownloadDir)
	return s.exec, nil
}

// downloads — подключение для загрузок к Chrome по адресу cdp (под s.mu):
// живое к тому же Chrome остаётся, иначе — новое. Не вышло (движок без
// DevTools браузера, фейк в тестах) — без него: download по клику не работает.
func (s *Service) downloads(ctx context.Context, cdp, dir string) {
	if s.dl.Alive() && s.dl.c.http == cdp {
		if s.dl.dir != dir && s.dl.behave(ctx, "allow", dir) == nil {
			s.dl.dir = dir
		}
		return
	}
	if s.dl != nil {
		s.dl.Close()
		s.dl = nil
	}
	d, err := DialDownloads(ctx, cdp, dir)
	if err != nil {
		if s.dlOf != cdp {
			s.dlOf = cdp
			fmt.Fprintln(os.Stderr, "browser: downloads without a dialog are off:", err)
		}
		return
	}
	s.dl = d
}

// downloadsConn — подключение для загрузок (nil — нет).
func (s *Service) downloadsConn() *Downloads {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dl.Alive() {
		return nil
	}
	return s.dl
}

// ErrBusy — страницу держит другая сессия дольше, чем вызов готов ждать.
var ErrBusy = errors.New("the browser is busy with another session")

func (s *Service) slotCh() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slot == nil {
		s.slot = make(chan struct{}, 1)
	}
	return s.slot
}

// MaxTabs — сколько временных вкладок (одноразовые read / screenshot /
// extract) открыто одновременно, кроме страницы сессии.
var MaxTabs = 4

// ErrTabLimit — все временные вкладки заняты дольше, чем вызов готов ждать.
var ErrTabLimit = errors.New("too many temporary browser tabs are open")

// Tabber — движок умеет открыть временную вкладку того же Chrome.
type Tabber interface {
	NewTab(ctx context.Context) (Engine, error)
}

func (s *Service) tabsCh() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tabs == nil {
		s.tabs = make(chan struct{}, MaxTabs)
	}
	return s.tabs
}

// Tab — временная вкладка того же Chrome и профиля для одноразового вызова:
// страницу сессии (слот Acquire) не занимает и не трогает; Release закрывает
// вкладку. Пока заняты все MaxTabs вкладок, ждёт не дольше wait (ErrTabLimit;
// 0 — до конца ctx).
func (s *Service) Tab(ctx context.Context, st Settings, wait time.Duration) (*Session, error) {
	tabs := s.tabsCh()
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case tabs <- struct{}{}:
	case <-timeout:
		return nil, ErrTabLimit
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", ErrTabLimit, ctx.Err())
	}
	st = normalize(st)
	main, err := s.mainEngine(ctx, st)
	if err != nil {
		<-tabs
		return nil, err
	}
	t, ok := main.(Tabber)
	if !ok {
		<-tabs
		return nil, errors.New("the browser engine cannot open a temporary tab")
	}
	eng, err := t.NewTab(ctx)
	if err != nil {
		<-tabs
		return nil, err
	}
	return &Session{Executor: &Executor{Engine: eng, RedactParams: st.RedactParams}, s: s, max: st.MaxTextChars, tab: tabs}, nil
}

// mainEngine — движок страницы сессии, от которого открываются вкладки.
// Страница свободна — Chrome запускается или переподключается как в Acquire;
// занята сессией — берётся её движок как есть (перезапуск оборвал бы сессию).
func (s *Service) mainEngine(ctx context.Context, st Settings) (Engine, error) {
	slot := s.slotCh()
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
		x, err := s.executor(ctx, st)
		if err != nil {
			return nil, err
		}
		return x.Engine, nil
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exec == nil || s.key != settingsKey(st) {
		// Сессия открыта с другими настройками браузера (их сменили на ходу).
		return nil, ErrBusy
	}
	return s.exec.Engine, nil
}

// ProbeTimeout — сколько ждать ответа Chrome при проверке, что он жив.
var ProbeTimeout = 3 * time.Second

// alive — движок отвечает (Chrome не закрыт, соединение живо).
func alive(ctx context.Context, e Engine) bool {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	_, err := e.URL(ctx)
	return err == nil
}

// Acquire — сессия (одна за раз) с настройками st; Release освобождает её.
// Пока страницу держит другая сессия, ждёт не дольше wait (ErrBusy; 0 — до
// конца ctx).
func (s *Service) Acquire(ctx context.Context, st Settings, wait time.Duration) (*Session, error) {
	slot := s.slotCh()
	var timeout <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case slot <- struct{}{}:
	case <-timeout:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", ErrBusy, ctx.Err())
	}
	st = normalize(st)
	x, err := s.executor(ctx, st)
	if err != nil {
		<-slot
		return nil, err
	}
	x.RedactParams = st.RedactParams
	return &Session{Executor: x, s: s, max: st.MaxTextChars}, nil
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
	if s.dl != nil {
		s.dl.Close()
		s.dl = nil
	}
	if s.proc != nil {
		StopChrome(s.proc, 3*time.Second)
		s.proc = nil
	}
	return nil
}

// Session — страница одного вызова: действия Executor (Act, Extract, Network,
// Login…) и освобождение страницы.
type Session struct {
	*Executor
	s    *Service
	max  int
	done bool
	tab  chan struct{} // временная вкладка (Tab): Release закрывает её
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

// Release освобождает сессию.
func (ss *Session) Release() {
	if ss.done {
		return
	}
	ss.done = true
	if ss.tab != nil {
		_ = ss.Executor.Engine.Close()
		<-ss.tab
		return
	}
	ss.Executor.Sites, ss.Executor.LoginOrigins = domain.Sites{}, nil
	<-ss.s.slotCh()
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
