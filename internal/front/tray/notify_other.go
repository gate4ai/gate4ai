// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin && !windows

package tray

import "errors"

// Notify на этой ОС не поддерживается.
func Notify(title, body string) error {
	return errors.New("notifications are not supported on this OS")
}
