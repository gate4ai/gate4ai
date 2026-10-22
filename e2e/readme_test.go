// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/connectors/mail/mailtest"
)

// fakeTelegram — фейковый Bot API с long polling.
type fakeTelegram struct {
	mu      sync.Mutex
	sent    []map[string]any
	updates []map[string]any
	nextID  int
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if !strings.HasPrefix(r.URL.Path, "/bot777:TEST/") {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "Unauthorized"})
		return
	}
	var req map[string]any
	json.NewDecoder(r.Body).Decode(&req)
	var result any = true
	switch method {
	case "sendMessage":
		f.mu.Lock()
		f.nextID++
		f.sent = append(f.sent, req)
		result = map[string]any{"message_id": f.nextID}
		f.mu.Unlock()
	case "getUpdates":
		deadline := time.Now().Add(300 * time.Millisecond)
		for {
			f.mu.Lock()
			if len(f.updates) > 0 || time.Now().After(deadline) {
				result = f.updates
				f.updates = nil
				f.mu.Unlock()
				break
			}
			f.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeTelegram) messages() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any{}, f.sent...)
}

// Поток из README на настоящих бинарниках gate4ai и коннекторе почты с фейковыми
// IMAP/SMTP и Telegram Bot API: send_email → ask → подтверждение в Telegram →
// SMTP → проверка по «Отправленным» → квитанция в журнале; отказ — честная ошибка.
func TestReadmeFlow(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("e2e builds binaries")
	}
	mailSrv, err := mailtest.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer mailSrv.Close()
	tg := &fakeTelegram{}
	tgSrv := httptest.NewServer(tg)
	defer tgSrv.Close()
	const owner = 4242

	cfg := fmt.Sprintf(`
agents:
  claude-ai:      { profile: assistant }
  openclaw-cloud: { profile: drafts-only }
lists:
  contacts_trusted: ["accountant@example.com"]
profiles:
  assistant:
    connectors: [email]
    rules:
      - id: email-send-trusted
        action: email.send
        when: to.all(r, r in lists.contacts_trusted)
        effect: allow
      - id: email-no-untrusted-recipients
        action: email.send
        when: "'untrusted_content' in provenance.to"
        effect: deny
        reason: "Адресат взят из входящего письма"
  drafts-only:
    connectors: [email]
    rules:
      - { id: no-send, action: email.send, effect: deny, reason: "recipient not in allowlist" }
connectors:
  email:
    address: me@example.com
    imap: { host: 127.0.0.1, port: %d, tls: none, username: %s, password: "@mail.work" }
    smtp: { host: 127.0.0.1, port: %d, tls: none, username: %s, password: "@mail.work" }
    verify_attempts: 5
    verify_delay_ms: 50
approvals:
  channels: [telegram]
  timeout: 30m
  on_timeout: deny
  telegram: { token: "@telegram.bot", owner_id: %d }
`, mailSrv.IMAPPort, mailtest.User, mailSrv.SMTPPort, mailtest.User, owner)
	g := startGate(t, "--telegram-api", tgSrv.URL)
	run := g.run
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(cfgFile, []byte(cfg), 0o600)

	// Владелец ставит коннектор почты из магазина: пакет своей сборки (--dev),
	// согласие на возможности; без согласия установка не проходит.
	pkg := filepath.Join(t.TempDir(), "email.g4c")
	if r := run("", "", "connectors", "pack", src(t, "connectors/mail"), "-o", pkg); r.code != 0 {
		t.Fatal(r.out)
	}
	if r := run("", "", "connectors", "install", pkg); r.code != 5 || !strings.Contains(r.out, "--dev") {
		t.Fatalf("unsigned package installed without --dev: %d %s", r.code, r.out)
	}
	if r := run("", "n\n", "connectors", "install", pkg, "--dev"); r.code != 2 || !strings.Contains(r.out, "cancelled") {
		t.Fatalf("installed without consent: %d %s", r.code, r.out)
	}
	if r := run("", "y\n", "connectors", "install", pkg, "--dev"); r.code != 0 || !strings.Contains(r.out, "network tls://${setting:imap.host}") {
		t.Fatalf("install: %d %s", r.code, r.out)
	}
	if r := run("", "", "connectors", "list"); !strings.Contains(r.out, `"loaded": true`) || !strings.Contains(r.out, `"trust": "local"`) {
		t.Fatal(r.out)
	}

	// Владелец: секреты по алиасам, политика, токены агентов.
	if r := run("", mailtest.Password, "vault", "set", "mail.work"); r.code != 0 {
		t.Fatal(r.out)
	}
	if r := run("", "777:TEST", "vault", "set", "telegram.bot"); r.code != 0 {
		t.Fatal(r.out)
	}
	if r := run("", "", "policy", "set", cfgFile, "-m", "email connector and rules"); r.code != 0 {
		t.Fatal(r.out)
	}
	claude := run("", "", "agents", "token", "claude-ai").json(t)["token"].(string)
	openclaw := run("", "", "agents", "token", "openclaw-cloud").json(t)["token"].(string)

	// 1. Агент вызывает send_email.
	r := run(claude, "", "mail", "send", "--to", "landlord@example.com", "--subject", "Аренда за октябрь", "--body", "Оплатил, квитанция во вложении.")
	// 2. Адресат не в списке доверенных → ask.
	res := r.json(t)
	if r.code != 3 || errOf(res)["code"] != "pending_approval" {
		t.Fatalf("exit %d: %s", r.code, r.out)
	}
	id := res["id"].(string)
	if len(mailSrv.Received) != 0 {
		t.Fatal("sent before approval")
	}
	// 3. В Telegram: «… хочет … landlord@example.com #хэш [Разрешить] [Отклонить] [Показать текст]».
	var msg map[string]any
	for start := time.Now(); msg == nil && time.Since(start) < StartTimeout; {
		for _, m := range tg.messages() {
			if strings.Contains(m["text"].(string), "landlord@example.com") {
				msg = m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if msg == nil {
		t.Fatal("no Telegram approval request")
	}
	text := msg["text"].(string)
	hash := regexp.MustCompile(`#[0-9a-f]{8}`).FindString(text)
	if !strings.Contains(text, "claude-ai") || hash == "" || msg["chat_id"].(float64) != owner {
		t.Fatal(text)
	}
	kb := msg["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	labels := []string{kb[0].(map[string]any)["text"].(string), kb[1].(map[string]any)["text"].(string), kb[2].(map[string]any)["text"].(string)}
	if strings.Join(labels, "|") != "Разрешить|Отклонить|Показать текст" {
		t.Fatal(labels)
	}
	// 4. Пользователь нажимает «Разрешить» → коннектор отправляет через SMTP с учётными данными из хранилища.
	tg.mu.Lock()
	tg.updates = append(tg.updates, map[string]any{"update_id": 1, "callback_query": map[string]any{
		"id": "cb", "from": map[string]any{"id": owner}, "data": kb[0].(map[string]any)["callback_data"]}})
	tg.mu.Unlock()
	// 5. Шлюз проверяет «Отправленные» и возвращает результат с идентификатором письма.
	// С запасом под -race и загруженный раннер: исполнение и проверка «Отправленных».
	r = run(claude, "", "status", id, "--wait", "60s")
	res = r.json(t)
	if r.code != 0 || res["status"] != "ok" || res["verified"] != true {
		t.Fatalf("exit %d: %s", r.code, r.out)
	}
	msgID := res["result"].(map[string]any)["message_id"].(string)
	if len(mailSrv.Received) != 1 || !strings.Contains(string(mailSrv.Received[0].Data), "Message-ID: "+msgID) || mailSrv.Count("Sent") != 1 {
		t.Fatal("not delivered / not in Sent")
	}
	// Квитанция — в журнал; находится по хэшу из Telegram.
	rec := run("", "", "show", hash).json(t)
	receipt := rec["receipt"].(map[string]any)
	if receipt["approval"].(map[string]any)["channel"] != "telegram" || receipt["verification"].(map[string]any)["status"] != "verified" ||
		receipt["execution"].(map[string]any)["result_ref"] != msgID || len(receipt["verification"].(map[string]any)["evidence_refs"].([]any)) < 2 {
		t.Fatal(rec)
	}
	if strings.Contains(r.out, mailtest.Password) || strings.Contains(rec["receipt"].(map[string]any)["args"].(map[string]any)["body"].(string), mailtest.Password) {
		t.Fatal("secret leaked")
	}

	// Отказ: честная понятная ошибка.
	r = run(openclaw, "", "mail", "send", "--to", "landlord@example.com", "--subject", "x")
	if r.code != 2 || !strings.Contains(errOf(r.json(t))["message"].(string), "denied_by_policy: recipient not in allowlist; user notified") {
		t.Fatalf("exit %d: %s", r.code, r.out)
	}
	// Агентский токен не даёт прав владельца.
	if r := run(claude, "", "log"); r.code == 0 {
		t.Fatal("agent read the journal")
	}
	// Журнал: цепочка цела, экспорт проверяется офлайн.
	if r := run("", "", "journal", "verify"); r.code != 0 || r.json(t)["ok"] != true {
		t.Fatal(r.out)
	}
	arch := filepath.Join(t.TempDir(), "journal.tar.gz")
	if r := run("", "", "export", "-o", arch); r.code != 0 {
		t.Fatal(r.out)
	}
	if r := run("", "", "verify", arch); r.code != 0 || r.json(t)["ok"] != true {
		t.Fatal(r.out)
	}
}

// errOf — error {code, message, hint} из конверта ответа.
func errOf(m map[string]any) map[string]any { e, _ := m["error"].(map[string]any); return e }
