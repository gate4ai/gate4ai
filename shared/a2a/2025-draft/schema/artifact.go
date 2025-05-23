package schema

// Artifact represents an output generated by a task, such as a file, text snippet, or structured data.
type Artifact struct {
	// Optional name for the artifact (e.g., filename).
	Name *string `json:"name,omitempty"`
	// Optional description of the artifact.
	Description *string `json:"description,omitempty"`
	// The content parts constituting the artifact. (Required, must have at least one part)
	Parts []Part `json:"parts"`
	// Zero-based index indicating the order or identity of the artifact, useful for streaming updates.
	Index int `json:"index"` // Not omitempty - 0 is a valid index
	// For streaming: if true, the content parts should be appended to the artifact at the same index. (Optional)
	Append *bool `json:"append,omitempty"`
	// For streaming: if true, this is the final chunk of data for this artifact index. (Optional)
	LastChunk *bool `json:"lastChunk,omitempty"`
	// Optional metadata associated with the artifact.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}
