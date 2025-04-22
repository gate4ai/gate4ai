package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gate4ai/gate4ai/server/a2a"
	"github.com/gate4ai/gate4ai/server/mcp/capability"
	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared"
	"github.com/gate4ai/gate4ai/shared/config"
	"go.uber.org/zap"
)

type ServerBuilder struct {
	ctx          context.Context
	logger       *zap.Logger
	cfg          config.IConfig
	listenAddr   string
	manager      *transport.Manager
	transport    *transport.Transport
	mux          *http.ServeMux
	capabilities []shared.ICapability // Store generic capabilities

	// Capability instances (created lazily)
	baseCap       *capability.BaseCapability
	toolsCap      *capability.ToolsCapability
	resourcesCap  *capability.ResourcesCapability
	promptsCap    *capability.PromptsCapability
	completionCap *capability.CompletionCapability
	a2aCap        *a2a.A2ACapability

	// Flags to control route registration
	registerMCPRoutes bool
	registerA2ARoutes bool
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

// EnsureA2ACapability creates the A2ACapability if it doesn't exist.
// Requires a TaskStore and A2AHandler to be provided via options (e.g., WithA2ACapability).
func (b *ServerBuilder) EnsureA2ACapability(store a2a.TaskStore, handler a2a.A2AHandler) (*a2a.A2ACapability, error) {
	if b.manager == nil { // manager is needed by A2ACapability
		return nil, fmt.Errorf("cannot initialize A2ACapability without MCP manager")
	}
	if b.a2aCap == nil {
		b.logger.Debug("Initializing A2ACapability")
		// Manager is now passed during construction
		b.a2aCap = a2a.NewA2ACapability(b.logger, b.manager, store, handler)
		b.capabilities = append(b.capabilities, b.a2aCap)
		b.registerA2ARoutes = true // A2A capability implies A2A routes are needed
	} else {
		b.logger.Error("A2ACapability already initialized")
	}
	return b.a2aCap, nil
}

// ServerOption defines a function type for configuring the ServerBuilder.
type ServerOption func(*ServerBuilder) error
