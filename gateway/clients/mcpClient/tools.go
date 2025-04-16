package mcpClient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gate4ai/mcp/shared"
	// Use 2025 schema
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

func (s *Session) UpdateTools(ctx context.Context) chan struct{} {
	logger := s.BaseSession.Logger.With(zap.String("operation", "UpdateTools"))
	done := make(chan struct{}, 1)

	// Goroutine to handle initialization and request sending
	go func() {
		defer close(done)
		allTools := make([]schema.Tool, 0)
		initErr := <-s.Open() // Wait for session initialization
		if initErr != nil {
			logger.Error("Session initialization failed", zap.Error(initErr))
			return
		}
		logger.Debug("Session initialized, proceeding to fetch tools")

		for msg := range s.SendRequestSync("tools/list", &schema.ListToolsRequestParams{}) {
			if msg == nil {
				logger.Error("tools/list - Received nil message")
				continue
			}
			if msg.Error != nil {
				logger.Error("tools/list - Failed to send initial tools list request", zap.Error(msg.Error))
				continue
			}
			if msg.Result == nil {
				logger.Error("tools/list - Tool list result is nil")
				continue
			}
			var listToolsResult schema.ListToolsResult
			if err := json.Unmarshal(*msg.Result, &listToolsResult); err != nil {
				logger.Error("tools/list - Failed to unmarshal tool list result", zap.Error(err))
				continue
			}
			if len(listToolsResult.Tools) > 0 {
				allTools = append(allTools, listToolsResult.Tools...)
				logger.Debug("tools/list - Appended tools", zap.Int("count", len(listToolsResult.Tools)))
			}
		}
		s.Locker.Lock()
		s.tools = allTools
		s.toolsInitialized = true
		s.Locker.Unlock()
	}()

	return done
}

// GetToolsResult contains the result of a tools list request (using 2025 schema).
type GetToolsResult struct {
	Tools []schema.Tool
	Err   error
}

// GetTools retrieves all available tools from the server.
// It returns a channel that will emit the 2025 schema tools list result.
func (s *Session) GetTools(ctx context.Context) chan GetToolsResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "GetTools"))
	resultChan := make(chan GetToolsResult, 1) // Buffered channel

	go func() {
		defer close(resultChan) // Ensure channel is closed

		s.Locker.RLock()
		initialized := s.toolsInitialized
		s.Locker.RUnlock()

		if !initialized {
			logger.Debug("Tools not initialized, updating...")
			select {
			case <-s.UpdateTools(ctx):
				logger.Debug("Tools updated")
				// Re-check status
				s.Locker.RLock()
				initialized = s.toolsInitialized
				s.Locker.RUnlock()
				if !initialized {
					logger.Error("Tools still not initialized after update attempt")
					resultChan <- GetToolsResult{nil, errors.New("failed to initialize tools")}
					return
				}
			case <-ctx.Done():
				logger.Error("Context cancelled while waiting for tools update", zap.Error(ctx.Err()))
				resultChan <- GetToolsResult{nil, fmt.Errorf("context cancelled: %w", ctx.Err())}
				return
			}
		}

		s.Locker.RLock()
		toolsCopy := make([]schema.Tool, len(s.tools))
		copy(toolsCopy, s.tools)
		s.Locker.RUnlock()

		logger.Debug("Returning tools list", zap.Int("count", len(toolsCopy)))
		resultChan <- GetToolsResult{toolsCopy, nil}
	}()

	return resultChan
}

// CallToolResult contains the result of a tool call request (using 2025 schema).
type CallToolResult struct {
	Result *schema.CallToolResult // Use 2025 schema type
	Error  error
}

// CallTool invokes a specific tool on the server by name with given arguments.
// Returns a channel emitting a 2025 schema result.
func (s *Session) CallTool(ctx context.Context, name string, arguments map[string]interface{}) chan CallToolResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "CallTool"), zap.String("toolName", name))
	resultChan := make(chan CallToolResult, 1) // Buffered channel

	if name == "" {
		err := errors.New("tool name cannot be empty")
		logger.Error("Invalid request", zap.Error(err))
		resultChan <- CallToolResult{Error: err}
		close(resultChan)
		return resultChan
	}

	go func() {
		// Use 2025 schema request parameters
		params := &schema.CallToolRequestParams{ // V2025 uses CallToolRequestParams
			Name:      name,
			Arguments: arguments,
		}

		// Define callback for the response
		callback := func(msg *shared.Message) {
			defer close(resultChan) // Ensure channel is closed
			responseLogger := s.BaseSession.Logger.With(zap.String("operation", "callToolCallback"), zap.String("toolName", name))
			if msg == nil {
				responseLogger.Error("Received nil message")
				resultChan <- CallToolResult{Error: errors.New("protocol error: received nil response")}
				return
			}

			if msg.Error != nil {
				// Check if it's a specific "tool error" indicated by IsError=true in the result,
				// or a general JSON-RPC error.
				responseLogger.Warn("Backend returned error for tool call", zap.Error(msg.Error))
				// Assume generic error if msg.Error is set
				resultChan <- CallToolResult{Error: fmt.Errorf("backend error: %w", msg.Error)}
				return
			}

			if msg.Result == nil {
				responseLogger.Error("Tool call result is nil")
				resultChan <- CallToolResult{Error: errors.New("protocol error: tool call result is nil")}
				return
			}

			// Use 2025 schema result type
			var callToolResult schema.CallToolResult
			if err := json.Unmarshal(*msg.Result, &callToolResult); err != nil {
				responseLogger.Error("Failed to unmarshal tool call result", zap.Error(err))
				resultChan <- CallToolResult{Error: fmt.Errorf("failed to parse backend response: %w", err)}
				return
			}
			msg.Processed = true

			// Check the IsError flag within the result structure
			if callToolResult.IsError {
				responseLogger.Warn("Tool call executed but resulted in an error")
				// Construct an error message indicating the tool itself failed
				toolErr := fmt.Errorf("tool '%s' execution failed on backend", name)
				// Optionally try to extract more details from callToolResult.Content if available
				resultChan <- CallToolResult{Result: &callToolResult, Error: toolErr}
			} else {
				responseLogger.Debug("Successfully called tool")
				resultChan <- CallToolResult{Result: &callToolResult, Error: nil}
			}
		}

		// Send the request
		logger.Debug("Sending tools/call request")
		_, err := s.SendRequest("tools/call", params, callback)
		if err != nil {
			logger.Error("Failed to send tool call request", zap.Error(err))
			// Try to send error through channel
			select {
			case resultChan <- CallToolResult{Error: fmt.Errorf("failed to send request: %w", err)}:
			default:
				logger.Error("Result channel closed before error could be sent", zap.Error(err))
			}
		}
	}()

	return resultChan
}
