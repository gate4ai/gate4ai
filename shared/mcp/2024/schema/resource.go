package schema

// TextResourceContents contains text-based resource content.
type ResourceContent struct {
	URI      string  `json:"uri"`                // Resource URI
	MimeType string  `json:"mimeType,omitempty"` // MIME type if known
	Text     *string `json:"text"`               // Resource text content
	Blob     *string `json:"blob"`               // A base64-encoded string representing the binary data of the item
}

type Content struct {
	Type        string           `json:"type"`
	Annotations *Annotations     `json:"annotations,omitempty"`
	Text        *string          `json:"text"`     // The text content
	Data        *string          `json:"data"`     // Base64-encoded image data
	MimeType    *string          `json:"mimeType"` // MIME type of the image
	Resource    *ResourceContent `json:"resource"` // Can be TextResourceContents or BlobResourceContents
}

func NewTextContent(text string) []Content {
	return []Content{
		{
			Type: "text",
			Text: &text,
		},
	}
}

// ListResourcesResult is the response to a resources list request.
type ListResourcesResult struct {
	Meta       map[string]interface{} `json:"_meta,omitempty"`      // Reserved for metadata
	NextCursor *Cursor                `json:"nextCursor,omitempty"` // Pagination token for next page
	Resources  []Resource             `json:"resources"`            // Available resources
}

// ReadResourceRequest requests the content of a resource.
type ReadResourceRequest struct {
	Method string                    `json:"method"` // const: "resources/read"
	Params ReadResourceRequestParams `json:"params"`
}

// ReadResourceRequestParams contains parameters for resource reading.
type ReadResourceRequestParams struct {
	URI string `json:"uri"` // The URI of the resource to read
}

// ReadResourceResult contains the content of a requested resource.
type ReadResourceResult struct {
	Meta     map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Contents []ResourceContent      `json:"contents"`        // Can be TextResourceContents or BlobResourceContents
}

// Resource describes a resource the server can read.
type Resource struct {
	Annotations *Annotations `json:"annotations,omitempty"`
	Description string       `json:"description,omitempty"` // Resource description
	MimeType    string       `json:"mimeType,omitempty"`    // MIME type if known
	Name        string       `json:"name"`                  // Human-readable name
	Size        int          `json:"size,omitempty"`        // Size in bytes if known
	URI         string       `json:"uri"`                   // Resource URI
}

// ResourceListChangedNotification informs that available resources have changed.
type ResourceListChangedNotification struct {
	Method string                 `json:"method"` // const: "notifications/resources/list_changed"
	Params map[string]interface{} `json:"params,omitempty"`
}

// ResourceReference identifies a resource or template.
type ResourceReference struct {
	Type string `json:"type"` // const: "ref/resource"
	URI  string `json:"uri"`  // Resource URI or template
}
