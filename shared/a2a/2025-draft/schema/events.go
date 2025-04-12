package schema

// TaskStatusUpdateEvent signals a change in the task's status during streaming.
type TaskStatusUpdateEvent struct {
	// The ID of the task being updated.
	ID string `json:"id"`
	// The new status of the task.
	Status TaskStatus `json:"status"`
	// If true, this is the terminal status update for the task. Defaults to false.
	Final bool `json:"final,omitempty"`
	// Optional metadata associated with the event.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// TaskArtifactUpdateEvent signals a new or updated artifact during streaming.
type TaskArtifactUpdateEvent struct {
	// The ID of the task associated with the artifact.
	ID string `json:"id"`
	// The artifact data.
	Artifact Artifact `json:"artifact"`
	// Optional metadata associated with the event.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}
