// SPDX-License-Identifier: AGPL-3.0-only

package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gate4ai/sdk-go/abi"
	"github.com/gate4ai/sdk-go/hostfuncs"
)

// Settings — раздел browser конфигурации (настройки службы, не коннектора).
type Settings struct {
	Engine       string // stagehand (по умолчанию) | cdp
	CDPURL       string // уже запущенный Chrome; иначе служба запускает его сама
	ChromePath   string
	ProfileDir   string // выделенный профиль (по умолчанию ~/.gate4ai-chrome)
	Model        string // модель для extract, например "openai/gpt-5.4-mini"
	ModelKey     string // алиас Vault ключа модели
	MaxTextChars int
}

// EngineFactory создаёт движок (подменяется в тестах); modelKey — уже значение.
type EngineFactory func(ctx context.Context, s Settings, cdpURL, modelKey string) (Engine, error)

// DefaultEngine — Stagehand или прямой CDP.
func DefaultEngine(ctx context.Context, s Settings, cdpURL, modelKey string) (Engine, error) {
	if s.Engine == "cdp" {
		return DialCDP(ctx, cdpURL)
	}
	return DialStagehand(ctx, StagehandConfig{CDPURL: cdpURL, ModelName: s.Model, APIKey: modelKey})
}

// Service — браузерный воркер (hostfuncs.Browser): один движок между вызовами,
// одна сессия (вызов коннектора) за раз — страница не делится между вызовами.
type Service struct {
	// Config — текущие настройки службы.
	Config func() Settings
	// Secret — значение секрета по алиасу (ключ модели).
	Secret    func(alias string) (string, error)
	NewEngine EngineFactory
	Launch    func(ctx context.Context, chrome, profile string) (string, *os.Process, error)

	session sync.Mutex // одна сессия за раз
	mu      sync.Mutex
	exec    *Executor
	key     string
	proc    *os.Process
}

// New — служба с движками по умолчанию.
func New(cfg func() Settings, secret func(string) (string, error)) *Service {
	return &Service{Config: cfg, Secret: secret, NewEngine: DefaultEngine, Launch: Launch}
}

func (s *Service) settings() Settings {
	st := Settings{}
	if s.Config != nil {
		st = s.Config()
	}
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
	if s.exec != nil && s.key == key {
		return s.exec, nil
	}
	if s.exec != nil {
		_ = s.exec.Engine.Close()
		s.exec = nil
	}
	modelKey := ""
	if st.ModelKey != "" && s.Secret != nil {
		v, err := s.Secret(st.ModelKey)
		if err != nil {
			return nil, fmt.Errorf("browser.model_key: %w", err)
		}
		modelKey = v
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
		u, proc, err := s.Launch(ctx, chrome, st.ProfileDir)
		if err != nil {
			return nil, err
		}
		cdp, s.proc = u, proc
	}
	eng, err := s.NewEngine(ctx, st, cdp, modelKey)
	if err != nil {
		return nil, err
	}
	s.exec = &Executor{Engine: eng, Pacing: NoPacing}
	s.key = key
	return s.exec, nil
}

// Acquire — сессия на время вызова коннектора.
func (s *Service) Acquire(ctx context.Context) (hostfuncs.BrowserSession, error) {
	s.session.Lock()
	st := s.settings()
	x, err := s.executor(ctx, st)
	if err != nil {
		s.session.Unlock()
		return nil, err
	}
	return &session{s: s, x: x, max: st.MaxTextChars}, nil
}

// Close закрывает движок.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exec != nil {
		_ = s.exec.Engine.Close()
		s.exec = nil
	}
	return nil
}

type session struct {
	s    *Service
	x    *Executor
	max  int
	done bool
}

func (ss *session) Open(ctx context.Context, url string, allowed []string) (abi.BrowserPage, error) {
	ss.x.Allowed = allowed
	final, err := ss.x.Open(ctx, url)
	if err != nil {
		return abi.BrowserPage{}, err
	}
	title, _ := ss.x.Engine.Title(ctx)
	return abi.BrowserPage{URL: final, Title: title}, nil
}

func (ss *session) Observe(ctx context.Context, max int) (abi.BrowserPage, error) {
	if max <= 0 || max > ss.max {
		max = ss.max
	}
	u, title, text, trunc, err := ss.x.Observe(ctx, max)
	return abi.BrowserPage{URL: u, Title: title, Text: text, Truncated: trunc}, err
}

func (ss *session) Extract(ctx context.Context, instruction string) (any, error) {
	return ss.x.Extract(ctx, instruction)
}

func (ss *session) ScreenshotElement(ctx context.Context, selector string) ([]byte, error) {
	return ss.x.Screenshot(ctx, selector)
}

func (ss *session) Act(ctx context.Context, r abi.BrowserRequest) (abi.BrowserActResponse, error) {
	return ss.x.Act(ctx, r)
}

func (ss *session) Release() {
	if ss.done {
		return
	}
	ss.done = true
	ss.x.Allowed = nil
	ss.s.session.Unlock()
}
