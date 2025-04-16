package mcpClient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	// Use 2025 schema
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// GetResourceTemplatesResult contains the result of a resource templates list request (using 2025 schema).
type GetResourceTemplatesResult struct {
	Templates []schema.ResourceTemplate
	Err       error
}

// UpdateResourceTemplates retrieves all available resource templates from the server and updates the session cache.
// It handles pagination automatically using SendRequestSync.
// Returns a channel that is closed when the update is complete or fails.
func (s *Session) UpdateResourceTemplates(ctx context.Context) chan struct{} {
	logger := s.BaseSession.Logger.With(zap.String("operation", "UpdateResourceTemplates"))
	done := make(chan struct{}, 1)

	// Goroutine to handle initialization and request sending
	go func() {
		allTemplates := make([]schema.ResourceTemplate, 0)
		initErr := <-s.Open() // Wait for session initialization
		if initErr != nil {
			logger.Error("Session initialization failed", zap.Error(initErr))
			return
		}
		logger.Debug("Session initialized, proceeding to fetch resource templates")

		for msg := range s.SendRequestSync("resources/templates/list", &schema.ListResourceTemplatesRequestParams{}) {
			if msg == nil {
				logger.Error("resources/templates/list - Received nil message")
				continue
			}
			if msg.Error != nil {
				logger.Error("resources/templates/list - Failed to send initial templates list request", zap.Error(msg.Error))
				continue
			}
			if msg.Result == nil {
				logger.Error("resources/templates/list - Resource template list result is nil")
				continue
			}
			var listTemplatesResult schema.ListResourceTemplatesResult
			if err := json.Unmarshal(*msg.Result, &listTemplatesResult); err != nil {
				logger.Error("resources/templates/list - Failed to unmarshal resource templates result", zap.Error(err))
				continue
			}
			if len(listTemplatesResult.ResourceTemplates) > 0 {
				allTemplates = append(allTemplates, listTemplatesResult.ResourceTemplates...)
				logger.Debug("resources/templates/list - Appended templates", zap.Int("count", len(listTemplatesResult.ResourceTemplates)))
			}
		}
		s.Locker.Lock()
		s.resourceTemplates = allTemplates
		s.resourceTemplatesInitialized = true
		s.Locker.Unlock()
		close(done)
	}()

	return done
}

// GetResourceTemplatesList retrieves all available resource templates from the server.
// If the templates haven't been initialized yet, it will fetch them first.
// Returns a channel emitting a 2025 schema resource templates list result.
func (s *Session) GetResourceTemplatesList(ctx context.Context) chan GetResourceTemplatesResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "GetResourceTemplatesList"))
	resultChan := make(chan GetResourceTemplatesResult, 1) // Buffered channel

	go func() {
		defer close(resultChan) // Ensure channel is closed

		s.Locker.RLock()
		initialized := s.resourceTemplatesInitialized
		s.Locker.RUnlock()

		if !initialized {
			logger.Debug("Resource templates not initialized, updating...")
			select {
			case <-s.UpdateResourceTemplates(ctx):
				logger.Debug("Resource templates updated")
				// Re-check status
				s.Locker.RLock()
				initialized = s.resourceTemplatesInitialized
				s.Locker.RUnlock()
				if !initialized {
					logger.Error("Resource templates still not initialized after update attempt")
					resultChan <- GetResourceTemplatesResult{nil, errors.New("failed to initialize resource templates")}
					return
				}
			case <-ctx.Done():
				logger.Error("Context cancelled while waiting for resource templates update", zap.Error(ctx.Err()))
				resultChan <- GetResourceTemplatesResult{nil, fmt.Errorf("context cancelled: %w", ctx.Err())}
				return
			}
		}

		s.Locker.RLock()
		templatesCopy := make([]schema.ResourceTemplate, len(s.resourceTemplates))
		copy(templatesCopy, s.resourceTemplates)
		s.Locker.RUnlock()

		logger.Debug("Returning resource templates list", zap.Int("count", len(templatesCopy)))
		resultChan <- GetResourceTemplatesResult{templatesCopy, nil}
	}()

	return resultChan
}
