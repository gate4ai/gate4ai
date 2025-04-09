package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gate4ai/mcp/shared"
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

func (s *Session) UpdateResources(ctx context.Context) chan struct{} {
	logger := s.BaseSession.Logger.With(zap.String("operation", "UpdateResources"))
	done := make(chan struct{})

	go func() {
		allResources := make([]schema.Resource, 0)
		initErr := <-s.Open()
		if initErr != nil {
			logger.Error("Session initialization failed", zap.Error(initErr))
			return
		}
		logger.Debug("Session initialized, proceeding to fetch resources")

		for msg := range s.SendRequestSync("resources/list", &schema.ListResourcesRequestParams{}) {
			if msg == nil {
				logger.Error("resources/list - Received nil message")
				continue
			}
			if msg.Error != nil {
				logger.Error("resources/list - Failed to send initial resources list request", zap.Error(msg.Error))
				continue
			}
			if msg.Result == nil {
				logger.Error("resources/list - Resources list result is nil")
				continue
			}
			var listResourcesResult schema.ListResourcesResult
			if err := json.Unmarshal(*msg.Result, &listResourcesResult); err != nil {
				logger.Error("resources/list - Failed to unmarshal resources list result", zap.Error(err))
				continue
			}
			if len(listResourcesResult.Resources) > 0 {
				allResources = append(allResources, listResourcesResult.Resources...)
				logger.Debug("resources/list - Appended resources", zap.Int("count", len(listResourcesResult.Resources)))
			}
		}
		s.Locker.Lock()
		s.resources = allResources
		s.resourcesInitialized = true
		s.Locker.Unlock()
		close(done)
	}()

	return done
}

// GetResourcesResult contains the result of a resources list request (using 2025 schema).
type GetResourcesResult struct {
	Resources []schema.Resource // Use 2025 schema type
	Err       error
}

// GetResources retrieves all available resources from the server.
// It returns a channel that will emit the 2025 schema resources list result.
func (s *Session) GetResources(ctx context.Context) chan GetResourcesResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "GetResources"))
	resultChan := make(chan GetResourcesResult, 1) // Buffered channel

	go func() {
		defer close(resultChan) // Ensure channel is closed

		s.Locker.RLock()
		initialized := s.resourcesInitialized
		s.Locker.RUnlock()

		if !initialized {
			logger.Debug("Resources not initialized, updating...")
			select {
			case <-s.UpdateResources(ctx):
				logger.Debug("Resources updated")
				// Re-check initialization status after update attempt
				s.Locker.RLock()
				initialized = s.resourcesInitialized
				s.Locker.RUnlock()
				if !initialized {
					logger.Error("Resources still not initialized after update attempt")
					resultChan <- GetResourcesResult{nil, errors.New("failed to initialize resources")}
					return
				}
			case <-ctx.Done():
				logger.Error("Context cancelled while waiting for resources update", zap.Error(ctx.Err()))
				resultChan <- GetResourcesResult{nil, fmt.Errorf("context cancelled: %w", ctx.Err())}
				return
			}
		}

		s.Locker.RLock()
		// Create a copy using 2025 schema type
		resourcesCopy := make([]schema.Resource, len(s.resources))
		copy(resourcesCopy, s.resources) // copy works fine
		s.Locker.RUnlock()

		logger.Debug("Returning resources list", zap.Int("count", len(resourcesCopy)))
		resultChan <- GetResourcesResult{resourcesCopy, nil}
	}()

	return resultChan
}

// ReadResourceResult contains the result of a resource read request (using 2025 schema).
type ReadResourceResult struct {
	Result *schema.ReadResourceResult // Use 2025 schema type
	Err    error
}

// ReadResource reads the content of a specific resource by its URI.
// It returns a channel that will emit the 2025 schema resource read result.
func (s *Session) ReadResource(ctx context.Context, uri string) chan ReadResourceResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "ReadResource"), zap.String("uri", uri))
	resultChan := make(chan ReadResourceResult, 1) // Buffered channel

	if uri == "" {
		err := errors.New("resource URI cannot be empty")
		logger.Error("Invalid request", zap.Error(err))
		resultChan <- ReadResourceResult{nil, err}
		close(resultChan)
		return resultChan
	}

	go func() {
		// Use 2025 schema request parameters
		params := &schema.ReadResourceRequestParams{
			URI: uri,
		}

		// Define callback for the response
		callback := func(msg *shared.Message) {
			defer close(resultChan) // Ensure channel is closed
			responseLogger := s.BaseSession.Logger.With(zap.String("operation", "readResourceCallback"), zap.String("uri", uri))
			if msg == nil {
				responseLogger.Error("Received nil message")
				resultChan <- ReadResourceResult{nil, errors.New("protocol error: received nil response")}
				return
			}

			if msg.Error != nil {
				responseLogger.Warn("Backend returned error", zap.Error(msg.Error))
				resultChan <- ReadResourceResult{nil, fmt.Errorf("backend error: %w", msg.Error)}
				return
			}

			if msg.Result == nil {
				responseLogger.Error("Resource read result is nil")
				resultChan <- ReadResourceResult{nil, errors.New("protocol error: resource read result is nil")}
				return
			}

			// Use 2025 schema result type
			var readResourceResult schema.ReadResourceResult
			if err := json.Unmarshal(*msg.Result, &readResourceResult); err != nil {
				responseLogger.Error("Failed to unmarshal resource read result", zap.Error(err))
				resultChan <- ReadResourceResult{nil, fmt.Errorf("failed to parse backend response: %w", err)}
				return
			}
			msg.Processed = true
			responseLogger.Debug("Successfully read resource")
			resultChan <- ReadResourceResult{&readResourceResult, nil}
		}

		// Send the request
		logger.Debug("Sending resources/read request")
		// Change: Ignore the first return value (requestID)
		_, err := s.SendRequest("resources/read", params, callback)
		if err != nil {
			logger.Error("Failed to send resource read request", zap.Error(err))
			// Try to send error through channel
			select {
			case resultChan <- ReadResourceResult{nil, fmt.Errorf("failed to send request: %w", err)}:
			default:
				logger.Error("Result channel closed before error could be sent", zap.Error(err))
			}
		}
	}()
	return resultChan
}
