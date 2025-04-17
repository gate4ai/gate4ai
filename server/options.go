package server

import (
	"github.com/gate4ai/gate4ai/server/mcp/capability"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
)

// WithMCPPrompt is a server option to add an MCP prompt.
func WithMCPPrompt(name string, description string, handler capability.PromptHandler) ServerOption {
	return func(b *ServerBuilder) error {
		if err := b.EnsureMCPBaseCapability(); err != nil {
			return err
		}
		promptsCap, err := b.EnsurePromptsCapability()
		if err != nil {
			return err
		}
		return promptsCap.AddPrompt(name, description, handler)
	}
}

// WithMCPPromptTemplate is a server option to add an MCP prompt template.
func WithMCPPromptTemplate(name string, description string, arguments []schema.PromptArgument, handler capability.PromptHandler) ServerOption {
	return func(b *ServerBuilder) error {
		if err := b.EnsureMCPBaseCapability(); err != nil {
			return err
		}
		promptsCap, err := b.EnsurePromptsCapability()
		if err != nil {
			return err
		}
		return promptsCap.AddTemplate(name, description, arguments, handler)
	}
}

// WithMCPResource is a server option to add an MCP resource.
func WithMCPResource(uri string, name string, description string, mimeType string, handler capability.ResourceHandler) ServerOption {
	return func(b *ServerBuilder) error {
		if err := b.EnsureMCPBaseCapability(); err != nil {
			return err
		}
		resCap, err := b.EnsureResourcesCapability()
		if err != nil {
			return err
		}
		return resCap.AddResource(uri, name, description, mimeType, handler)
	}
}

// WithMCPResourceTemplate is a server option to add an MCP resource template.
func WithMCPResourceTemplate(uriTemplate string, name string, description string, mimeType string, handler capability.ResourceHandler) ServerOption {
	return func(b *ServerBuilder) error {
		if err := b.EnsureMCPBaseCapability(); err != nil {
			return err
		}
		resCap, err := b.EnsureResourcesCapability()
		if err != nil {
			return err
		}
		return resCap.AddResourceTemplate(uriTemplate, name, description, mimeType, handler)
	}
}

// WithMCPSubscriptionHandler is a server option to add a handler for subscription events.
func WithMCPSubscriptionHandler(handler capability.SubscriptionHandler) ServerOption {
	return func(b *ServerBuilder) error {
		if err := b.EnsureMCPBaseCapability(); err != nil {
			return err
		}
		resCap, err := b.EnsureResourcesCapability()
		if err != nil {
			return err
		}
		resCap.AddSubscriptionHandler(handler)
		return nil
	}
}

// WithMCPTool is a server option to add an MCP tool.
func WithMCPTool(name string, description string, inputSchema *schema.JSONSchemaProperty, annotations *schema.ToolAnnotations, handler capability.ToolHandler) ServerOption {
	return func(b *ServerBuilder) error {
		// Ensure Base and Tools capabilities are initialized
		if err := b.EnsureMCPBaseCapability(); err != nil {
			return err
		}
		toolsCap, err := b.EnsureToolsCapability()
		if err != nil {
			return err
		}

		// Add the tool using the capability's method
		return toolsCap.AddTool(name, description, inputSchema, annotations, handler)
	}
}
