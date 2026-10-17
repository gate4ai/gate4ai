// SPDX-License-Identifier: AGPL-3.0-only

package tray

import "os/exec"

// Notify показывает системное уведомление через osascript. Текст передаётся
// аргументами скрипта, а не подставляется в него.
func Notify(title, body string) error {
	return exec.Command("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body).Run()
}
