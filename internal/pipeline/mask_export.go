// SPDX-License-Identifier: AGPL-3.0-only

package pipeline

// MaskURLText — текст с *** вместо одноразовых кодов и токенов в адресах
// (для уведомлений каналов подтверждения: Telegram, рабочий стол).
func MaskURLText(s string) string {
	if m, ok := maskURLs(s).(string); ok {
		return m
	}
	return s
}
