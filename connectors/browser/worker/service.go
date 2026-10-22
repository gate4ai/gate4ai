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

// Service — браузерный воркер: один движок между вызовами, одна сессия за
// раз — страница не делится между вызовами.
type Service struct {
	NewEngine EngineFactory
	Launch    func(ctx context.Context, chrome, profile string, extra ...string) (string, *os.Process, error)

	mu   sync.Mutex
	slot chan struct{} // одна сессия за раз
	exec *Executor
	key  string
	cdp  string // адрес DevTools текущего Chrome
	proc *os.Process
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

func (s *Service) executor(ctx context.Context, st Settings) (*Executor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprint(st.CDPURL, st.ChromePath, st.ProfileDir, st.Engine, st.Model, st.ModelKey)
	reuse := ""
	if s.exec != nil && s.key == key {
		if alive(ctx, s.exec.Engine) {
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
			s.exec = &Executor{Engine: eng, Pacing: NoPacing}
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
	s.exec = &Executor{Engine: eng, Pacing: NoPacing}
	s.key, s.cdp = key, cdp
	return s.exec, nil
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
	return Page{URL: final, Title: title}, nil
}

// Observe — адрес, заголовок и текст страницы (до max символов).
func (ss *Session) Observe(ctx context.Context, max int) (Page, error) {
	if max <= 0 || max > ss.max {
		max = ss.max
	}
	u, title, text, trunc, err := ss.Executor.Observe(ctx, max)
	return Page{URL: u, Title: title, Text: text, Truncated: trunc}, err
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
