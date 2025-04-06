package schema

// ResourceTemplate describes a template for available resources.
type ResourceTemplate struct {
	Annotations *Annotations `json:"annotations,omitempty"`
	Description string       `json:"description,omitempty"` // Template description
	MimeType    string       `json:"mimeType,omitempty"`    // MIME type if all resources have same type
	Name        string       `json:"name"`                  // Human-readable name
	URITemplate string       `json:"uriTemplate"`           // URI template for resources
}

// ListResourceTemplatesResult is the response to a templates list request.
type ListResourceTemplatesResult struct {
	Meta              map[string]interface{} `json:"_meta,omitempty"`      // Reserved for metadata
	NextCursor        *Cursor                `json:"nextCursor,omitempty"` // Pagination token for next page
	ResourceTemplates []ResourceTemplate     `json:"resourceTemplates"`    // Available templates
}
