package mcpClient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gate4ai/gate4ai/shared"

	"go.uber.org/zap"
)

func (s *Session) executeSendRequest(msg *shared.Message) {
	// Use a logger derived from the base session logger, adding request context
	logger := s.BaseSession.Logger.With(
		zap.Stringp("method", msg.Method),
		zap.String("reqID", msg.ID.String()),
	)

	s.Locker.RLock()
	endpoint := s.postEndpoint
	httpClient := s.httpClient
	s.Locker.RUnlock()

	// Prepare error message for RequestManager in case of failure
	notifyError := func(err error) {
		if msg.ID != nil && !msg.ID.IsEmpty() { // Only notify for requests, not notifications
			// Simulate an error response to trigger cleanup in RequestManager
			s.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(err), Session: s})
		}
	}

	if endpoint == "" {
		err := errors.New("post endpoint not initialized, cannot send request")
		logger.Error(err.Error())
		notifyError(err)
		return
	}
	if httpClient == nil {
		err := errors.New("http client not initialized, cannot send request")
		logger.Error(err.Error())
		notifyError(err)
		return
	}

	reqJSON, err := json.Marshal(msg)
	if err != nil {
		// Log the specific error during marshalling
		logger.Error("Failed to marshal JSON-RPC request", zap.Error(err))
		// Create a more informative error message
		notifyErr := fmt.Errorf("internal error: failed to marshal JSON-RPC request for method '%s': %w", shared.NilIfNil(msg.Method), err)
		notifyError(notifyErr)
		return
	}

	// Create HTTP request with a timeout context
	// Use s.ctx as the base context for cancellation propagation
	httpReqCtx, cancel := context.WithTimeout(s.ctx, 30*time.Second) // 30-second timeout for the POST request itself
	defer cancel()

	req, err := http.NewRequestWithContext(httpReqCtx, http.MethodPost, endpoint, bytes.NewBuffer(reqJSON))
	if err != nil {
		// Log details about the failed request creation
		logger.Error("Failed to create HTTP request",
			zap.Error(err),
			zap.String("endpoint", endpoint),
		)
		notifyErr := fmt.Errorf("failed to create HTTP request to %s: %w", endpoint, err)
		notifyError(notifyErr)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	// Add Authorization header if present in the SSE client config
	s.Locker.RLock()
	authHeader := s.sseClient.Headers["Authorization"]
	s.Locker.RUnlock()
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	logger.Debug("Sending HTTP POST request", zap.String("endpoint", endpoint))

	// Execute the HTTP request
	startTime := time.Now()
	resp, err := httpClient.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		// Log detailed error including duration
		logger.Warn("HTTP POST request failed",
			zap.Error(err),
			zap.String("endpoint", endpoint),
			zap.Duration("duration", duration),
		)
		// Pass the HTTP error to the RequestManager callback
		notifyErr := fmt.Errorf("http request to %s failed: %w", endpoint, err)
		notifyError(notifyErr)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Log the unexpected status code
		logger.Error("HTTP POST request returned non-success status",
			zap.Int("status", resp.StatusCode),
			zap.String("endpoint", endpoint),
			zap.Duration("duration", duration),
		)
		notifyErr := fmt.Errorf("http request to %s failed with status %d", endpoint, resp.StatusCode)
		notifyError(notifyErr)
		return
	}

	logger.Debug("HTTP POST request acknowledged successfully",
		zap.Int("status", resp.StatusCode),
		zap.Duration("duration", duration),
	)
}
