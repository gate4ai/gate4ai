package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time" // Import time

	"github.com/gate4ai/mcp/shared"
	// Use 2025 schema for request parsing, although structure is same as 2024
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// gw_tools_call handles the "tools/call" request from the client.
func (c *GatewayCapability) gw_tools_call(inputMsg *shared.Message) (interface{}, error) {
	// Use SugaredLogger and add context
	logger := c.logger.Sugar().With("msgID", inputMsg.ID.String(), "method", "tools/call")
	logger.Debug("Processing request")

	// Parse the input parameters using 2025 schema type
	var params schema.CallToolRequestParams // V2025 uses CallToolRequestParams
	if inputMsg.Params == nil {
		return nil, fmt.Errorf("missing parameters")
	}
	if err := json.Unmarshal(*inputMsg.Params, &params); err != nil {
		logger.Errorw("Failed to unmarshal parameters", "error", err)
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	logger = logger.With("toolName", params.Name) // Add tool name context

	// Get all tools from the available servers (handles fetching, conflict resolution)
	// Pass the non-sugared logger to GetTools
	tools, err := c.GetTools(inputMsg, c.logger.With(zap.String("msgID", inputMsg.ID.String())))
	if err != nil {
		logger.Errorw("Failed to get tools list", "error", err)
		return nil, fmt.Errorf("failed to get tools: %w", err)
	}

	// Find which server has this tool (using potentially modified name)
	var selectedTool *tool
	for _, t := range tools {
		if t != nil && t.Name == params.Name { // Add nil check
			selectedTool = t
			break
		}
	}

	if selectedTool == nil {
		logger.Warnw("Tool not found in any backend")
		return nil, fmt.Errorf("tool not found: %s", params.Name)
	}

	logger.Debugw("Found tool, forwarding call to backend",
		"backendServerID", selectedTool.serverSlug,
		"originalName", selectedTool.originalName)

	// Get the backend session for the server that has this tool
	backendSession, err := c.getBackendSession(inputMsg.Session, selectedTool.serverSlug)
	if err != nil {
		logger.Errorw("Failed to get backend session", "serverID", selectedTool.serverSlug, "error", err)
		return nil, fmt.Errorf("failed to get backend session for server %s: %w", selectedTool.serverSlug, err)
	}
	if backendSession == nil {
		logger.Errorw("Backend session is nil after successful retrieval", "serverID", selectedTool.serverSlug)
		return nil, fmt.Errorf("internal error: failed to get valid backend session for server %s", selectedTool.serverSlug)
	}

	// Call the tool on the backend using the ORIGINAL tool name
	toolName := selectedTool.originalName

	// Arguments are already map[string]interface{} in V2025 params
	args := params.Arguments

	// Use a timeout context for the backend call
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Timeout for tool execution
	defer cancel()

	resultChan := backendSession.CallTool(ctx, toolName, args)
	result := <-resultChan // Wait for the result from the backend

	// Handle the result (CallToolResult uses 2025 schema)
	if result.Error != nil {
		// Error could be connection error OR IsError=true from backend
		logger.Errorw("Failed to call tool on backend",
			"server", selectedTool.serverSlug,
			"tool", toolName,
			"error", result.Error)
		// Return the error received from the client call wrapper
		return nil, fmt.Errorf("failed to call tool '%s' on backend: %w", toolName, result.Error)
	}

	if result.Result == nil {
		// Should not happen if Error is nil, but check defensively
		err := fmt.Errorf("nil result received from backend %s for tool '%s'", selectedTool.serverSlug, toolName)
		logger.Errorw(err.Error())
		return nil, err
	}

	// Check IsError flag within the result from the backend
	if result.Result.IsError {
		logger.Warnw("Tool call succeeded but backend reported tool error",
			"server", selectedTool.serverSlug,
			"tool", toolName)
		// Return the result structure which indicates IsError=true
		// The error message itself is typically within the Content field in this case.
		return result.Result, nil // Return the result containing IsError=true
	}

	logger.Debugw("Successfully called tool via backend")
	// Return the successful result obtained from the backend
	return result.Result, nil
}
