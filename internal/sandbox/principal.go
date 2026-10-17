// SPDX-License-Identifier: AGPL-3.0-only

package sandbox

import "github.com/gate4ai/gate4ai/internal/platform/auth"

func agentPrincipal(agent string) auth.Principal { return auth.AgentVia(agent, "run") }
