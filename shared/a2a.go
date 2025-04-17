package shared

import a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema"

// A2AStreamEvent holds data for A2A SSE events, used internally by transport
type A2AStreamEvent struct {
	// Type indicates whether this event is a status update or an artifact update.
	Type string // "status" or "artifact"
	// Status contains the status update event data if Type is "status".
	Status *a2aSchema.TaskStatusUpdateEvent `json:"status,omitempty"`
	// Artifact contains the artifact update event data if Type is "artifact".
	Artifact *a2aSchema.TaskArtifactUpdateEvent `json:"artifact,omitempty"`
	// Final indicates if this is the last event for the stream (usually set on the final status update).
	Final bool `json:"final,omitempty"`
	// Error holds any error encountered while processing the stream (e.g., parsing error, connection closed).
	Error error `json:"-"` // Use Go error type for internal handling
}
