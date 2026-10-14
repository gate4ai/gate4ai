// SPDX-License-Identifier: FSL-1.1-ALv2

//go:build !darwin || cgo

// Команда gate4ai-tray — трей gate4.ai: статус daemon, уведомления о новых
// запросах подтверждения, быстрые «Разрешить / Отклонить» и открытие Web UI.
// Отдельный бинарник: на macOS трею нужен cgo, а ядро собирается без него.
package main

import (
	"context"
	_ "embed"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/gate4ai/gate4ai/internal/front/cli"
	"github.com/gate4ai/gate4ai/internal/front/tray"
	"github.com/gate4ai/gate4ai/internal/platform/paths"
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
	t := tray.New(cli.NewSocketClient(paths.OwnerSocket(), ""))
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
		_ = tray.Notify("gate4.ai: нужно подтверждение", p.Summary+"\n"+p.Reason)
	}
	systray.SetTooltip(tray.Tooltip(st))
	if key := tray.MenuKey(st); key != lastKey {
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
	if st != nil {
		if len(st.Pending) == 0 {
			systray.AddMenuItem("Нет запросов подтверждения", "").Disable()
		}
		for i, p := range st.Pending {
			if i == maxPending {
				break
			}
			sub := systray.AddMenuItem(p.Title(), p.Summary)
			sub.AddSubMenuItem(p.Summary, "").Disable()
			sub.AddSubMenuItem("Почему: "+p.Reason, "").Disable()
			onClick(sub.AddSubMenuItem("Разрешить", ""), func() { decide(t, p.ID, true, refresh) })
			onClick(sub.AddSubMenuItem("Отклонить", ""), func() { decide(t, p.ID, false, refresh) })
			onClick(sub.AddSubMenuItem("Подробнее в Web UI", ""), func() { openUI(t) })
		}
	}
	systray.AddSeparator()
	onClick(systray.AddMenuItem("Открыть Web UI", ""), func() { openUI(t) })
	onClick(systray.AddMenuItem("Выход из трея", ""), systray.Quit)
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
		_ = tray.Notify("gate4.ai: решение не принято", err.Error())
	}
	select {
	case refresh <- struct{}{}:
	default:
	}
}

func openUI(t *tray.Tray) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, err := t.LoginURL(ctx)
	if err == nil {
		err = cli.OpenURL(u)
	}
	if err != nil {
		_ = tray.Notify("gate4.ai: Web UI не открыт", err.Error())
	}
}
