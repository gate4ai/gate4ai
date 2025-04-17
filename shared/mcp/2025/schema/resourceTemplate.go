package schema

import schema2024 "github.com/gate4ai/gate4ai/shared/mcp/2024/schema"

// ResourceTemplate describes a template description for resources available on the server.
type ResourceTemplate = schema2024.ResourceTemplate

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
