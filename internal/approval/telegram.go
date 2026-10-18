// SPDX-License-Identifier: AGPL-3.0-only

package approval

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/internal/state"
)

// TelegramAPI — нужная часть Bot API (интерфейс для фейка в тестах).
type TelegramAPI interface {
	SendMessage(ctx context.Context, chatID int64, text string, kb [][]Button) (int64, error)
	EditMessage(ctx context.Context, chatID, msgID int64, text string) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error)
	SetWebhook(ctx context.Context, url, secret string) error
	DeleteWebhook(ctx context.Context) error
}

// Button — inline-кнопка.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// Update — обновление Bot API (только используемые поля).
type Update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		From *User  `json:"from"`
		Chat *Chat  `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
	CallbackQuery *struct {
		ID      string `json:"id"`
		From    *User  `json:"from"`
		Data    string `json:"data"`
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      *Chat `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// User — пользователь Telegram.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat — чат Telegram.
type Chat struct {
	ID int64 `json:"id"`
}

// BotAPI — HTTP-клиент Bot API. Daemon ходит в Telegram сам, исходящими запросами.
type BotAPI struct {
	Base  string // https://api.telegram.org
	Token string
	HTTP  *http.Client
}

func (b *BotAPI) call(ctx context.Context, method string, req, out any) error {
	body, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(b.Base, "/")+"/bot"+b.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	c := b.HTTP
	if c == nil {
		c = &http.Client{Timeout: 70 * time.Second}
	}
	resp, err := c.Do(r)
	if err != nil {
		// Ошибка содержит URL с токеном — не выносим её наружу как есть.
		return errors.New("telegram: request failed: " + strings.ReplaceAll(err.Error(), b.Token, "***"))
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err := json.Unmarshal(rb, &env); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (b *BotAPI) SendMessage(ctx context.Context, chatID int64, text string, kb [][]Button) (int64, error) {
	req := map[string]any{"chat_id": chatID, "text": text}
	if kb != nil {
		req["reply_markup"] = map[string]any{"inline_keyboard": kb}
	}
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	err := b.call(ctx, "sendMessage", req, &m)
	return m.MessageID, err
}

func (b *BotAPI) EditMessage(ctx context.Context, chatID, msgID int64, text string) error {
	return b.call(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": msgID, "text": text}, nil)
}

func (b *BotAPI) AnswerCallback(ctx context.Context, id, text string) error {
	return b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

func (b *BotAPI) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var u []Update
	err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": timeout, "allowed_updates": []string{"message", "callback_query"}}, &u)
	return u, err
}

func (b *BotAPI) SetWebhook(ctx context.Context, url, secret string) error {
	return b.call(ctx, "setWebhook", map[string]any{"url": url, "secret_token": secret, "allowed_updates": []string{"message", "callback_query"}}, nil)
}

func (b *BotAPI) DeleteWebhook(ctx context.Context) error {
	return b.call(ctx, "deleteWebhook", map[string]any{}, nil)
}

// Telegram — канал подтверждений через личного бота пользователя. Принимает
// команды и нажатия только от Telegram ID владельца.
type Telegram struct {
	API     TelegramAPI
	OwnerID int64
	Decider Decider
	State   *state.Store
	Log     *slog.Logger
	// Webhook — адрес вебхука (<публичный адрес>/telegram); "" — long polling.
	Webhook string

	mu      sync.Mutex
	msgs    map[string]int64 // approval id → message id
	logins  map[string]loginReq
	secret  string // секрет вебхука (заголовок X-Telegram-Bot-Api-Secret-Token)
	lastErr string // последняя ошибка получения обновлений ("" — работает)
	mode    string // polling | webhook
}

// Status — режим получения обновлений и последняя ошибка ("" — работает).
func (t *Telegram) Status() (mode, lastErr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mode, t.lastErr
}

func (t *Telegram) setStatus(mode, err string) {
	t.mu.Lock()
	t.mode, t.lastErr = mode, err
	t.mu.Unlock()
}

// NewTelegram создаёт канал.
func NewTelegram(api TelegramAPI, owner int64, d Decider, st *state.Store) *Telegram {
	return &Telegram{API: api, OwnerID: owner, Decider: d, State: st, Log: slog.Default(), msgs: map[string]int64{}, logins: map[string]loginReq{}}
}

// loginReq — запрос подтверждения входа в Web UI.
type loginReq struct {
	msg  int64
	text string
	done func(ok bool)
}

// ConfirmLogin просит владельца подтвердить вход в Web UI (id — случайный
// идентификатор попытки входа, text — где и с каким кодом). done вызывается один
// раз — по нажатию «Разрешить вход» или «Отклонить».
func (t *Telegram) ConfirmLogin(id, text string, done func(ok bool)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	kb := [][]Button{{{Text: "Разрешить вход", Data: "la:" + id}, {Text: "Отклонить", Data: "lr:" + id}}}
	msg, err := t.API.SendMessage(ctx, t.OwnerID, text, kb)
	if err != nil {
		t.Log.Warn("telegram login confirmation", "err", err)
		return err
	}
	t.mu.Lock()
	t.logins[id] = loginReq{msg: msg, text: text, done: done}
	t.mu.Unlock()
	return nil
}

// loginCallback — нажатие кнопки подтверждения входа.
func (t *Telegram) loginCallback(ctx context.Context, cbID, data string) {
	ok, id := strings.HasPrefix(data, "la:"), data[3:]
	t.mu.Lock()
	req, found := t.logins[id]
	delete(t.logins, id)
	t.mu.Unlock()
	if !found {
		_ = t.API.AnswerCallback(ctx, cbID, "Вход не найден или уже завершён")
		return
	}
	req.done(ok)
	res := map[bool]string{true: "✅ Вход разрешён", false: "❌ Вход отклонён"}[ok]
	_ = t.API.AnswerCallback(ctx, cbID, res)
	_ = t.API.EditMessage(ctx, t.OwnerID, req.msg, req.text+"\n"+res)
}

func sig(a *state.Approval) string {
	if len(a.Nonce) < 12 {
		return a.Nonce
	}
	return a.Nonce[:12]
}

// Notify отправляет запрос: кратко, с хэшем квитанции и кнопками.
func (t *Telegram) Notify(a *state.Approval) {
	text := Summary(a) + "\nПочему: " + a.Decision.Reason
	kb := [][]Button{{
		{Text: "Разрешить", Data: "y:" + a.ID + ":" + sig(a)},
		{Text: "Отклонить", Data: "n:" + a.ID + ":" + sig(a)},
		{Text: "Показать текст", Data: "s:" + a.ID + ":" + sig(a)},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := t.API.SendMessage(ctx, t.OwnerID, text, kb)
	if t.State != nil {
		t.State.ChannelResult("telegram", err)
	}
	if err != nil {
		t.Log.Warn("telegram notify", "err", err)
		return
	}
	t.mu.Lock()
	t.msgs[a.ID] = id
	t.mu.Unlock()
}

func resultText(a *state.Approval) string {
	switch a.Status {
	case state.Approved, state.Done:
		return "✅ Разрешено"
	case state.Rejected:
		return "❌ Отклонено"
	case state.Timeout:
		return "⌛ Время истекло — отклонено"
	}
	return a.Status
}

// Resolved обновляет сообщение: решение принято (в любом канале).
func (t *Telegram) Resolved(a *state.Approval) {
	t.mu.Lock()
	id, ok := t.msgs[a.ID]
	delete(t.msgs, a.ID)
	t.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	via := ""
	if a.Channel != "" && a.Channel != "telegram" {
		via = " (" + a.Channel + ")"
	}
	_ = t.API.EditMessage(ctx, t.OwnerID, id, Summary(a)+"\n"+resultText(a)+via)
}

// Denied — сообщение об отказе политикой.
func (t *Telegram) Denied(agent, action, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = t.API.SendMessage(ctx, t.OwnerID, fmt.Sprintf("⛔ %s: %s запрещено — %s", agent, action, reason), nil)
}

// Notice — служебное сообщение владельцу.
func (t *Telegram) Notice(text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := t.API.SendMessage(ctx, t.OwnerID, text, nil); err != nil {
		t.Log.Warn("telegram notice", "err", err)
	}
}

// Run получает обновления до отмены ctx: вебхуком, если задан Webhook и Telegram
// его принял, иначе long polling.
func (t *Telegram) Run(ctx context.Context) {
	if t.Webhook != "" {
		secret := randomSecret()
		if err := t.API.SetWebhook(ctx, t.Webhook, secret); err == nil {
			t.mu.Lock()
			t.secret, t.mode, t.lastErr = secret, "webhook", ""
			t.mu.Unlock()
			<-ctx.Done()
			t.mu.Lock()
			t.secret = ""
			t.mu.Unlock()
			dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = t.API.DeleteWebhook(dctx)
			return
		} else if ctx.Err() == nil {
			t.Log.Warn("telegram webhook not set, falling back to polling", "err", err)
		}
	}
	t.poll(ctx)
}

func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ServeWebhook — вебхук Telegram: принимается только запрос с секретом,
// выданным при setWebhook; остальное — 404, как будто маршрута нет.
func (t *Telegram) ServeWebhook(w http.ResponseWriter, r *http.Request) {
	t.mu.Lock()
	secret := t.secret
	t.mu.Unlock()
	got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
		http.NotFound(w, r)
		return
	}
	var u Update
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
		http.Error(w, "bad update", http.StatusBadRequest)
		return
	}
	t.Handle(r.Context(), u)
	w.WriteHeader(http.StatusOK)
}

// poll — цикл long polling до отмены ctx.
func (t *Telegram) poll(ctx context.Context) {
	// Вебхук от прошлого запуска мешает getUpdates (409).
	_ = t.API.DeleteWebhook(ctx)
	t.setStatus("polling", "")
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		ups, err := t.API.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			t.Log.Warn("telegram poll", "err", err)
			t.setStatus("polling", err.Error())
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		t.setStatus("polling", "")
		for _, u := range ups {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			t.Handle(ctx, u)
		}
	}
}

// Handle обрабатывает одно обновление.
func (t *Telegram) Handle(ctx context.Context, u Update) {
	if cq := u.CallbackQuery; cq != nil {
		if cq.From == nil || cq.From.ID != t.OwnerID {
			_ = t.API.AnswerCallback(ctx, cq.ID, "Нет доступа")
			return
		}
		t.callback(ctx, cq.ID, cq.Data)
		return
	}
	if m := u.Message; m != nil {
		if m.From == nil || m.From.ID != t.OwnerID {
			return // посторонним бот не отвечает
		}
		switch strings.Fields(m.Text + " ")[0] {
		case "/start", "/help":
			_, _ = t.API.SendMessage(ctx, t.OwnerID, "gate4.ai: запросы подтверждения приходят сюда. /pending — ожидающие запросы.", nil)
		case "/pending":
			as, _ := t.State.ListApprovals(state.Pending, 20)
			if len(as) == 0 {
				_, _ = t.API.SendMessage(ctx, t.OwnerID, "Нет ожидающих запросов.", nil)
			}
			for _, a := range as {
				t.Notify(a)
			}
		}
	}
}

func (t *Telegram) callback(ctx context.Context, cbID, data string) {
	if strings.HasPrefix(data, "la:") || strings.HasPrefix(data, "lr:") {
		t.loginCallback(ctx, cbID, data)
		return
	}
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		_ = t.API.AnswerCallback(ctx, cbID, "Неверный запрос")
		return
	}
	a, err := t.State.GetApproval(parts[1])
	if err != nil || parts[2] != sig(a) {
		_ = t.API.AnswerCallback(ctx, cbID, "Запрос не найден")
		return
	}
	switch parts[0] {
	case "s":
		_ = t.API.AnswerCallback(ctx, cbID, "")
		_, _ = t.API.SendMessage(ctx, t.OwnerID, a.Preview, nil)
	case "y", "n":
		_, err := t.Decider.Decide(ctx, a.ID, parts[0] == "y", "telegram", "tg:"+strconv.FormatInt(t.OwnerID, 10))
		switch {
		case errors.Is(err, state.ErrAlreadyDecided):
			_ = t.API.AnswerCallback(ctx, cbID, "Решение уже принято")
		case err != nil:
			_ = t.API.AnswerCallback(ctx, cbID, "Ошибка: "+err.Error())
		default:
			_ = t.API.AnswerCallback(ctx, cbID, "Готово")
		}
	default:
		_ = t.API.AnswerCallback(ctx, cbID, "Неверный запрос")
	}
}
