package transport

import (
	"io"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/shared"
	"go.uber.org/zap"
)

// handlePOST processes POST requests on the unified MCP endpoint.
// It handles V2024 message posting (via session_id query) and V2 message posting (via header).
func (t *Transport) handle2024POST(w http.ResponseWriter, r *http.Request, logger *zap.Logger) {
	session, err := t.getSession(w, r, logger, false)
	if err != nil {
		logger.Warn("Session not found for V2024 POST", zap.Error(err))
		// http.Error was already called by getSession if session not found
		return
	}

	// --- Process Message(s) ---
	// If we reach here, it's a V2024 style POST (session determined by query param)
	bodyBytes, bodyErr := io.ReadAll(r.Body)
	if bodyErr != nil {
		logger.Error("Failed to read request body for V2024 POST", zap.Error(bodyErr))
		// V2024 POST doesn't return error on POST, just logs it. Always return 202 Accepted.
		w.WriteHeader(statusAccepted)
		return
	}
	defer r.Body.Close()

	msgs, err := shared.ParseMessages(session, bodyBytes)
	if err != nil {
		logger.Error("Failed to parse JSON-RPC message(s) for V2024 POST", zap.Error(err), zap.ByteString("body", bodyBytes))
		// V2024 POST doesn't return error on POST, just logs it. Always return 202 Accepted.
		w.WriteHeader(statusAccepted)
		return
	}

	// Handle messages via session manager
	for _, msg := range msgs {
		msg.Session = session
		msg.Timestamp = time.Now()
		if handleErr := session.Input().Put(msg); handleErr != nil {
			logger.Error("Error handling message in V2024 POST", zap.Error(handleErr), zap.String("sessionId", session.GetID()), zap.Any("msgId", msg.ID))
			// V2024 POST doesn't have a standard way to return errors for individual messages here.
			// The original spec just sends back the 'endpoint' event. The V2024 server would send errors via SSE.
			// Since we're adapting, we just log the error.
		}
	}

	w.WriteHeader(statusAccepted)
	logger.Debug("POST processed, returning 202 Accepted", zap.String("sessionId", session.GetID()), zap.Int("messageCount", len(msgs)))
}
