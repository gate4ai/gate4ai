// SPDX-License-Identifier: FSL-1.1-ALv2

package sandbox

import "github.com/gate4ai/gate4ai/internal/platform/auth"

func agentPrincipal(agent string) auth.Principal { return auth.AgentVia(agent, "run") }
