package agent

import (
	"context"
	"time"

	"github.com/gate4ai/gate4ai/server/a2a" // Import the server's a2a package for A2AYieldUpdate
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
)

// --- Helper Functions for Agent Logic ---

// sendUpdate sends a status or artifact update via the channel, respecting context cancellation.
func sendUpdate(ctx context.Context, updates chan<- a2a.A2AYieldUpdate, update a2a.A2AYieldUpdate) error {
	select {
	case updates <- update:
		return nil
	case <-ctx.Done():
		return ctx.Err() // Context cancelled
	}
}

// sendStatusUpdate creates and sends a TaskStatus update.
// The 'final' flag indicates if this status represents the end of the *current processing step* for the agent.
// For terminal states (completed, failed, canceled), this will be true.
// For input-required, it's also true, as the agent stops processing until new input arrives.
// For working, it's typically false.
func sendStatusUpdate(ctx context.Context, updates chan<- a2a.A2AYieldUpdate, state a2aSchema.TaskState, messageText string) error {
	status := a2aSchema.TaskStatus{
		State:     state,
		Timestamp: time.Now(),
	}
	if messageText != "" {
		status.Message = &a2aSchema.Message{
			Role:  "agent",
			Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &messageText}},
		}
	}
	return sendUpdate(ctx, updates, a2a.A2AYieldUpdate{Status: &status})
}

// sendArtifactUpdate creates and sends an Artifact update.
func sendArtifactUpdate(ctx context.Context, updates chan<- a2a.A2AYieldUpdate, artifact a2aSchema.Artifact) error {
	// Ensure artifact.LastChunk is set appropriately by the creator function if needed.
	return sendUpdate(ctx, updates, a2a.A2AYieldUpdate{Artifact: &artifact})
}

// sendJsonRpcErrorUpdate creates and sends a JSONRPCError update.
func sendJsonRpcErrorUpdate(ctx context.Context, updates chan<- a2a.A2AYieldUpdate, code int, message string) error {
	jsonErr := &a2aSchema.JSONRPCError{
		Code:    code,
		Message: message,
	}
	return sendUpdate(ctx, updates, a2a.A2AYieldUpdate{Error: jsonErr})
}

// createTextArtifact creates a simple text artifact.
func createTextArtifact(name, content string) a2aSchema.Artifact {
	return a2aSchema.Artifact{
		Name:      shared.PointerTo(name),
		Parts:     []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &content}},
		Index:     0, // Caller should set correct index before sending
		LastChunk: shared.PointerTo(true),
	}
}

// createFileArtifact creates a file artifact.
func createFileArtifact(name, mimeType, base64Content string) a2aSchema.Artifact {
	return a2aSchema.Artifact{
		Name: shared.PointerTo(name),
		Parts: []a2aSchema.Part{{
			Type: shared.PointerTo("file"),
			File: &a2aSchema.FileContent{
				Name:     shared.PointerTo(name),
				MimeType: shared.PointerTo(mimeType),
				Bytes:    shared.PointerTo(base64Content),
			},
		}},
		Index:     0, // Caller should set correct index
		LastChunk: shared.PointerTo(true),
	}
}

// createDataArtifact creates a structured data artifact.
func createDataArtifact(data map[string]interface{}) a2aSchema.Artifact {
	return a2aSchema.Artifact{
		Name:      shared.PointerTo("structured_data.json"),
		Parts:     []a2aSchema.Part{{Type: shared.PointerTo("data"), Data: &data}},
		Index:     0, // Caller should set correct index
		LastChunk: shared.PointerTo(true),
	}
}
