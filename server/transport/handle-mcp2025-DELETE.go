package transport

import (
	"net/http"

	"go.uber.org/zap"
)

// handleDELETE processes DELETE requests (for V2 session termination).
func (t *Transport) handleDELETE(w http.ResponseWriter, r *http.Request, logger *zap.Logger) {
	sessionIDHeader := r.Header.Get(MCP_SESSION_HEADER)

	if sessionIDHeader == "" {
		logger.Warn("Missing Mcp-Session-Id header for DELETE request")
		http.Error(w, "Bad Request: Mcp-Session-Id header required", statusBadRequest)
		return
	}

	// Attempt to find the session
	_, err := t.sessionManager.GetSession(sessionIDHeader)
	if err != nil {
		logger.Warn("Session not found for DELETE request", zap.String("sessionId", sessionIDHeader), zap.Error(err))
		http.Error(w, "Not Found: Session expired or invalid", statusNotFound)
		return
	}

	// Close the session
	logger.Info("Received DELETE request, closing session", zap.String("sessionId", sessionIDHeader))
	t.sessionManager.CloseSession(sessionIDHeader)

	// Respond with 200 OK or 204 No Content
	w.WriteHeader(http.StatusNoContent)
}
