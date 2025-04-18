package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gate4ai/gate4ai/server/a2a"
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"go.uber.org/zap"
)

// streamA2AResponse handles the SSE stream specifically for A2A `tasks/sendSubscribe` and `tasks/resubscribe`.
// It assumes the initial HTTP headers have already been sent by the caller.
// It reads events from the session's output channel and forwards them as SSE events.
func (t *Transport) streamA2AResponse(w http.ResponseWriter, r *http.Request, session shared.ISession, logger *zap.Logger) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Error("Streaming unsupported (http.Flusher missing) for A2A SSE", zap.String("sessionID", session.GetID()))
		t.sessionManager.CloseSession(session.GetID()) // Clean up session
		// Cannot send HTTP error here as headers might be sent already.
		return
	}

	logger.Info("A2A SSE stream initiated", zap.String("sessionId", session.GetID()))

	// --- Process Stream Events ---
	ticker := time.NewTicker(15 * time.Second) // Keepalive ticker
	defer ticker.Stop()
	defer logger.Debug("Exiting A2A SSE stream goroutine", zap.String("sessionId", session.GetID()))
	defer t.sessionManager.CloseSession(session.GetID()) // Clean up session on disconnect
	ctx := r.Context()                                   // Use request context for cancellation

	output, ok := session.AcquireOutput()
	if !ok {
		logger.Error("Failed to acquire session output channel", zap.String("sessionId", session.GetID()))
		return
	}
	defer session.ReleaseOutput() // Ensure output is released

	// Start event ID counter
	eventID := 1

	// Process events until client disconnect or task completion
	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			logger.Info("Client disconnected from A2A SSE stream", zap.String("sessionId", session.GetID()))
			return
		case <-ticker.C:
			// Send comment as keepalive
			fmt.Fprintf(w, ": keepalive %s\n\n", time.Now().Format(time.RFC3339))
			flusher.Flush()
			logger.Debug("Sent A2A SSE keepalive")
		case msg, ok := <-output:
			if !ok {
				// Channel closed - session ended
				logger.Info("A2A SSE output channel closed, ending stream", zap.String("sessionId", session.GetID()))
				return
			}

			// Skip messages without ID or Result (not sure if this can happen in A2A)
			// Actually, A2A SSE events have nil ID by design.
			if msg.Error != nil && msg.Result == nil {
				logger.Warn("Received error message on A2A SSE stream", zap.Any("error", msg.Error))
				continue
			}

			// The message in the output channel *should* already contain the A2AStreamEvent
			// marshalled into its Result field by session.SendA2AStreamEvent.
			if msg.Result == nil {
				logger.Warn("Received message on A2A SSE stream with nil result", zap.Any("msgID", msg.ID), zap.String("method", *msg.Method))
				continue
			}

			// Send event
			// Use standard SSE format: id, event (optional, A2A doesn't specify), data
			// The data payload is the JSON representation of the event (TaskStatusUpdateEvent or TaskArtifactUpdateEvent).
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", eventID, string(*msg.Result))
			eventID++
			flusher.Flush()
			logger.Debug("Sent A2A SSE event", zap.String("eventData", string(*msg.Result)))

			// Check if the event payload indicates it's the final one.
			// Unmarshal the Result into TaskStatusUpdateEvent to check its Final flag.
			var statusEvent a2aSchema.TaskStatusUpdateEvent
			// We only care about the 'final' flag on status updates.
			isFinal := false
			if err := json.Unmarshal(*msg.Result, &statusEvent); err == nil {
				isFinal = statusEvent.Final
			}

			if isFinal {
				logger.Info("Final A2A event sent, closing SSE stream", zap.String("sessionId", session.GetID()))
				return // Exit loop, which closes the stream
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
