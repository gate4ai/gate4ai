// SPDX-License-Identifier: AGPL-3.0-only

//go:build !gate4ai_devstand

package agentid

// DevStand — сборка для тестового стенда; в обычной сборке проверку агента
// не выключить: иначе агент перезапустил бы daemon с GATE4AI_OWNER_CHECK=off
// и вошёл на сокет владельца.
const DevStand = false
