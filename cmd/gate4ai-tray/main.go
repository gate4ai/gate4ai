// SPDX-License-Identifier: AGPL-3.0-only

//go:build !darwin || cgo

// Команда gate4ai-tray — трей gate4.ai: статус daemon, уведомления о новых
// запросах подтверждения, быстрые «Разрешить / Отклонить» и открытие Web UI.
// Отдельный бинарник: на macOS трею нужен cgo, а ядро собирается без него.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/tray"
)

var (
	//go:embed icon.png
	iconPNG []byte
	//go:embed icon.ico
	iconICO []byte
)

// maxPending — сколько запросов показывать в меню (остальные — в Web UI).
const maxPending = 8

func main() {
	t := tray.New(cli.NewOwnerClient())
	systray.Run(func() { onReady(t) }, nil)
}

// Пункт статуса и ключ последнего меню: меню пересобирается, только когда
// меняется список запросов, иначе оно закрывалось бы у пользователя каждые 5 с.
var (
	status  *systray.MenuItem
	lastKey = "-"
)

func onReady(t *tray.Tray) {
	if runtime.GOOS == "windows" {
		systray.SetIcon(iconICO)
	} else {
		systray.SetIcon(iconPNG)
	}
	systray.SetTooltip("gate4.ai")
	refresh := make(chan struct{}, 1)
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			update(t, refresh)
			select {
			case <-tick.C:
			case <-refresh:
			}
		}
	}()
}

// update перечитывает состояние, уведомляет о новых запросах и пересобирает меню.
func update(t *tray.Tray, refresh chan<- struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := t.Fetch(ctx)
	for _, p := range t.Fresh(st) {
		_ = tray.Notify(tray.T("need_approval"), p.Summary+"\n"+p.Reason)
	}
	systray.SetTooltip(tray.Tooltip(st))
	if key := tray.MenuKey(st) + fmt.Sprint(t.CanStart()); key != lastKey {
		lastKey = key
		buildMenu(t, st, refresh)
	}
	status.SetTitle(tray.StatusLine(st))
}

func buildMenu(t *tray.Tray, st *tray.State, refresh chan<- struct{}) {
	systray.ResetMenu()
	status = systray.AddMenuItem(tray.StatusLine(st), "")
	status.Disable()
	systray.AddSeparator()
	if st == nil && t.CanStart() {
		onClick(systray.AddMenuItem(tray.T("start"), ""), func() { start(refresh) })
	}
	if st != nil {
		if len(st.Pending) == 0 {
			systray.AddMenuItem(tray.T("no_pending"), "").Disable()
		}
		for i, p := range st.Pending {
			if i == maxPending {
				break
			}
			sub := systray.AddMenuItem(p.Title(), p.Summary)
			sub.AddSubMenuItem(p.Summary, "").Disable()
			sub.AddSubMenuItem(tray.T("why")+p.Reason, "").Disable()
			if p.NeedsWebUI() {
				// Самонастройка: правки видны целиком только в Web UI — разрешить
				// можно только там; отклонить — и здесь (как approval.WebOnly).
				onClick(sub.AddSubMenuItem(tray.T("decide_in_ui"), ""), func() { openUI(t) })
				onClick(sub.AddSubMenuItem(tray.T("deny"), ""), func() { decide(t, p.ID, false, refresh) })
				continue
			}
			onClick(sub.AddSubMenuItem(tray.T("allow"), ""), func() { decide(t, p.ID, true, refresh) })
			onClick(sub.AddSubMenuItem(tray.T("deny"), ""), func() { decide(t, p.ID, false, refresh) })
			onClick(sub.AddSubMenuItem(tray.T("details"), ""), func() { openUI(t) })
		}
	}
	systray.AddSeparator()
	if line := tray.BrowserLine(st); line != "" {
		onClick(systray.AddMenuItem(line, ""), func() { openPage(t, "/browser") })
	}
	onClick(systray.AddMenuItem(tray.T("open_ui"), ""), func() { openUI(t) })
	onClick(systray.AddMenuItem(tray.T("quit"), ""), systray.Quit)
}

// onClick вызывает f при выборе пункта; горутина завершается, когда ResetMenu
// закрывает канал пункта.
func onClick(item *systray.MenuItem, f func()) {
	go func() {
		for range item.ClickedCh {
			f()
		}
	}()
}

func decide(t *tray.Tray, id string, approve bool, refresh chan<- struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.Decide(ctx, id, approve); err != nil {
		notifyProblem(tray.T("not_decided"), err)
	}
	nudge(refresh)
}

// openPage — Web UI на странице next.
func openPage(t *tray.Tray, next string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, err := t.URLTo(ctx, next)
	if err == nil {
		err = cli.OpenURL(u)
	}
	if err != nil {
		notifyProblem(tray.T("ui_failed"), err)
	}
}

func openUI(t *tray.Tray) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, err := t.URL(ctx)
	if err == nil {
		err = cli.OpenURL(u)
	}
	if err != nil {
		notifyProblem(tray.T("ui_failed"), err)
	}
}

// start запускает daemon в фоне (`gate4ai daemon start`): gate4ai рядом с треем или в PATH.
func start(refresh chan<- struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := cli.StartBackground(ctx, gateBinary())
	if err != nil {
		notifyProblem(tray.T("start_failed"), err)
	} else {
		_ = tray.Notify(tray.T("started"), tray.T("started_body"))
	}
	nudge(refresh)
}

// nudge — перечитать состояние сейчас, не дожидаясь тика (уже запрошено — не ждать).
func nudge(refresh chan<- struct{}) {
	select {
	case refresh <- struct{}{}:
	default:
	}
}

// notifyProblem — уведомление title с ошибкой err словами для человека.
func notifyProblem(title string, err error) {
	_, body := tray.Problem(err)
	_ = tray.Notify(title, body)
}

func gateBinary() string {
	name := "gate4ai"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}
