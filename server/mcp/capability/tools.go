package capability

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared"

	// Use 2025 schema
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// ToolHandler defines the function signature for handling tool calls (using 2025 schema types).
// It receives the message (containing session and arguments) and returns metadata, result content, and error.
type ToolHandler func(msg *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error)

var _ shared.IServerCapability = (*ToolsCapability)(nil)

// ToolsCapability handles tool registration and invocation.
type ToolsCapability struct {
	manager  *transport.Manager
	logger   *zap.Logger
	mu       sync.RWMutex
	tools    map[string]*Tool                                      // Map tool name -> Tool
	handlers map[string]func(*shared.Message) (interface{}, error) // Map method -> handler function
}

// Tool represents a tool entity (using 2025 schema).
type Tool struct {
	schema.Tool // Embed the V2025 Tool definition (Name, Description, InputSchema, Annotations)
	Handler     ToolHandler
}

// NewToolsCapability creates a new ToolsCapability.
func NewToolsCapability(manager *transport.Manager, logger *zap.Logger) *ToolsCapability {
	tc := &ToolsCapability{
		manager: manager,
		logger:  logger.Named("tools-capability"),
		tools:   make(map[string]*Tool),
	}
	tc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"tools/list": tc.handleToolsList,
		"tools/call": tc.handleToolsCall,
	}

	return tc
}

func (tc *ToolsCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return tc.handlers
}

func (tc *ToolsCapability) SetCapabilities(s *schema.ServerCapabilities) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	// Only set the capability if tools are actually registered
	if len(tc.tools) > 0 {
		tc.logger.Debug("Setting Tools capability in ServerCapabilities")
		s.Tools = &schema.Capability{} // Mark capability as present
	}
}

// AddTool adds a new tool with the specified details (using 2025 schema).
func (tc *ToolsCapability) AddTool(name string, description string, inputSchema *schema.JSONSchemaProperty, annotations *schema.ToolAnnotations, handler ToolHandler) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if _, exists := tc.tools[name]; exists {
		return fmt.Errorf("tool with name '%s' already exists", name)
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil for tool '%s'", name)
	}

	tc.tools[name] = &Tool{
		Tool: schema.Tool{
			Name:        name,
			Description: description,
			InputSchema: inputSchema,
			Annotations: annotations,
		},
		Handler: handler,
	}

	tc.logger.Info("Added tool", zap.String("name", name))
	go tc.broadcastToolsChanged()
	return nil
}

// UpdateTool updates an existing tool.
func (tc *ToolsCapability) UpdateTool(name string, description string, inputSchema *schema.JSONSchemaProperty, annotations *schema.ToolAnnotations, handler ToolHandler) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tool, exists := tc.tools[name]
	if !exists {
		return fmt.Errorf("tool with name '%s' does not exist", name)
	}
	if handler == nil {
		return fmt.Errorf("handler cannot be nil for tool '%s'", name)
	}

	tool.Description = description
	tool.InputSchema = inputSchema
	tool.Annotations = annotations
	tool.Handler = handler

	tc.logger.Info("Updated tool", zap.String("name", name))
	go tc.broadcastToolsChanged()
	return nil
}

// DeleteTool removes a tool by name.
func (tc *ToolsCapability) DeleteTool(name string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if _, exists := tc.tools[name]; !exists {
		return fmt.Errorf("tool with name '%s' does not exist", name)
	}

	delete(tc.tools, name)
	tc.logger.Info("Deleted tool", zap.String("name", name))
	go tc.broadcastToolsChanged()
	return nil
}

// broadcastToolsChanged sends a "notifications/tools/list_changed" notification to eligible sessions.
// Kept internal for potential future direct use or testing.
func (tc *ToolsCapability) broadcastToolsChanged() {
	if tc.manager == nil {
		tc.logger.Error("Cannot broadcast tool list changed: manager not set")
		return
	}
	tc.manager.NotifyEligibleSessions("notifications/tools/list_changed", nil)
	tc.logger.Debug("Broadcasted tools list changed notification")
}

// handleToolsList handles the "tools/list" request from the client.
func (tc *ToolsCapability) handleToolsList(msg *shared.Message) (interface{}, error) {
	logger := tc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tools/list"))
	logger.Debug("Handling tools list request")

	tc.mu.RLock()
	defer tc.mu.RUnlock()

	// Parse pagination parameters (V2025)
	var params schema.ListToolsRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil {
			logger.Warn("Failed to unmarshal pagination params", zap.Error(err))
			return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
		}
	}
	// TODO: Implement pagination based on params.Cursor

	toolsList := make([]schema.Tool, 0, len(tc.tools))
	for _, tool := range tc.tools {
		toolsList = append(toolsList, tool.Tool) // Add embedded V2025 Tool
	}

	result := schema.ListToolsResult{
		Tools: toolsList,
		PaginatedResult: schema.PaginatedResult{
			NextCursor: nil,
		},
	}

	logger.Debug("Returning tool list", zap.Int("count", len(result.Tools)))
	return result, nil
}

// handleToolsCall handles the "tools/call" request from the client.
func (tc *ToolsCapability) handleToolsCall(msg *shared.Message) (interface{}, error) {
	logger := tc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "tools/call"))

	var params schema.CallToolRequestParams
	if msg.Params == nil {
		logger.Warn("Missing parameters in tools/call request")
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: "Missing params"})
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tools/call params", zap.Error(err))
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorInvalidParams, Message: fmt.Sprintf("Invalid parameters: %v", err)})
	}
	logger = logger.With(zap.String("toolName", params.Name))
	logger.Debug("Handling tool call request")

	tc.mu.RLock()
	tool, exists := tc.tools[params.Name]
	tc.mu.RUnlock()

	if !exists {
		logger.Warn("Tool not found")
		// Return a specific JSON-RPC error for method not found
		return nil, shared.NewJSONRPCError(&shared.JSONRPCError{Code: shared.JSONRPCErrorMethodNotFound, Message: fmt.Sprintf("Tool not found: %s", params.Name)})
	}

	// TODO: Add validation of params.Arguments against tool.InputSchema

	logger.Debug("Calling tool handler", zap.Any("arguments", params.Arguments))
	startTime := time.Now()
	meta, content, err := tool.Handler(msg, params.Arguments)
	duration := time.Since(startTime)

	// Prepare V2025 result
	result := schema.CallToolResult{
		Meta:    meta,
		Content: content,
		IsError: err != nil,
	}

	if err != nil {
		logger.Error("Tool handler returned an error", zap.Error(err), zap.Duration("duration", duration))
		// Even if the handler returns an error, the JSON-RPC call itself might succeed.
		// We return the CallToolResult indicating IsError=true.
		// The Go error 'err' should not be returned directly as the JSON-RPC error,
		// unless we specifically want to map it to a JSON-RPC error code.
		// For simplicity, return the result structure with IsError=true and nil Go error.
		return result, nil
	}

	logger.Info("Tool call successful", zap.Duration("duration", duration))
	return result, nil
}
