// SPDX-License-Identifier: FSL-1.1-ALv2

// Package approval — каналы Approval Service: уведомления на компьютере и личный
// Telegram-бот пользователя. Решение принимается только нажатием в привязанном
// канале; запрос одноразовый, кнопки подписаны секретом запроса.
package approval

import (
	"context"
	"fmt"
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
	if amt, ok := a.Args["amount"].(float64); ok && amt != 0 {
		key = strings.TrimSpace(fmt.Sprintf("%s %.2f", key, amt))
	}
	s := fmt.Sprintf("%s хочет выполнить %s", a.Agent, a.Action)
	if key != "" {
		s += ": " + key
	}
	return s + " " + audit.Short(a.PreviewHash)
}

// Runner запускает команду (подменяется в тестах).
type Runner func(name string, args ...string) error

// ExecRunner — реальный запуск.
func ExecRunner(name string, args ...string) error { return exec.Command(name, args...).Run() }

// Desktop — уведомления на компьютере (notify-send / osascript / PowerShell).
// Подтверждение — в трее, Web UI или CLI; сам тост не принимает решений.
type Desktop struct {
	Run  Runner
	GOOS string
}

// NewDesktop — канал уведомлений для текущей ОС.
func NewDesktop() *Desktop { return &Desktop{Run: ExecRunner, GOOS: runtime.GOOS} }

func (d *Desktop) show(title, body string) {
	switch d.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %q with title %q", body, title)
		_ = d.Run("osascript", "-e", script)
	case "windows":
		ps := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;` +
			`$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
			`$x = $t.GetElementsByTagName('text'); $x.Item(0).AppendChild($t.CreateTextNode($env:G4A_TITLE)) > $null; $x.Item(1).AppendChild($t.CreateTextNode($env:G4A_BODY)) > $null;` +
			`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('gate4.ai').Show([Windows.UI.Notifications.ToastNotification]::new($t))`
		_ = d.Run("powershell", "-NoProfile", "-Command", "$env:G4A_TITLE='"+psQuote(title)+"'; $env:G4A_BODY='"+psQuote(body)+"'; "+ps)
	default:
		_ = d.Run("notify-send", "--app-name=gate4.ai", "--urgency=critical", title, body)
	}
}

func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// Notify — новый запрос подтверждения.
func (d *Desktop) Notify(a *state.Approval) {
	d.show("gate4.ai: нужно подтверждение", Summary(a)+"\n"+a.Decision.Reason+"\nПодтвердите в трее, Web UI или: gate4ai approvals approve "+a.ID)
}

// Resolved — решение принято (уведомление не нужно).
func (d *Desktop) Resolved(a *state.Approval) {}

// Denied — агенту отказано политикой; пользователь узнаёт об этом.
func (d *Desktop) Denied(agent, action, reason string) {
	d.show("gate4.ai: действие запрещено", fmt.Sprintf("%s: %s — %s", agent, action, reason))
}
