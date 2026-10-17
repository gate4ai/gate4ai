// SPDX-License-Identifier: AGPL-3.0-only

package approval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/state"
)

// Channels — каналы подтверждений для Web UI: состояние, статистика, проверка,
// мастер настройки Telegram.
type Channels struct {
	d            *daemon.Daemon
	Desktop      *Desktop
	Push         *WebPush // nil — нет ключа VAPID
	TelegramBase string
	tg           *switchable
}

// Канал и его состояние.
const (
	StateOK            = "ok"             // включён и работает
	StateOff           = "off"            // выключен
	StateError         = "error"          // включён, но не работает (Detail — почему)
	StateNotConfigured = "not_configured" // включён, но не настроен
)

// ChannelStatus — строка таблицы «Каналы уведомлений».
type ChannelStatus struct {
	Name    string            `json:"name"`
	Title   string            `json:"title"`
	On      bool              `json:"on"`
	State   string            `json:"state"`
	Detail  string            `json:"detail,omitempty"`
	Stat    state.ChannelStat `json:"stat"`
	Decided int               `json:"decided"` // запросов решено через этот канал
}

// Telegram — работающий канал Telegram (nil — выключен или не настроен).
func (c *Channels) Telegram() *Telegram {
	if c == nil || c.tg == nil {
		return nil
	}
	t, _ := c.tg.get().(*Telegram)
	return t
}

// Status — состояние всех каналов.
func (c *Channels) Status() []ChannelStatus {
	cfg := c.d.Config.Current()
	stats, _ := c.d.State.ChannelStats()
	decided, _ := c.d.State.DecidedByChannel()
	out := []ChannelStatus{}

	desk := ChannelStatus{Name: "desktop", Title: "Уведомления на этом компьютере", On: cfg.ChannelOn("desktop"), Stat: stats["desktop"], Decided: decided["tray"]}
	switch {
	case !desk.On:
		desk.State = StateOff
	case c.Desktop == nil:
		desk.State, desk.Detail = StateError, "notifications are not available here"
	case recentError(desk.Stat):
		desk.State, desk.Detail = StateError, desk.Stat.LastErr
	default:
		desk.State = StateOK
	}
	out = append(out, desk)

	tg := ChannelStatus{Name: "telegram", Title: "Telegram", On: cfg.ChannelOn("telegram"), Stat: stats["telegram"], Decided: decided["telegram"]}
	t := cfg.Approvals.Telegram
	switch {
	case !tg.On:
		tg.State = StateOff
		if t == nil {
			tg.Detail = "not set up"
		}
	case t == nil:
		tg.State, tg.Detail = StateNotConfigured, "bot is not set up"
	default:
		if _, err := c.d.Vault.Get(t.Token); err != nil {
			tg.State, tg.Detail = StateError, "bot token "+t.Token+" is not in the vault"
		} else if run := c.Telegram(); run == nil {
			tg.State, tg.Detail = StateError, "not running"
		} else if mode, e := run.Status(); e != "" {
			tg.State, tg.Detail = StateError, e
		} else if recentError(tg.Stat) {
			tg.State, tg.Detail = StateError, tg.Stat.LastErr
		} else {
			tg.State, tg.Detail = StateOK, mode
		}
	}
	out = append(out, tg)

	push := ChannelStatus{Name: "webpush", Title: "Браузерный push", On: cfg.ChannelOn("webpush"), Stat: stats["webpush"]}
	subs, _ := c.d.State.PushSubs()
	switch {
	case !push.On:
		push.State = StateOff
	case c.Push == nil:
		push.State, push.Detail = StateError, "no VAPID key"
	case len(subs) == 0:
		push.State, push.Detail = StateNotConfigured, "no browser is subscribed"
	case recentError(push.Stat):
		push.State, push.Detail = StateError, push.Stat.LastErr
	default:
		push.State, push.Detail = StateOK, fmt.Sprintf("%d browser(s)", len(subs))
	}
	out = append(out, push)

	web := ChannelStatus{Name: "webui", Title: "Web UI (эта страница, удалённо — /ui/)", On: true, State: StateOK,
		Decided: decided["webui"] + decided["webui-remote"]}
	out = append(out, web)
	return out
}

// recentError — последняя попытка канала была неудачной.
func recentError(s state.ChannelStat) bool {
	return !s.LastErrAt.IsZero() && s.LastErrAt.After(s.LastOK)
}

// Test отправляет пробное уведомление в канал.
func (c *Channels) Test(ctx context.Context, name string) error {
	switch name {
	case "desktop":
		if c.Desktop == nil {
			return errors.New("desktop notifications are not available")
		}
		return c.Desktop.Test()
	case "telegram":
		t := c.d.Config.Current().Approvals.Telegram
		if t == nil {
			return errors.New("telegram bot is not set up")
		}
		tok, err := c.d.Vault.Get(t.Token)
		if err != nil {
			return fmt.Errorf("bot token %s is not in the vault", t.Token)
		}
		api := &BotAPI{Base: c.TelegramBase, Token: tok}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err = api.SendMessage(ctx, t.OwnerID, "gate4.ai: проверка. Так будут приходить запросы подтверждения.", nil)
		c.d.State.ChannelResult("telegram", err)
		return err
	case "webpush":
		if c.Push == nil {
			return errors.New("browser push is not available")
		}
		return c.Push.Test()
	}
	return fmt.Errorf("unknown channel %q", name)
}

// ---- Мастер Telegram ----

// TelegramUser — кто писал боту (для выбора владельца).
type TelegramUser struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Text     string `json:"text,omitempty"`
}

// TelegramBot проверяет токен (getMe) и возвращает имя бота.
func (c *Channels) TelegramBot(ctx context.Context, token string) (string, error) {
	api := &BotAPI{Base: c.TelegramBase, Token: token}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var me struct {
		Username string `json:"username"`
	}
	if err := api.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// TelegramWriters — кто недавно писал боту (getUpdates без подтверждения
// прочтения). Пока канал включён и опрашивает Telegram сам, getUpdates вернёт 409.
func (c *Channels) TelegramWriters(ctx context.Context, token string) ([]TelegramUser, error) {
	api := &BotAPI{Base: c.TelegramBase, Token: token}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_ = api.DeleteWebhook(ctx)
	ups, err := api.GetUpdates(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []TelegramUser
	for i := len(ups) - 1; i >= 0; i-- {
		m := ups[i].Message
		if m == nil || m.From == nil || seen[m.From.ID] {
			continue
		}
		seen[m.From.ID] = true
		out = append(out, TelegramUser{ID: m.From.ID, Name: m.From.FirstName, Username: m.From.Username, Text: m.Text})
	}
	return out, nil
}
