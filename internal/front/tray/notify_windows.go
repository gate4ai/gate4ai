// SPDX-License-Identifier: FSL-1.1-ALv2

package tray

import toast "git.sr.ht/~jackmordaunt/go-toast"

// Notify показывает toast-уведомление Windows.
func Notify(title, body string) error {
	n := toast.Notification{AppID: "gate4.ai", Title: title, Body: body}
	return n.Push()
}
