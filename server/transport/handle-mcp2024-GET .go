package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/shared"
	"go.uber.org/zap"
)

const (
	sseEventEndpoint = "endpoint"
	sseEventMessage  = "message"
	sseEventPing     = "ping"
)

// It handles V2024 initialization via SSE endpoint event and
// V2024 persistent SSE stream opening on GET request.
func (t *Transport) handle2024GET(w http.ResponseWriter, r *http.Request, logger *zap.Logger) {
	logger = logger.With(zap.String("method", "handle2024GET"))
	session, err := t.getSession(w, r, logger, true)
	if err != nil {
		logger.Error("Failed to get session", zap.Error(err))
		return
	}

	output, ok := session.AcquireOutput()
	if !ok {
		logger.Error("Failed to acquire output channel for V2024 SSE stream", zap.String("sessionId", session.GetID()))
		http.Error(w, "Failed to acquire output channel", statusInternalServerError)
		return
	}
	defer session.ReleaseOutput()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	endpointPath := MCP2024_PATH + "?" + SESSION_ID_KEY2024 + "=" + session.GetID()

	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Error("Streaming unsupported for SSE", zap.String("sessionId", session.GetID()))
		t.sessionManager.CloseSession(session.GetID()) // Clean up session
		http.Error(w, "Streaming unsupported", statusInternalServerError)
		return
	}

	// Send the mandatory 'endpoint' event for V2024
	fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", "endpoint-event-id", sseEventEndpoint, endpointPath)
	flusher.Flush()
	logger.Debug("Sent V2024 endpoint event", zap.String("sessionId", session.GetID()), zap.String("endpoint", endpointPath))

	session.SetStatus(shared.StatusConnected)
	logger.Info("Session status set to Connected", zap.String("sessionId", session.GetID()))

	ticker := time.NewTicker(15 * time.Second) // Keepalive ticker
	defer ticker.Stop()
	defer logger.Debug("Stopped forwarding session output to V2024 SSE stream", zap.String("sessionId", session.GetID()))

	go func() {
		for {
			select {
			case <-r.Context().Done():
				logger.Info("V2024 SSE client disconnected (context done)", zap.String("sessionId", session.GetID()))
				t.sessionManager.CloseSession(session.GetID())
				return
			case msg, ok := <-output:
				if !ok {
					logger.Info("Session output channel closed", zap.String("sessionId", session.GetID()))
					return
				}
				if msg == nil {
					continue
				}

				data, err := json.Marshal(msg)
				if err != nil {
					logger.Error("Failed to marshal message for SSE", zap.Error(err), zap.Any("msgId", msg.ID), zap.Stringp("method", msg.Method))
					continue // Skip message if marshalling fails
				}

				// Send as 'message' event
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", time.Now().UnixNano(), sseEventMessage, data)
				flusher.Flush()
				session.UpdateLastActivity()
			case <-ticker.C:
				// Send keepalive ping event
				// Double-check context before sending keepalive to avoid race condition on disconnect
				select {
				case <-r.Context().Done():
					// Context was canceled, exit the loop silently
					return
				default:
					// Context still active, send keepalive ping event
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sseEventPing, `{}`) // V2024 might not use ID for pings
					flusher.Flush()
				}
			}
		}
	}()

	// Keep the handler alive while the goroutine runs.
	// The client disconnecting will cancel the request context.
	<-r.Context().Done()
}
