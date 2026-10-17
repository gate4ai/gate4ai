// SPDX-License-Identifier: AGPL-3.0-only

package tray

import toast "git.sr.ht/~jackmordaunt/go-toast"

// Notify показывает toast-уведомление Windows.
func Notify(title, body string) error {
	n := toast.Notification{AppID: "gate4.ai", Title: title, Body: body}
	return n.Push()
}
