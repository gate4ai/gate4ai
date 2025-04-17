package schema

// FileContent represents file data, either as inline bytes or a URI reference.
type FileContent struct {
	// Optional filename.
	Name *string `json:"name,omitempty"`
	// Optional MIME type of the file content.
	MimeType *string `json:"mimeType,omitempty"`
	// Base64 encoded file content. Mutually exclusive with URI.
	Bytes *string `json:"bytes,omitempty"`
	// URI pointing to the file content. Mutually exclusive with Bytes.
	URI *string `json:"uri,omitempty"`
}

// FilePart represents a file part of a message or artifact.
type Part struct {
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
	Type     *string                 `json:"type"` // enum: "text", "file", "data" . but sometimes they don't send it
	Text     *string                 `json:"text,omitempty"`
	File     *FileContent            `json:"file,omitempty"`
	Data     *map[string]interface{} `json:"data,omitempty"`
}

// Message represents a unit of communication between a user/client and an agent.
type Message struct {
	// Role of the sender ("user" or "agent").
	Role string `json:"role"` // enum: "user", "agent"
	// The content parts of the message. Each part should be unmarshalled based on its 'type' field.
	Parts []Part `json:"parts"`
	// Optional metadata associated with the entire message.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}
