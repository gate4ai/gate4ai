package schema

import (
	schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"
)

// JSONSchemaProperty represents a property within a JSON Schema definition.
type JSONSchemaProperty = schema2024.JSONSchemaProperty

// Arguments is a type alias for tool arguments map.
type Arguments = schema2024.Arguments

// Meta is a type alias for the reserved metadata field.
type Meta = schema2024.Meta

// ToolAnnotations provides additional properties describing a Tool to clients.
// NOTE: all properties in ToolAnnotations are **hints**.
// They are not guaranteed to provide a faithful description of tool behavior.
// Clients should never make tool use decisions based on ToolAnnotations received from untrusted servers.
type ToolAnnotations struct {
	// A human-readable title for the tool.
	Title string `json:"title,omitempty"`
	// If true, the tool does not modify its environment (Default: false).
	ReadOnlyHint *bool `json:"readOnlyHint,omitempty"`
	// If true, the tool may perform destructive updates (Default: true).
	// (Meaningful only when readOnlyHint == false).
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	// If true, repeated calls with same args have no additional effect (Default: false).
	// (Meaningful only when readOnlyHint == false).
	IdempotentHint *bool `json:"idempotentHint,omitempty"`
	// If true, this tool may interact with an "open world" (Default: true).
	OpenWorldHint *bool `json:"openWorldHint,omitempty"`
}

// Tool defines a callable tool the client can use.
type Tool struct {
	// The name of the tool.
	Name string `json:"name"`
	// A human-readable description of the tool.
	Description string `json:"description,omitempty"`
	// A JSON Schema object defining the expected parameters for the tool.
	InputSchema *JSONSchemaProperty `json:"inputSchema,omitempty"`
	// Optional additional tool information.
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ListToolsRequest requests a list of available tools.
// Sent from the client to request a list of tools the server has.
type ListToolsRequest struct {
	Method string                 `json:"method"` // const: "tools/list"
	Params ListToolsRequestParams `json:"params,omitempty"`
}

// ListToolsRequestParams contains parameters for tool listing requests.
type ListToolsRequestParams struct {
	PaginatedRequestParams // Embeds pagination cursor
}

// ListToolsResult is the response to a tools list request.
// The server's response to a tools/list request from the client.
type ListToolsResult struct {
	PaginatedResult        // Embeds next cursor
	Meta            Meta   `json:"_meta,omitempty"` // Reserved for metadata
	Tools           []Tool `json:"tools"`           // Available tools
}

// CallToolRequest requests a tool invocation.
// Used by the client to invoke a tool provided by the server.
type CallToolRequest struct {
	Method string                `json:"method"` // const: "tools/call"
	Params CallToolRequestParams `json:"params"`
}

// CallToolRequestParams contains parameters for tool call requests.
type CallToolRequestParams struct {
	// The name of the tool.
	Name string `json:"name"`
	// Arguments for the tool call.
	Arguments Arguments `json:"arguments"` // removed:omitempty because several implimentation require this field to be present. Send empty object if no arguments are needed.
}

// CallToolResult contains the result of a tool invocation.
// The server's response to a tool call.
type CallToolResult struct {
	Meta *Meta `json:"_meta,omitempty"` // Reserved for metadata
	// Result content, can be Text, Image, Audio, or EmbeddedResource.
	Content []Content `json:"content"`
	// Whether the tool call ended in an error. If not set, assumed false.
	IsError bool `json:"isError,omitempty"`
}

// ToolListChangedNotification informs that available tools have changed.
// An optional notification from the server to the client.
type ToolListChangedNotification struct {
	Method string                 `json:"method"` // const: "notifications/tools/list_changed"
	Params map[string]interface{} `json:"params,omitempty"`
}
