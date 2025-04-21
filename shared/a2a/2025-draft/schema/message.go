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

// Part represents a piece of content within a Message or Artifact.
// Uses pointers for value fields to distinguish between empty/zero and omitted.
type Part struct {
	// The type of the part ("text", "file", or "data"). Strongly recommended.
	Type *string `json:"type,omitempty"`
	// Text content, only if Type is "text".
	Text *string `json:"text,omitempty"`
	// File content, only if Type is "file".
	File *FileContent `json:"file,omitempty"`
	// Structured data content, only if Type is "data".
	Data *map[string]interface{} `json:"data,omitempty"`
	// Optional metadata specific to this part.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// Message represents a unit of communication between a user/client and an agent.
type Message struct {
	// Role of the sender ("user" or "agent"). (Required)
	Role string `json:"role"` // enum: "user", "agent"
	// The content parts of the message. (Required, must have at least one part)
	Parts []Part `json:"parts"`
	// Optional metadata associated with the entire message.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}
