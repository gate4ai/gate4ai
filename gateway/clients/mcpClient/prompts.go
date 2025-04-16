package mcpClient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gate4ai/mcp/shared"
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// GetPromptAsyncResult represents the result of an asynchronous GetPrompt call
type GetPromptAsyncResult struct {
	Result *schema.GetPromptResult
	Error  error
}

// GetPromptsResult contains the result of a prompts list request (using 2025 schema).
type GetPromptsResult struct {
	Prompts []schema.Prompt
	Error   error
}

// UpdatePrompts retrieves all available prompts from the server and updates the Session.
// It handles pagination automatically, making multiple requests if necessary.
// Returns a channel that is closed when the update is complete or fails.
func (s *Session) UpdatePrompts(ctx context.Context) chan struct{} {
	logger := s.BaseSession.Logger.With(zap.String("operation", "UpdatePrompts"))
	done := make(chan struct{}, 1)

	// Goroutine to handle initialization and request sending
	go func() {
		allPrompts := make([]schema.Prompt, 0)
		initErr := <-s.Open() // Wait for session initialization
		if initErr != nil {
			logger.Error("Session initialization failed", zap.Error(initErr))
			return
		}
		logger.Debug("Session initialized, proceeding to fetch prompts")

		for msg := range s.SendRequestSync("prompts/list", &schema.ListPromptsRequestParams{}) {
			if msg == nil {
				logger.Error("prompts/list - Received nil message")
				continue
			}
			if msg.Error != nil {
				logger.Error("prompts/list - Failed to send initial prompts list request", zap.Error(msg.Error))
				continue
			}
			if msg.Result == nil {
				logger.Error("prompts/list - Prompt list result is nil")
				continue
			}
			var listPromptsResult schema.ListPromptsResult
			if err := json.Unmarshal(*msg.Result, &listPromptsResult); err != nil {
				logger.Error("prompts/list - Failed to unmarshal prompt list result", zap.Error(err))
				continue
			}
			if len(listPromptsResult.Prompts) > 0 {
				allPrompts = append(allPrompts, listPromptsResult.Prompts...)
				logger.Debug("prompts/list - Appended prompts", zap.Int("count", len(listPromptsResult.Prompts)))
			}
		}
		s.Locker.Lock()
		s.prompts = allPrompts
		s.promptsInitialized = true
		s.Locker.Unlock()
		close(done)
	}()

	return done
}

// GetPrompts returns the list of available prompts.
// If the prompts haven't been initialized yet, it will fetch them first.
// Returns a channel emitting a 2025 schema prompts list result.
func (s *Session) GetPrompts(ctx context.Context) chan GetPromptsResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "GetPrompts"))
	resultChan := make(chan GetPromptsResult, 1) // Buffered channel

	go func() {
		defer close(resultChan) // Ensure channel is closed

		s.Locker.RLock()
		initialized := s.promptsInitialized
		s.Locker.RUnlock()

		if !initialized {
			logger.Debug("Prompts not initialized, updating...")
			select {
			case <-s.UpdatePrompts(ctx):
				logger.Debug("Prompts updated")
				// Re-check status
				s.Locker.RLock()
				initialized = s.promptsInitialized
				s.Locker.RUnlock()
				if !initialized {
					logger.Error("Prompts still not initialized after update attempt")
					resultChan <- GetPromptsResult{Prompts: nil, Error: errors.New("failed to initialize prompts")}
					return
				}
			case <-ctx.Done():
				logger.Error("Context cancelled while waiting for prompts update", zap.Error(ctx.Err()))
				resultChan <- GetPromptsResult{Prompts: nil, Error: fmt.Errorf("context cancelled: %w", ctx.Err())}
				return
			}
		}

		s.Locker.RLock()
		promptsCopy := make([]schema.Prompt, len(s.prompts))
		copy(promptsCopy, s.prompts)
		s.Locker.RUnlock()

		logger.Debug("Returning prompts list", zap.Int("count", len(promptsCopy)))
		resultChan <- GetPromptsResult{Prompts: promptsCopy, Error: nil}
	}()

	return resultChan
}

// GetPrompt retrieves a specific prompt from the server by name.
// It allows passing arguments for prompt templating.
// Returns a channel emitting a 2025 schema result.
func (s *Session) GetPrompt(ctx context.Context, name string, arguments map[string]string) chan GetPromptAsyncResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "GetPrompt"), zap.String("promptName", name))
	resultChan := make(chan GetPromptAsyncResult, 1) // Buffered channel

	if name == "" {
		err := errors.New("prompt name cannot be empty")
		logger.Error("Invalid request", zap.Error(err))
		resultChan <- GetPromptAsyncResult{Error: err}
		close(resultChan)
		return resultChan
	}

	// Goroutine to handle request sending and response processing
	go func() {
		// Use 2025 schema request parameters
		params := &schema.GetPromptRequestParams{
			Name:      name,
			Arguments: arguments,
		}

		// Define callback for the response
		callback := func(msg *shared.Message) {
			defer close(resultChan) // Ensure channel is closed on exit
			responseLogger := s.BaseSession.Logger.With(zap.String("operation", "getPromptCallback"), zap.String("promptName", name))
			if msg == nil {
				responseLogger.Error("Received nil message")
				resultChan <- GetPromptAsyncResult{Error: errors.New("protocol error: received nil response")}
				return
			}

			if msg.Error != nil {
				responseLogger.Error("Backend returned error", zap.Error(msg.Error))
				resultChan <- GetPromptAsyncResult{Error: fmt.Errorf("backend error: %w", msg.Error)}
				return
			}

			if msg.Result == nil {
				responseLogger.Error("Prompt result is nil")
				resultChan <- GetPromptAsyncResult{Error: errors.New("protocol error: prompt result is nil")}
				return
			}

			// Use 2025 schema result type
			promptResult := &schema.GetPromptResult{}
			if err := json.Unmarshal(*msg.Result, promptResult); err != nil {
				responseLogger.Error("Failed to unmarshal prompt result", zap.Error(err))
				resultChan <- GetPromptAsyncResult{Error: fmt.Errorf("failed to parse backend response: %w", err)}
				return
			}
			msg.Processed = true
			responseLogger.Debug("Successfully retrieved prompt")
			resultChan <- GetPromptAsyncResult{Result: promptResult, Error: nil}
		}

		// Send the request
		logger.Debug("Sending prompts/get request")
		_, err := s.SendRequest("prompts/get", params, callback)
		if err != nil {
			logger.Error("Failed to send prompt get request", zap.Error(err))
			// Try to send error through channel if it's still open
			select {
			case resultChan <- GetPromptAsyncResult{Error: fmt.Errorf("failed to send request: %w", err)}:
				defer close(resultChan) // Ensure channel is closed on exit
			default:
				logger.Error("Result channel closed before error could be sent", zap.Error(err))
			}
		}
	}()

	return resultChan
}
