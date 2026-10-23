// SPDX-License-Identifier: AGPL-3.0-only

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	stagehand "github.com/browserbase/stagehand/packages/sdk-go/v4"
)

// Stagehand — движок на Stagehand Go SDK v4 поверх нашего Chrome (ConnectLocalBrowser).
// Модель указывает пользователь (provider/model + ключ из Vault); обращения к
// Browserbase не включаются: API-ключ Browserbase не передаётся, SelfHeal выключен,
// телеметрия не задана. Act с естественным языком не используется — в нём нет точки
// перехвата политикой; изменения на страницах — только browser_act по селектору
// (Executor.Act).
//
// JS-диалоги SDK не показывает, а переход при открытом beforeunload в нём
// висит: вкладку дополнительно слушает прямое подключение CDP (aux, по id
// страницы — это id цели CDP) — диалоги, переключение вкладки, проверка Chrome.
type Stagehand struct {
	browser *stagehand.Browser
	client  *stagehand.Stagehand
	page    *stagehand.Page
	tab     bool   // вкладка NewTab: Close закрывает только её
	http    string // адрес DevTools
	aux     *CDP   // прямое подключение к вкладке (nil — не удалось)
}

// StagehandConfig — модель для extract.
type StagehandConfig struct {
	CDPURL    string
	ModelName string
	APIKey    string
	// Generate — собственный обработчик LLM (тесты, локальные модели).
	Generate stagehand.LLMGenerateFunc
}

// DialStagehand подключает SDK к уже запущенному выделенному профилю Chrome.
func DialStagehand(ctx context.Context, cfg StagehandConfig) (*Stagehand, error) {
	b, err := stagehand.ConnectLocalBrowser(ctx, stagehand.LocalBrowserConnectOptions{CDPURL: cfg.CDPURL})
	if err != nil {
		return nil, fmt.Errorf("stagehand connect: %w", err)
	}
	off := false
	opts := stagehand.CreateOptions{Browser: b, SelfHeal: &off, Generate: cfg.Generate}
	if cfg.ModelName != "" {
		key := cfg.APIKey
		opts.Model = &stagehand.ModelConfig{ModelName: stagehand.ModelName(cfg.ModelName), APIKey: &key}
	}
	cl, err := stagehand.Create(ctx, opts)
	if err != nil {
		_ = b.Close(ctx)
		return nil, fmt.Errorf("stagehand: %w", err)
	}
	bc, err := b.Context()
	if err != nil {
		return nil, err
	}
	pages, err := bc.Pages(ctx)
	if err != nil || len(pages) == 0 {
		return nil, errors.New("stagehand: no page in the browser context")
	}
	sh := &Stagehand{browser: b, client: cl, page: pages[0], http: cfg.CDPURL}
	sh.attach(ctx)
	return sh, nil
}

// attach — прямое подключение CDP к вкладке страницы (диалоги) и перехват
// обработчиков click для observe.
func (s *Stagehand) attach(ctx context.Context) {
	_ = s.page.AddInitScript(ctx, clickHookJS)
	if aux, err := AttachCDP(ctx, s.http, s.page.PageID()); err == nil {
		s.aux = aux
	}
}

func (s *Stagehand) Navigate(ctx context.Context, url string) error {
	_, err := s.page.Goto(ctx, url, nil)
	return err
}

func (s *Stagehand) URL(ctx context.Context) (string, error)   { return s.page.URL(ctx) }
func (s *Stagehand) Title(ctx context.Context) (string, error) { return s.page.Title(ctx) }

func (s *Stagehand) Text(ctx context.Context) (string, error) {
	var t string
	err := s.Eval(ctx, visibleTextJS+"(document.body)", &t)
	return t, err
}

func (s *Stagehand) Eval(ctx context.Context, expr string, out any) error {
	raw, err := s.page.Evaluate(ctx, expr)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (s *Stagehand) InsertText(ctx context.Context, text string) error {
	return s.page.Type(ctx, text, nil)
}

func (s *Stagehand) Type(ctx context.Context, text string, delay time.Duration) error {
	ms := float64(delay.Milliseconds())
	return s.page.Type(ctx, text, &stagehand.PageTypeOptions{Delay: &ms})
}

// Key — клавиша или сочетание; одиночный символ без модификаторов («,», «ж») —
// через Type: KeyPress SDK отдаёт странице для него keydown с пустым key.
func (s *Stagehand) Key(ctx context.Context, key string) error {
	if k, err := ParseKey(key); err == nil && !k.Ctrl && !k.Alt && !k.Meta && !k.Shift {
		if _, named := keyCodes[k.Name]; !named {
			return s.page.Type(ctx, k.Name, nil)
		}
	}
	return s.page.KeyPress(ctx, key, nil)
}

func (s *Stagehand) Screenshot(ctx context.Context, clip *Clip, full bool) ([]byte, error) {
	on := true
	switch {
	case full:
		return s.page.Screenshot(ctx, &stagehand.ScreenshotOptions{FullPage: &on})
	case clip == nil:
		return s.page.Screenshot(ctx, nil)
	}
	return s.page.Screenshot(ctx, &stagehand.ScreenshotOptions{Clip: &stagehand.PageScreenshotClip{X: clip.X, Y: clip.Y, Width: clip.W, Height: clip.H}})
}

type extracted struct {
	Answer string `json:"answer"`
}

func (s *Stagehand) Extract(ctx context.Context, instruction string) (any, error) {
	// Страница — явно: иначе SDK берёт активную, а это может быть чужая вкладка.
	r, err := stagehand.Extract[extracted](ctx, s.client, instruction, &stagehand.StagehandClientExtractOptions{Page: s.page})
	if err != nil {
		return nil, err
	}
	return r.Data.Answer, nil
}

// NewTab — ещё одна вкладка того же Chrome (тот же профиль и cookie) на
// общем подключении SDK; Close закрывает только её. background SDK не
// поддерживает: вкладка открывается впереди.
func (s *Stagehand) NewTab(ctx context.Context, _ bool) (Engine, error) {
	bc, err := s.browser.Context()
	if err != nil {
		return nil, err
	}
	p, err := bc.NewPage(ctx, "about:blank")
	if err != nil {
		return nil, fmt.Errorf("stagehand: new tab: %w", err)
	}
	t := &Stagehand{browser: s.browser, client: s.client, page: p, tab: true, http: s.http}
	t.attach(ctx)
	return t, nil
}

// CloseTimeout — сколько ждать закрытия вкладки или SDK.
var CloseTimeout = 5 * time.Second

func (s *Stagehand) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), CloseTimeout)
	defer cancel()
	if s.aux != nil {
		s.aux.AutoDialogs() // beforeunload при закрытии не держит вкладку
		defer s.aux.Close()
	}
	if s.tab {
		return s.page.Close(ctx)
	}
	return errors.Join(s.client.Close(ctx), s.browser.Close(ctx))
}

// Диалоги, проверка и переключение — через прямое подключение к вкладке.

func (s *Stagehand) OpenDialog() *Dialog {
	if s.aux == nil {
		return nil
	}
	return s.aux.OpenDialog()
}

func (s *Stagehand) DialogOpened() <-chan struct{} {
	if s.aux == nil {
		return nil
	}
	return s.aux.DialogOpened()
}

func (s *Stagehand) HandleDialog(ctx context.Context, accept bool, text string) error {
	if s.aux == nil {
		return ErrNoDialog
	}
	return s.aux.HandleDialog(ctx, accept, text)
}

func (s *Stagehand) AcceptUnload(on bool) {
	if s.aux != nil {
		s.aux.AcceptUnload(on)
	}
}

func (s *Stagehand) AutoDialogs() {
	if s.aux != nil {
		s.aux.AutoDialogs()
	}
}

func (s *Stagehand) ManualDialogs() {
	if s.aux != nil {
		s.aux.ManualDialogs()
	}
}

func (s *Stagehand) Ping(ctx context.Context) error {
	if s.aux == nil {
		_, err := s.page.URL(ctx)
		return err
	}
	return s.aux.Ping(ctx)
}

func (s *Stagehand) Activate(ctx context.Context) error {
	if s.aux == nil {
		return nil
	}
	return s.aux.Activate(ctx)
}
