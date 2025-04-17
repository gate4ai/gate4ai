package mcpClient

import (
	"context"
	"fmt" // Import fmt for error wrapping

	// Use V2025 schema
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// GetServerInfoResult holds the result of retrieving server information (using 2025 schema).
type GetServerInfoResult struct {
	ServerInfo *schema.Implementation // Use V2025 schema type
	Err        error
}

// GetServerInfo returns the server information obtained during the initialization handshake.
// It waits for the session to be initialized if it hasn't been already.
// Returns a channel that will emit the 2025 schema server info result.
func (s *Session) GetServerInfo(ctx context.Context) chan GetServerInfoResult {
	logger := s.BaseSession.Logger.With(zap.String("operation", "GetServerInfo"))
	resultChan := make(chan GetServerInfoResult, 1) // Buffered channel

	go func() {
		defer close(resultChan) // Ensure channel is closed

		logger.Debug("Waiting for session initialization...")
		// Wait for the session initialization process to complete or fail
		select {
		case initErr := <-s.Open(): // Use Open() to get the initialization result channel
			if initErr != nil {
				logger.Error("Session initialization failed", zap.Error(initErr))
				resultChan <- GetServerInfoResult{nil, fmt.Errorf("session initialization failed: %w", initErr)}
				return
			}
			logger.Debug("Session initialized successfully")
			// Proceed to get server info
		case <-ctx.Done():
			logger.Warn("Context cancelled while waiting for session initialization", zap.Error(ctx.Err()))
			resultChan <- GetServerInfoResult{nil, fmt.Errorf("context cancelled: %w", ctx.Err())}
			return
		}

		// Get server info safely
		s.Locker.RLock()
		serverInfoCopy := s.serverInfo // Get potentially nil pointer
		s.Locker.RUnlock()

		if serverInfoCopy == nil {
			// This case should theoretically not happen if initialization succeeded without error,
			// but handle it defensively.
			logger.Error("Server info is nil even after successful initialization")
			resultChan <- GetServerInfoResult{nil, fmt.Errorf("internal error: server info not set after initialization")}
			return
		}

		// Return a copy of the server info (Implementation is simple, direct copy is fine)
		info := *serverInfoCopy
		logger.Debug("Returning server info", zap.Any("serverInfo", info))
		resultChan <- GetServerInfoResult{&info, nil}
	}()

	return resultChan
}
