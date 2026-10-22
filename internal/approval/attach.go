// SPDX-License-Identifier: AGPL-3.0-only

package approval

import (
	"context"
	"net/http"
	"strconv"
	"sync"

	"github.com/gate4ai/gate4ai/internal/config"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/state"
)

// switchable — канал, который можно включать и выключать при смене конфигурации.
// События уходят в канал через свою очередь (одна горутина на канал, порядок
// сохраняется): медленный Telegram, Web Push или PowerShell не задерживают
// ответ агенту «pending» и отказ.
type switchable struct {
	mu sync.RWMutex
	n  pipeline.Notifier

	qOnce sync.Once
	q     chan func()
	wg    sync.WaitGroup
}

// queueLen — очередь событий канала; при переполнении событие доставляется синхронно.
const queueLen = 256

func (s *switchable) set(n pipeline.Notifier) { s.mu.Lock(); s.n = n; s.mu.Unlock() }

// setIf включает канал n, если on, иначе выключает.
func (s *switchable) setIf(on bool, n pipeline.Notifier) {
	if !on {
		n = nil
	}
	s.set(n)
}
func (s *switchable) get() pipeline.Notifier {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.n
}

// async ставит событие в очередь канала.
func (s *switchable) async(f func()) {
	s.qOnce.Do(func() {
		s.q = make(chan func(), queueLen)
		go func() {
			for f := range s.q {
				f()
				s.wg.Done()
			}
		}()
	})
	s.wg.Add(1)
	select {
	case s.q <- f:
	default:
		f()
		s.wg.Done()
	}
}

func (s *switchable) Notify(a *state.Approval) {
	if n := s.get(); n != nil {
		s.async(func() { n.Notify(a) })
	}
}

func (s *switchable) Resolved(a *state.Approval) {
	if n := s.get(); n != nil {
		s.async(func() { n.Resolved(a) })
	}
}
func (s *switchable) Denied(ag, ac, r string) {
	if n := s.get(); n != nil {
		s.async(func() { n.Denied(ag, ac, r) })
	}
}
func (s *switchable) HandedOff(text string) {
	if h, ok := s.get().(daemon.HandedOff); ok {
		s.async(func() { h.HandedOff(text) })
	}
}
func (s *switchable) Asked(id, text string) {
	if a, ok := s.get().(daemon.Asker); ok {
		s.async(func() { a.Asked(id, text) })
	}
}

// Attach подключает каналы из approvals.channels и следит за изменениями
// конфигурации: desktop — уведомления ОС, telegram — личный бот (токен из Vault;
// вебхук — если включён и у устройства есть публичный адрес). Вызывать после
// подключения источников публичного адреса (relay, прямой HTTPS).
func Attach(ctx context.Context, d *daemon.Daemon, desktop *Desktop, telegramBase string) *Channels {
	desk, tgSw, pushSw := &switchable{}, &switchable{}, &switchable{}
	d.AddNotifier(desk)
	d.AddNotifier(tgSw)
	d.AddNotifier(pushSw)
	if desktop != nil && desktop.Result == nil {
		desktop.Result = func(err error) { d.State.ChannelResult("desktop", err) }
	}
	push, err := NewWebPush(d.Keys, d.State)
	if err != nil {
		d.Log.Warn("browser push disabled: no VAPID key", "err", err)
	} else {
		push.Log = d.Log
	}
	ch := &Channels{d: d, Desktop: desktop, Push: push, TelegramBase: telegramBase, tg: tgSw, queues: []*switchable{desk, tgSw, pushSw}}
	// Вебхук на публичном адресе; без бота в режиме вебхука — 404.
	d.AddPublic("POST /telegram", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tg, ok := tgSw.get().(*Telegram); ok {
			tg.ServeWebhook(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	var stopTG context.CancelFunc
	var tgKey string
	apply := func(c *config.Config) {
		on := map[string]bool{}
		for _, ch := range c.Approvals.Channels {
			on[ch] = true
		}
		desk.setIf(on["desktop"] && desktop != nil, desktop)
		pushSw.setIf(on["webpush"] && push != nil, push)
		key, hook := "", ""
		var token string
		if t := c.Approvals.Telegram; on["telegram"] && t != nil {
			var err error
			if token, err = d.Vault.Get(t.Token); err != nil {
				d.Log.Warn("telegram approvals disabled: bot token not in vault", "alias", t.Token)
			} else {
				key = t.Token + "/" + token + "/" + strconv.FormatInt(t.OwnerID, 10)
				if t.Webhook {
					if hook = d.PublicURL(); hook != "" {
						hook += "/telegram"
					} else {
						d.Log.Warn("telegram webhook: no public address, using polling")
					}
				}
				key += "/" + hook
			}
		}
		if key == tgKey {
			return
		}
		if stopTG != nil {
			stopTG()
			stopTG = nil
		}
		tgSw.set(nil)
		tgKey = key
		if key == "" {
			return
		}
		tg := NewTelegram(&BotAPI{Base: telegramBase, Token: token}, c.Approvals.Telegram.OwnerID, d.Pipeline, d.State)
		tg.Log = d.Log
		tg.Answer = d.Answer
		tg.Webhook = hook
		tctx, cancel := context.WithCancel(ctx)
		stopTG = cancel
		tgSw.set(tg)
		go tg.Run(tctx)
		d.Log.Info("telegram approvals enabled")
	}
	apply(d.Config.Current())
	d.Config.Subscribe(apply)
	return ch
}
