package shared

import "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"

type CapabilityOption string

type ICapability interface {
	GetHandlers() map[string]func(*Message) (interface{}, error)
}

type IServerCapability interface {
	SetCapabilities(s *schema.ServerCapabilities)
}

type IClientCapability interface {
	SetCapabilities(s *schema.ClientCapabilities)
}
