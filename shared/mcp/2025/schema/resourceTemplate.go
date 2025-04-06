package schema

// ResourceTemplate describes a template description for resources available on the server.
type ResourceTemplate struct {
	Annotations *Annotations `json:"annotations,omitempty"` // Optional annotations for the client
	// A URI template (according to RFC 6570) that can be used to construct resource URIs.
	URITemplate string `json:"uriTemplate"` // @format uri-template
	// A human-readable name for the type of resource this template refers to.
	Name string `json:"name"`
	// A description of what this template is for.
	Description string `json:"description,omitempty"`
	// The MIME type for all resources that match this template.
	MimeType string `json:"mimeType,omitempty"`
}

// ListResourceTemplatesRequest requests a list of resource templates.
// Sent from the client to request a list of resource templates the server has.
type ListResourceTemplatesRequest struct {
	Method string                             `json:"method"` // const: "resources/templates/list"
	Params ListResourceTemplatesRequestParams `json:"params,omitempty"`
}

// ListResourceTemplatesRequestParams contains parameters for listing resource templates.
type ListResourceTemplatesRequestParams struct {
	PaginatedRequestParams // Embeds pagination cursor
}

// ListResourceTemplatesResult is the response to a resource templates list request.
// The server's response to a resources/templates/list request from the client.
type ListResourceTemplatesResult struct {
	PaginatedResult                          // Embeds next cursor
	Meta              map[string]interface{} `json:"_meta,omitempty"`   // Reserved for metadata
	ResourceTemplates []ResourceTemplate     `json:"resourceTemplates"` // Available resource templates
}
