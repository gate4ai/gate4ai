// SPDX-License-Identifier: AGPL-3.0-only

package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/sdk-go/abi"

	"github.com/gate4ai/gate4ai/internal/connectors"
	"github.com/gate4ai/gate4ai/internal/daemon"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/platform/auth"
	"github.com/gate4ai/gate4ai/internal/policy"
	"github.com/gate4ai/gate4ai/internal/secrets/keys"
	"github.com/gate4ai/gate4ai/internal/state"
	"github.com/gate4ai/gate4ai/sdk-go/manifest"
)

// fakeTelegram — фейковый Bot API.
type fakeTelegram struct {
	mu      sync.Mutex
	token   string
	sent    []map[string]any
	edits   []map[string]any
	answers []string
	deleted []any
	updates []Update
	nextID  int64
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/bot"+f.token+"/") {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Unauthorized"})
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/bot"+f.token+"/")
	var req map[string]any
	json.NewDecoder(r.Body).Decode(&req)
	var result any = true
	switch method {
	case "sendMessage":
		f.nextID++
		req["message_id"] = f.nextID
		f.sent = append(f.sent, req)
		result = map[string]any{"message_id": f.nextID}
	case "editMessageText":
		f.edits = append(f.edits, req)
	case "deleteMessage":
		f.deleted = append(f.deleted, req["message_id"])
	case "answerCallbackQuery":
		f.answers = append(f.answers, req["text"].(string))
	case "getUpdates":
		result = f.updates
		f.updates = nil
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

type mail struct{ sent int }

func (m *mail) Manifest() manifest.Manifest {
	return manifest.Manifest{Connector: "email", Actions: []manifest.Action{{Name: "send", Class: "external", InputSchema: manifest.MustSchema(`{ type: object, properties: { to: { type: array, items: { type: string, format: email } }, subject: { type: string }, body: { type: string } }, required: [to] }`)}}}
}
func (m *mail) Execute(context.Context, abi.ExecRequest) (abi.ExecResult, error) {
	m.sent++
	return abi.ExecResult{Structured: "sent"}, nil
}
func (m *mail) Verify(context.Context, abi.VerifyRequest) (abi.VerifyResult, error) {
	return abi.VerifyResult{}, nil
}

func cbUpdate(id int64, from int64, data string) Update {
	var u Update
	b, _ := json.Marshal(map[string]any{"update_id": id, "callback_query": map[string]any{"id": "cb1", "from": map[string]any{"id": from}, "data": data,
		"message": map[string]any{"message_id": 1, "chat": map[string]any{"id": from}}}})
	json.Unmarshal(b, &u)
	return u
}

func TestTelegramApprovalFlow(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4atg")
	defer os.RemoveAll(home)
	m := &mail{}
	c, _ := connectors.NewInProcess(m)
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: keys.NewMemory(), Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {claude-ai: {profile: p}}\nprofiles: {p: {connectors: [email]}}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fake := &fakeTelegram{token: "123:SECRET"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	const owner = int64(4242)
	tg := NewTelegram(&BotAPI{Base: srv.URL, Token: "123:SECRET"}, owner, d.Pipeline, d.State)
	d.AddNotifier(tg)
	ctx := context.Background()

	res := d.Pipeline.Execute(ctx, pipeline.Call{Agent: "claude-ai", Action: "email.send",
		Args: map[string]any{"to": "landlord@example.com", "subject": "Rent", "body": "Hello"}})
	if res.Status != "pending" {
		t.Fatal(res)
	}
	if len(fake.sent) != 1 {
		t.Fatal("no telegram message")
	}
	msg := fake.sent[0]
	text := msg["text"].(string)
	a, _ := d.State.GetApproval(res.ID)
	if !strings.Contains(text, "claude-ai хочет выполнить email.send: landlord@example.com #"+a.PreviewHash[:8]) || msg["chat_id"].(float64) != float64(owner) {
		t.Fatal(text)
	}
	kb := msg["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	yes := kb[0].(map[string]any)["callback_data"].(string)
	show := kb[2].(map[string]any)["callback_data"].(string)
	if !strings.HasPrefix(yes, "y:"+res.ID+":") {
		t.Fatal(yes)
	}

	// Посторонний не может нажать кнопку.
	tg.Handle(ctx, cbUpdate(1, 666, yes))
	if fake.answers[0] != "Нет доступа" || m.sent != 0 {
		t.Fatal("foreign user approved")
	}
	// Подделанная подпись кнопки (агент знает id, но не секрет).
	tg.Handle(ctx, cbUpdate(2, owner, "y:"+res.ID+":forged"))
	if fake.answers[1] != "Запрос не найден" || m.sent != 0 {
		t.Fatal("forged callback accepted")
	}
	// «Показать текст».
	tg.Handle(ctx, cbUpdate(3, owner, show))
	if !strings.Contains(fake.sent[len(fake.sent)-1]["text"].(string), "subject: Rent") {
		t.Fatal("preview not shown")
	}
	// Через long polling: владелец нажимает «Разрешить».
	fake.mu.Lock()
	fake.updates = []Update{cbUpdate(10, owner, yes)}
	fake.mu.Unlock()
	pctx, cancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() { tg.Run(pctx); close(runDone) }()
	st := d.Pipeline.WaitFor(ctx, res.ID, 3*time.Second)
	// Дождаться ответа «Готово» на нажатие (answerCallbackQuery) и только потом
	// остановить long polling: иначе фейковый сервер допишет этот ответ позже.
	for i := 0; i < 300; i++ {
		fake.mu.Lock()
		done := len(fake.answers) > 0 && fake.answers[len(fake.answers)-1] == "Готово"
		fake.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-runDone
	if st.Status != "ok" || m.sent != 1 {
		t.Fatal("approve via telegram did not execute", st)
	}
	fake.mu.Lock()
	edited := len(fake.edits) == 1 && strings.Contains(fake.edits[0]["text"].(string), "Разрешено")
	fake.mu.Unlock()
	if !edited {
		t.Fatal("message not updated", fake.edits)
	}
	// Повторное нажатие — «уже решено».
	tg.Handle(ctx, cbUpdate(11, owner, yes))
	fake.mu.Lock()
	last := fake.answers[len(fake.answers)-1]
	fake.mu.Unlock()
	if last != "Решение уже принято" || m.sent != 1 {
		t.Fatal("double approval", last)
	}
	// Ошибки Bot API не раскрывают токен.
	bad := &BotAPI{Base: "http://127.0.0.1:1", Token: "123:SECRET"}
	if _, err := bad.SendMessage(ctx, 1, "x", nil); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("token leaked in error", err)
	}
}

func TestDesktopCommands(t *testing.T) {
	a := &state.Approval{ID: "a_1", Agent: "claude-ai", Action: "payments.pay_bill", PreviewHash: "a3f9c21e00",
		Args: map[string]any{"payee": "@uk-dom", "amount": 5000.0}}
	for goos, bin := range map[string]string{"linux": "notify-send", "darwin": "osascript", "windows": "powershell"} {
		var got []string
		d := &Desktop{GOOS: goos, Run: func(env []string, name string, args ...string) error {
			got = append(append([]string{name}, args...), env...)
			return nil
		}}
		d.Notify(a)
		if got[0] != bin || !strings.Contains(strings.Join(got, " "), "@uk-dom 5000.00 #a3f9c21e") {
			t.Errorf("%s: %v", goos, got)
		}
		d.Denied("bot", "email.send", "recipient not in allowlist")
		if !strings.Contains(strings.Join(got, " "), "recipient not in allowlist") {
			t.Errorf("%s denied: %v", goos, got)
		}
	}
}

// Аргументы агента в уведомлении — данные, а не код: скрипт PowerShell и
// AppleScript — константы, заголовок и текст идут переменными окружения или
// аргументами, какие бы кавычки ни были в аргументах (в том числе «умные»,
// которые PowerShell тоже считает одинарными).
func TestDesktopNotificationIsNotCode(t *testing.T) {
	evil := "C:/x\u2019; Start-Process calc; \u2018 \u201b \u201a ' \\ \" $(calc) `calc` & calc\n-x"
	a := &state.Approval{ID: "a_1", Agent: "bot", Action: "files.write", PreviewHash: "a3f9c21e00",
		Args: map[string]any{"path": evil + "\x00"}, Decision: policy.Decision{Reason: "why " + evil}}
	type call struct {
		env  []string
		name string
		args []string
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		var calls []call
		d := &Desktop{GOOS: goos, Run: func(env []string, name string, args ...string) error {
			calls = append(calls, call{env, name, args})
			return nil
		}}
		d.Notify(a)
		d.Asked("a_2", "code? "+evil)
		d.Denied("bot", "files.write", evil)
		if len(calls) != 3 {
			t.Fatal(goos, calls)
		}
		for _, c := range calls {
			switch goos {
			case "windows":
				// Скрипт — ровно константа; данные — только в G4A_TITLE/G4A_BODY.
				if c.name != "powershell" || c.args[len(c.args)-1] != psToast {
					t.Fatalf("windows script is not constant: %q", c.args)
				}
				for _, x := range c.args {
					if strings.Contains(x, "calc") {
						t.Fatalf("agent data in powershell command line: %q", x)
					}
				}
				if len(c.env) != 2 || !strings.HasPrefix(c.env[0], "G4A_TITLE=") || !strings.HasPrefix(c.env[1], "G4A_BODY=") ||
					!strings.Contains(c.env[1], "Start-Process calc") {
					t.Fatalf("windows env: %q", c.env)
				}
			case "darwin":
				if c.name != "osascript" || len(c.args) != 8 || c.args[1] != osaRun || c.args[3] != osaShow || c.args[5] != osaEnd {
					t.Fatalf("darwin: %q", c.args)
				}
				for _, x := range c.args[:6] {
					if strings.Contains(x, "calc") {
						t.Fatalf("agent data in AppleScript: %q", x)
					}
				}
				if !strings.Contains(c.args[7], "calc") {
					t.Fatalf("darwin body: %q", c.args)
				}
			default:
				if c.name != "notify-send" || c.args[2] != "--" || len(c.args) != 5 {
					t.Fatalf("linux: %q", c.args)
				}
			}
			for _, x := range append(append([]string{}, c.args...), c.env...) {
				if strings.Contains(x, "\x00") {
					t.Fatalf("%s: NUL passed to the process: %q", goos, x)
				}
			}
		}
	}
}

// Одноразовые токены в адресе не уходят в уведомления (L4).
func TestSummaryMasksURLTokens(t *testing.T) {
	a := &state.Approval{ID: "a_1", Agent: "bot", Action: "browser.open", PreviewHash: "a3f9c21e00",
		Args: map[string]any{"url": "https://example.com/cb?code=SECRET123&x=1"}}
	if s := Summary(a); strings.Contains(s, "SECRET123") || !strings.Contains(s, "example.com/cb") {
		t.Fatal(s)
	}
}

func TestAttachFromConfig(t *testing.T) {
	home, _ := os.MkdirTemp("", "g4aat")
	defer os.RemoveAll(home)
	c, _ := connectors.NewInProcess(&mail{})
	ks := keys.NewMemory()
	d, err := daemon.New(daemon.Options{Home: filepath.Join(home, "d"), Keys: ks, Connectors: []connectors.Client{c},
		InitialConfig: []byte("agents: {a: {profile: p}}\nprofiles: {p: {connectors: [email]}}\napprovals: {channels: [desktop, telegram], telegram: {token: '@telegram.bot', owner_id: 7}}\n")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Vault.Set("@telegram.bot", "999:TOKEN")
	// Перечитать конфигурацию после появления токена.
	raw, _ := d.Config.CurrentRaw()
	fake := &fakeTelegram{token: "999:TOKEN"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	var desk []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := Attach(ctx, d, &Desktop{GOOS: "linux", Run: func(env []string, n string, a ...string) error { desk = append(desk, n); return nil }}, srv.URL)
	d.Config.Put(ownerP, raw, "reload")
	d.Pipeline.Execute(ctx, pipeline.Call{Agent: "a", Action: "email.send", Args: map[string]any{"to": "x@y.example"}})
	ch.Flush()
	fake.mu.Lock()
	n := len(fake.sent)
	fake.mu.Unlock()
	if n != 1 || len(desk) != 1 {
		t.Fatalf("telegram %d desktop %d", n, len(desk))
	}
}

var ownerP = auth.OwnerVia("test")

func TestTelegramLoginConfirmation(t *testing.T) {
	fake := &fakeTelegram{token: "1:T"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	const owner = 42
	tg := NewTelegram(&BotAPI{Base: srv.URL, Token: "1:T"}, owner, nil, nil)
	got := make(chan bool, 2)
	if err := tg.ConfirmLogin("abc", "Вход в Web UI, код 123456", func(ok bool) { got <- ok }); err != nil {
		t.Fatal(err)
	}
	kb := fake.sent[0]["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	yes := kb[0].(map[string]any)["callback_data"].(string)
	// Идентификатор попытки (значение cookie) в Telegram не уходит (E.12).
	if strings.Contains(yes, "abc") || strings.Contains(kb[1].(map[string]any)["callback_data"].(string), "abc") {
		t.Fatal("login attempt id leaked to Telegram:", yes)
	}
	// Чужой не подтверждает вход.
	tg.Handle(context.Background(), cbUpdate(1, 7, yes))
	if len(got) != 0 || fake.answers[0] != "Нет доступа" {
		t.Fatal("stranger confirmed login", fake.answers)
	}
	tg.Handle(context.Background(), cbUpdate(2, owner, yes))
	if ok := <-got; !ok || len(fake.edits) != 1 || !strings.Contains(fake.edits[0]["text"].(string), "Вход разрешён") {
		t.Fatal("login not confirmed", fake.edits)
	}
	// Повторное нажатие ничего не делает.
	tg.Handle(context.Background(), cbUpdate(3, owner, yes))
	if len(got) != 0 || fake.answers[len(fake.answers)-1] != "Вход не найден или уже завершён" {
		t.Fatal("second press accepted", fake.answers)
	}
	_ = tg.ConfirmLogin("def", "x", func(ok bool) { got <- ok })
	tg.Handle(context.Background(), cbUpdate(4, owner, "la:def")) // id попытки как ключ не принимается
	if len(got) != 0 {
		t.Fatal("cookie value accepted as callback key")
	}
	kb = fake.sent[1]["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	tg.Handle(context.Background(), cbUpdate(5, owner, kb[1].(map[string]any)["callback_data"].(string)))
	if ok := <-got; ok {
		t.Fatal("rejection confirmed login")
	}
}

// Вопрос с вводом кода (SMS для входа на сайт) приходит в Telegram; ответ —
// следующим сообщением владельца, сообщение с кодом бот удаляет.
func TestTelegramQuestion(t *testing.T) {
	fake := &fakeTelegram{token: "1:Q"}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	const owner = int64(77)
	got := map[string]string{}
	tg := NewTelegram(&BotAPI{Base: srv.URL, Token: "1:Q"}, owner, nil, nil)
	tg.Answer = func(id, v string) error { got[id] = v; return nil }
	ctx := context.Background()
	tg.Asked("q_1", "lk_emias signs in on https://login.mos.ru with @emias and asks: код из SMS")
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0]["text"].(string), "код из SMS") {
		t.Fatal(fake.sent)
	}
	msg := func(from int64, id int64, text string) Update {
		var u Update
		json.Unmarshal([]byte(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":%d,"from":{"id":%d},"chat":{"id":%d},"text":%q}}`, id, id, from, from, text)), &u)
		return u
	}
	tg.Handle(ctx, msg(666, 10, "1234")) // посторонний
	if len(got) != 0 {
		t.Fatal("foreign answer")
	}
	tg.Handle(ctx, msg(owner, 11, " 4821 "))
	if got["q_1"] != "4821" || len(fake.deleted) != 1 || fake.deleted[0].(float64) != 11 || len(fake.edits) != 1 {
		t.Fatal(got, fake.deleted, fake.edits)
	}
	// Вопроса больше нет: следующее сообщение — обычное.
	tg.Handle(ctx, msg(owner, 12, "5555"))
	if len(got) != 1 {
		t.Fatal("answered twice")
	}
	tg.Asked("q_2", "x")
	tg.Handle(ctx, msg(owner, 13, "/cancel"))
	if v, ok := got["q_2"]; !ok || v != "" {
		t.Fatal("cancel:", got)
	}
}
