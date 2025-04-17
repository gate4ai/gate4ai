package server

import (
	"context"
	"net/http"

	"github.com/gate4ai/gate4ai/server/mcp"
	"github.com/gate4ai/gate4ai/server/mcp/capability"
	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/gate4ai/shared/config"
	"go.uber.org/zap"
)

type ServerBuilder struct {
	ctx          context.Context
	logger       *zap.Logger
	cfg          config.IConfig
	listenAddr   string
	manager      *mcp.Manager
	transport    *transport.Transport
	mux          *http.ServeMux
	capabilities []shared.ICapability // Store generic capabilities

	// Capability instances (created lazily)
	baseCap       *capability.BaseCapability
	toolsCap      *capability.ToolsCapability
	resourcesCap  *capability.ResourcesCapability
	promptsCap    *capability.PromptsCapability
	completionCap *capability.CompletionCapability
	//a2aCap        *a2a.A2ACapability

	// Flags to control route registration
	registerMCPRoutes bool
	registerA2ARoutes bool
	a2aSkills         []a2aSchema.AgentSkill // Store skills added via options
	// a2aAgentHandler  a2a.A2AHandler        // Store the A2A agent logic handler
}

// EnsureMCPBaseCapability creates the BaseCapability if it doesn't exist.
func (b *ServerBuilder) EnsureMCPBaseCapability() error {
	if b.baseCap == nil {
		b.logger.Debug("Initializing BaseCapability")
		b.baseCap = capability.NewBase(b.logger, b.manager)
		b.capabilities = append(b.capabilities, b.baseCap)
		b.registerMCPRoutes = true // Base capability implies MCP routes are needed
	}
	return nil
}

// EnsureToolsCapability creates the ToolsCapability if it doesn't exist.
func (b *ServerBuilder) EnsureToolsCapability() (*capability.ToolsCapability, error) {
	if err := b.EnsureMCPBaseCapability(); err != nil {
		return nil, err
	}
	if b.toolsCap == nil {
		b.logger.Debug("Initializing ToolsCapability")
		b.toolsCap = capability.NewToolsCapability(b.manager, b.logger)
		b.capabilities = append(b.capabilities, b.toolsCap)
	}
	return b.toolsCap, nil
}

// EnsurePromptsCapability creates the PromptsCapability if it doesn't exist.
func (b *ServerBuilder) EnsurePromptsCapability() (*capability.PromptsCapability, error) {
	if err := b.EnsureMCPBaseCapability(); err != nil {
		return nil, err
	}
	if b.promptsCap == nil {
		b.logger.Debug("Initializing PromptsCapability")
		b.promptsCap = capability.NewPromptsCapability(b.logger, b.manager) // Pass manager if needed
		b.capabilities = append(b.capabilities, b.promptsCap)
	}
	return b.promptsCap, nil
}

// EnsureResourcesCapability creates the ResourcesCapability if it doesn't exist.
func (b *ServerBuilder) EnsureResourcesCapability() (*capability.ResourcesCapability, error) {
	if err := b.EnsureMCPBaseCapability(); err != nil {
		return nil, err
	}
	if b.resourcesCap == nil {
		b.logger.Debug("Initializing ResourcesCapability")
		b.resourcesCap = capability.NewResourcesCapability(b.manager, b.logger) // Pass manager
		b.capabilities = append(b.capabilities, b.resourcesCap)
	}
	return b.resourcesCap, nil
}

// EnsureCompletionCapability creates the CompletionCapability if it doesn't exist.
func (b *ServerBuilder) EnsureCompletionCapability() (*capability.CompletionCapability, error) {
	if err := b.EnsureMCPBaseCapability(); err != nil {
		return nil, err
	}
	if b.completionCap == nil {
		b.logger.Debug("Initializing CompletionCapability")
		b.completionCap = capability.NewCompletionCapability(b.logger)
		b.capabilities = append(b.capabilities, b.completionCap)
	}
	return b.completionCap, nil
}

// ServerOption defines a function type for configuring the ServerBuilder.
type ServerOption func(*ServerBuilder) error
