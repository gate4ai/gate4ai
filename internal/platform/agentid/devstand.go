// SPDX-License-Identifier: AGPL-3.0-only

//go:build gate4ai_devstand

package agentid

// DevStand — сборка для тестового стенда (go build -tags gate4ai_devstand):
// в ней GATE4AI_OWNER_CHECK=off выключает проверку агента. Выпускаемые
// бинарники собираются без тега, и переменная в них ничего не делает.
const DevStand = true
