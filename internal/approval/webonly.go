// SPDX-License-Identifier: AGPL-3.0-only

package approval

import "strings"

// WebOnly — запрос, который одним нажатием в трее или Telegram не одобряется:
// самонастройка gate4ai (change_policy: правила, установка коннекторов, учётки).
// Человек должен видеть правки целиком, поэтому «Разрешить» — только в Web UI
// (code review E.5). Отклонить можно в любом канале.
func WebOnly(action string) bool { return strings.HasPrefix(action, "gate4ai.") }

// WebOnlyHint — подсказка для каналов без кнопки «Разрешить».
const WebOnlyHint = "Разрешить это изменение gate4ai можно только в Web UI (gate4ai ui): там видны все правки."
