package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/server/a2a"
	"github.com/gate4ai/mcp/shared"
	"go.uber.org/zap"
)

// streamA2AResponse handles the SSE stream specifically for A2A `tasks/sendSubscribe` requests.
func (t *Transport) streamA2AResponse(w http.ResponseWriter, r *http.Request, session shared.ISession, logger *zap.Logger) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Error("Streaming unsupported for A2A SSE", zap.String("sessionId", session.GetID()))
		t.sessionManager.CloseSession(session.GetID()) // Clean up session
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// --- Get A2ACapability ---
	// This assumes A2ACapability is registered with the session's input processor.
	// We need a way to access it or its state (like running handlers) more directly.
	// For now, let's assume the initial `tasks/sendSubscribe` handler in A2ACapability
	// already started the agentHandler and we just need to forward events from the session output.

	a2aCapability := findA2ACapability(session.Input())
	if a2aCapability == nil {
		logger.Error("A2ACapability not found for session, cannot stream", zap.String("sessionID", session.GetID()))
		http.Error(w, "Internal server error: A2A capability unavailable", http.StatusInternalServerError)
		t.sessionManager.CloseSession(session.GetID())
		return
	}

	// --- Prepare SSE Headers ---
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*") // Consider restricting this
	// Optionally set Mcp-Session-Id header if applicable/useful for A2A?
	// w.Header().Set(MCP_SESSION_HEADER, session.GetID())
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // Send headers immediately

	logger.Info("A2A SSE stream initiated", zap.String("sessionId", session.GetID()))

	// --- Event Loop ---
	ticker := time.NewTicker(15 * time.Second) // Keepalive ticker
	defer ticker.Stop()
	defer logger.Debug("Exiting A2A SSE stream goroutine", zap.String("sessionId", session.GetID()))

	ctx := r.Context() // Use request context for cancellation

	output, ok := session.AcquireOutput()
	if !ok {
		logger.Error("Failed to acquire output channel for A2A SSE", zap.String("sessionId", session.GetID()))
		// Don't write HTTP error here, stream is already started
		return
	}
	defer session.ReleaseOutput()

	eventID := time.Now().UnixNano() // Initial event ID

	for {
		select {
		case <-ctx.Done(): // Client disconnected
			logger.Info("A2A SSE client disconnected (context done)", zap.String("sessionId", session.GetID()))
			// Attempt to cancel the underlying task handler if it's still running
			// Need the task ID associated with this stream! How to get it?
			// Maybe store task ID in session Params? Or associate stream context with taskID?
			// For now, we can't reliably cancel the specific handler here.
			return

		case msg, ok := <-output:
			if !ok {
				logger.Info("Session output channel closed, closing A2A SSE stream", zap.String("sessionId", session.GetID()))
				return // Exit loop
			}
			if msg == nil {
				logger.Error("Received nil message from session output channel (A2A)", zap.String("sessionId", session.GetID()))
				continue
			}

			eventDataJSON, err := json.Marshal(msg)
			if err != nil {
				logger.Error("Failed to marshal A2A SSE event data", zap.Error(err))
				continue
			}

			// Send event
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", eventID, "event", eventDataJSON)
			eventID++
			flusher.Flush()
			logger.Debug("Sent A2A SSE event", zap.Any("eventName", msg))

			var a2aEvent shared.A2AStreamEvent
			err = json.Unmarshal(*msg.Result, &a2aEvent)
			// If this was the final event, close the stream
			if err == nil && a2aEvent.Final {
				logger.Info("Final A2A event sent, closing SSE stream", zap.String("sessionId", session.GetID()))
				return // Exit loop, which closes the stream
			}
		case <-ticker.C:
			// Send keepalive ping event
			select {
			case <-ctx.Done():
				return // Exit if client disconnected during tick
			default:
				// A2A doesn't specify ping events, but it's good practice for SSE
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	}
}

// findA2ACapability searches the input processor's capabilities for the A2ACapability.
// This is a helper and might need adjustment based on how capabilities are stored/accessed.
func findA2ACapability(input *shared.Input) *a2a.A2ACapability {
	if input == nil {
		return nil
	}
	// Accessing internal capabilities list - maybe Input needs a getter?
	// This is a simplification for the example.
	input.Mu.RLock()
	defer input.Mu.RUnlock()
	// This relies on direct access to the internal 'capabilities' field, which isn't ideal.
	// A better approach would be for Input to provide a way to get capabilities by type.
	// For now, we iterate assuming we can access it (this might break if Input changes).
	// This part needs refinement in a real implementation. Accessing private fields isn't good.
	// Let's assume Input gets a method like GetCapabilityByType() in the future.
	// For now, we'll simulate it with a type assertion loop (knowing it's not robust).
	/*
	   type capabilitiesAccessor interface {
	       GetCapabilities() []shared.ICapability // Assume Input has this method
	   }
	   accessor, ok := input.(capabilitiesAccessor)
	   if !ok { return nil } // Cannot access capabilities
	   for _, cap := range accessor.GetCapabilities() {
	       if a2aCap, ok := cap.(*capability.A2ACapability); ok {
	           return a2aCap
	       }
	   }
	*/
	// Placeholder: Since we can't access the private field, return nil.
	// The A2ACapability instance should ideally be passed to the transport or accessed via the manager.
	// Let's assume the transport holds a reference or gets it from the manager when needed.
	// This function becomes unnecessary if the capability is passed directly.
	fmt.Println("Warning: findA2ACapability is a placeholder due to private field access limitation.")
	return nil // Placeholder return
}
