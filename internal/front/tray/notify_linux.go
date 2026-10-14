// SPDX-License-Identifier: FSL-1.1-ALv2

package tray

import "github.com/godbus/dbus/v5"

// Notify показывает системное уведомление (org.freedesktop.Notifications).
func Notify(title, body string) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return err
	}
	obj := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	return obj.Call("org.freedesktop.Notifications.Notify", 0,
		"gate4.ai", uint32(0), "", title, body, []string{}, map[string]dbus.Variant{}, int32(-1)).Err
}
