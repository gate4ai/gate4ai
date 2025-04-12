package schema

import (
	"encoding/json"
	"fmt"
)

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

// TextPart represents a textual part of a message or artifact.
type TextPart struct {
	// Type identifier, always "text".
	Type string `json:"type"` // const: "text"
	// The actual text content.
	Text string `json:"text"`
	// Optional metadata specific to this part.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// FilePart represents a file part of a message or artifact.
type FilePart struct {
	// Type identifier, always "file".
	Type string `json:"type"` // const: "file"
	// The file content details (bytes or URI).
	File FileContent `json:"file"`
	// Optional metadata specific to this part.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// DataPart represents a structured data part of a message or artifact (e.g., for forms).
type DataPart struct {
	// Type identifier, always "data".
	Type string `json:"type"` // const: "data"
	// The structured data object.
	Data map[string]interface{} `json:"data"`
	// Optional metadata specific to this part.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// Part represents a piece of content within a Message or Artifact.
// It's a union type; use the 'Type' field to determine the actual structure (TextPart, FilePart, or DataPart).
// In Go, this is typically handled using json.RawMessage or by attempting to unmarshal into specific types.
type Part json.RawMessage

// Message represents a unit of communication between a user/client and an agent.
type Message struct {
	// Role of the sender ("user" or "agent").
	Role string `json:"role"` // enum: "user", "agent"
	// The content parts of the message. Each part should be unmarshalled based on its 'type' field.
	Parts []Part `json:"parts"`
	// Optional metadata associated with the entire message.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// Helper functions to determine Part type (example implementation)

func GetPartType(p Part) (string, error) {
	var typeFinder struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(p, &typeFinder); err != nil {
		return "", fmt.Errorf("failed to determine part type: %w", err)
	}
	return typeFinder.Type, nil
}

func AsTextPart(p Part) (*TextPart, error) {
	var tp TextPart
	if err := json.Unmarshal(p, &tp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal as TextPart: %w", err)
	}
	if tp.Type != "text" {
		return nil, fmt.Errorf("part is not of type 'text'")
	}
	return &tp, nil
}

func AsFilePart(p Part) (*FilePart, error) {
	var fp FilePart
	if err := json.Unmarshal(p, &fp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal as FilePart: %w", err)
	}
	if fp.Type != "file" {
		return nil, fmt.Errorf("part is not of type 'file'")
	}
	return &fp, nil
}

func AsDataPart(p Part) (*DataPart, error) {
	var dp DataPart
	if err := json.Unmarshal(p, &dp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal as DataPart: %w", err)
	}
	if dp.Type != "data" {
		return nil, fmt.Errorf("part is not of type 'data'")
	}
	return &dp, nil
}
