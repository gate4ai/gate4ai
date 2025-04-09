package capability

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/shared"

	// Use 2025 schema
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// ToolHandler defines the function signature for handling tool calls (using 2025 schema types).
// It receives the message (containing session and arguments) and returns metadata, result content, and error.
type ToolHandler func(msg *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error)

// ToolsCapability handles tool registration and invocation.
type ToolsCapability struct {
	manager  *mcp.Manager
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
func NewToolsCapability(manager *mcp.Manager, logger *zap.Logger) *ToolsCapability {
	tc := &ToolsCapability{
		manager: manager,
		logger:  logger,
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
	tc.logger.Debug("SetCapabilities called on ToolsCapability")
	s.Tools = &schema.Capability{}
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
			InputSchema: inputSchema, // Assign raw message
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

	// Update fields
	tool.Description = description
	tool.InputSchema = inputSchema
	tool.Annotations = annotations
	tool.Handler = handler
	// tool.Name should not change ideally, as it's the key

	tc.logger.Info("Updated tool", zap.String("name", name))
	go tc.broadcastToolsChanged() // Notify clients
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
	go tc.broadcastToolsChanged() // Notify clients
	return nil
}

// broadcastToolsChanged sends a "notifications/tools/list_changed" notification to eligible sessions.
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
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}
	// TODO: Implement pagination based on params.Cursor

	// Collect all tools
	toolsList := make([]schema.Tool, 0, len(tc.tools)) // V2025 result expects []Tool
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

	// Parse parameters (V2025)
	var params schema.CallToolRequestParams
	if msg.Params == nil {
		logger.Warn("Missing parameters in tools/call request")
		return nil, fmt.Errorf("invalid request: missing params")
	}
	if err := json.Unmarshal(*msg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal tools/call params", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	logger = logger.With(zap.String("toolName", params.Name))
	logger.Debug("Handling tool call request")

	tc.mu.RLock()
	tool, exists := tc.tools[params.Name]
	tc.mu.RUnlock()

	if !exists {
		logger.Warn("Tool not found")
		return nil, fmt.Errorf("tool not found: %s", params.Name)
	}

	// TODO: Add validation of params.Arguments against tool.InputSchema if required by server policy.

	// Log tool call details
	logger.Debug("Calling tool handler", zap.Any("arguments", params.Arguments))

	// Call the tool handler
	startTime := time.Now()
	meta, content, err := tool.Handler(msg, params.Arguments) // Pass message and arguments
	duration := time.Since(startTime)

	// Prepare V2025 result
	result := schema.CallToolResult{
		Meta:    meta,
		Content: content, // Handler should return []schema.Content (V2025)
		IsError: err != nil,
	}

	// Log the outcome
	if err != nil {
		logger.Error("Tool handler returned an error", zap.Error(err), zap.Duration("duration", duration))
		// The error itself is not part of the V2025 CallToolResult structure.
		// IsError=true signals failure. Content might contain error details.
		// We return the result structure, not the Go error directly.
		return result, nil // Return result indicating error, nil Go error for JSON-RPC
	}

	logger.Info("Tool call successful", zap.Duration("duration", duration)) // Use Info for successful calls
	return result, nil                                                      // Return successful result
}
