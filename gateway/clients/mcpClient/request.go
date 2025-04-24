package mcpClient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gate4ai/gate4ai/shared"
	"go.uber.org/zap"
)

func (s *Session) executeSendRequest(msg *shared.Message) {
	logger := s.BaseSession.Logger.With(
		zap.Stringp("method", msg.Method),
		zap.String("reqID", msg.ID.String()),
	)

	s.Locker.RLock()
	endpoint := s.postEndpoint
	httpClient := s.httpClient
	currentHeaders := s.GetCurrentHeaders()
	s.Locker.RUnlock()

	notifyError := func(err error) {
		if msg.ID != nil && !msg.ID.IsEmpty() {
			s.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(err), Session: s})
		}
	}

	if endpoint == "" {
		err := errors.New("post endpoint not initialized")
		logger.Error(err.Error())
		notifyError(err)
		return
	}
	if httpClient == nil {
		err := errors.New("http client not initialized")
		logger.Error(err.Error())
		notifyError(err)
		return
	}

	reqJSON, err := json.Marshal(msg)
	if err != nil {
		logger.Error("Failed to marshal JSON-RPC request", zap.Error(err))
		notifyErr := fmt.Errorf("internal marshal error for '%s': %w", shared.NilIfNil(msg.Method), err)
		notifyError(notifyErr)
		return
	}

	httpReqCtx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpReqCtx, http.MethodPost, endpoint, bytes.NewBuffer(reqJSON))
	if err != nil {
		logger.Error("Failed to create HTTP request", zap.Error(err), zap.String("endpoint", endpoint))
		notifyErr := fmt.Errorf("failed to create HTTP request to %s: %w", endpoint, err)
		notifyError(notifyErr)
		return
	}

	// Set Headers - Use the headers stored in the session
	req.Header.Set("Content-Type", "application/json")
	for key, value := range currentHeaders {
		req.Header.Set(key, value) // Add all stored headers
	}

	logger.Debug("Sending HTTP POST request", zap.String("endpoint", endpoint), zap.Int("headerCount", len(currentHeaders)))

	startTime := time.Now()
	resp, err := httpClient.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		logger.Warn("HTTP POST request failed", zap.Error(err), zap.Duration("duration", duration))
		notifyErr := fmt.Errorf("http request to %s failed: %w", endpoint, err)
		notifyError(notifyErr)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 500)) // Read up to 500 bytes
		logger.Error("HTTP POST request returned non-success status",
			zap.Int("status", resp.StatusCode),
			zap.String("endpoint", endpoint),
			zap.Duration("duration", duration),
			zap.String("body", string(bodyBytes)), // Log partial body
		)
		notifyErr := fmt.Errorf("post to %s failed status %d: %s", endpoint, resp.StatusCode, string(bodyBytes))
		notifyError(notifyErr)
		return
	}

	logger.Debug("HTTP POST request successful", zap.Int("status", resp.StatusCode), zap.Duration("duration", duration))
}
