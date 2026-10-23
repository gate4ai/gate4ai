// SPDX-License-Identifier: AGPL-3.0-only

// Package approval — каналы Approval Service: уведомления на компьютере и личный
// Telegram-бот пользователя. Решение принимается только нажатием в привязанном
// канале; запрос одноразовый, кнопки подписаны секретом запроса.
package approval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/gate4ai/gate4ai/internal/audit"
	"github.com/gate4ai/gate4ai/internal/pipeline"
	"github.com/gate4ai/gate4ai/internal/state"
)

// Decider — принимает решение по запросу (pipeline.Pipeline).
type Decider interface {
	Decide(ctx context.Context, id string, approve bool, channel, who string) (pipeline.Result, error)
}

// Summary — краткое «что именно» для уведомления: агент, действие, ключевой аргумент.
func Summary(a *state.Approval) string {
	key := ""
	for _, k := range []string{"to", "path", "url", "payee", "recipient"} {
		if v, ok := a.Args[k]; ok {
			switch t := v.(type) {
			case string:
				key = t
			case []any:
				parts := make([]string, 0, len(t))
				for _, x := range t {
					parts = append(parts, fmt.Sprint(x))
				}
				key = strings.Join(parts, ", ")
			}
			if key != "" {
				break
			}
		}
	}
	// Одноразовые коды и токены в адресах (?code=, #access_token=) не уходят
	// в Telegram и журнал уведомлений ОС.
	key = pipeline.MaskURLText(key)
	if amt, ok := a.Args["amount"].(float64); ok && amt != 0 {
		key = strings.TrimSpace(fmt.Sprintf("%s %.2f", key, amt))
	}
	s := fmt.Sprintf("%s хочет выполнить %s", a.Agent, a.Action)
	if key != "" {
		s += ": " + key
	}
	return s + " " + audit.Short(a.PreviewHash)
}

// Runner запускает команду с дополнительными переменными окружения env
// ("K=V"; подменяется в тестах).
type Runner func(env []string, name string, args ...string) error

// ExecRunner — реальный запуск.
func ExecRunner(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Run()
}

// Desktop — уведомления на компьютере (notify-send / osascript / PowerShell).
// Подтверждение — в трее, Web UI или CLI; сам тост не принимает решений.
type Desktop struct {
	Run  Runner
	GOOS string
	// Result — итог показа уведомления (статистика канала); nil — не учитывать.
	Result func(err error)
}

// NewDesktop — канал уведомлений для текущей ОС.
func NewDesktop() *Desktop { return &Desktop{Run: ExecRunner, GOOS: runtime.GOOS} }

// Скрипты уведомлений — константы: заголовок и текст (в них аргументы агента)
// никогда не входят в текст скрипта, а передаются данными — переменными
// окружения (PowerShell) или аргументами (osascript, notify-send). Кавычки,
// в том числе «умные» ’ ‘ ‛ ‚, которые PowerShell тоже считает кавычками,
// ничего не закрывают.
const (
	psToast = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;` +
		`$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
		`$x = $t.GetElementsByTagName('text'); $x.Item(0).AppendChild($t.CreateTextNode($env:G4A_TITLE)) > $null; $x.Item(1).AppendChild($t.CreateTextNode($env:G4A_BODY)) > $null;` +
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('gate4.ai').Show([Windows.UI.Notifications.ToastNotification]::new($t))`
	osaRun  = `on run argv`
	osaShow = `display notification (item 2 of argv) with title (item 1 of argv)`
	osaEnd  = `end run`
)

func (d *Desktop) show(title, body string) error {
	// NUL в аргументе или переменной окружения не даёт запустить процесс:
	// агент не должен так гасить уведомление.
	title, body = strings.ReplaceAll(title, "\x00", ""), strings.ReplaceAll(body, "\x00", "")
	var err error
	switch d.GOOS {
	case "darwin":
		err = d.Run(nil, "osascript", "-e", osaRun, "-e", osaShow, "-e", osaEnd, title, body)
	case "windows":
		err = d.Run([]string{"G4A_TITLE=" + oneLine(title), "G4A_BODY=" + body}, "powershell", "-NoProfile", "-NonInteractive", "-Command", psToast)
	default:
		// "--": текст, начинающийся с "-", не становится флагом.
		err = d.Run(nil, "notify-send", "--app-name=gate4.ai", "--urgency=critical", "--", title, body)
	}
	if d.Result != nil {
		d.Result(err)
	}
	return err
}

// oneLine — заголовок в одну строку.
func oneLine(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// Test показывает пробное уведомление.
func (d *Desktop) Test() error {
	return d.show("gate4.ai: проверка уведомлений", "Так будут приходить запросы подтверждения.")
}

// Notify — новый запрос подтверждения.
func (d *Desktop) Notify(a *state.Approval) {
	_ = d.show("gate4.ai: нужно подтверждение", Summary(a)+"\n"+a.Decision.Reason+"\nПодтвердите в трее, Web UI или: gate4ai approvals approve "+a.ID)
}

// Resolved — решение принято (уведомление не нужно).
func (d *Desktop) Resolved(a *state.Approval) {}

// Asked — вопрос с вводом значения (код для входа на сайт).
func (d *Desktop) Asked(id, text string) {
	_ = d.show("gate4.ai: нужен код", text+"\nВведите в Web UI («Подтверждения») или: gate4ai approvals answer "+id)
}

// HandedOff — агент передал страницу браузера человеку (browser.handoff).
func (d *Desktop) HandedOff(text string) {
	_ = d.show("gate4.ai: агент ждёт вас в Chrome", text)
}

// PairRequested — агент просит подключения (gate4ai pair): решение — в Web UI.
func (d *Desktop) PairRequested(p *state.Pairing) {
	_ = d.show("gate4.ai: агент просит подключения", PairingText(p)+"\nСверьте код с чатом агента и подтвердите в Web UI (gate4ai ui → Агенты).")
}

// PairingText — запрос на подключение одним абзацем: агент, код, каталог.
func PairingText(p *state.Pairing) string {
	s := fmt.Sprintf("%s просит подключиться к gate4.ai. Код: %s", p.Agent, p.Code)
	if p.Dir != "" {
		s += "\nКлюч — только для агента из " + p.Dir
	}
	return s
}

// Denied — агенту отказано политикой; пользователь узнаёт об этом.
func (d *Desktop) Denied(agent, action, reason string) {
	_ = d.show("gate4.ai: действие запрещено", fmt.Sprintf("%s: %s — %s", agent, action, reason))
}
