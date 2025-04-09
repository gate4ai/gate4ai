package schema

type Arguments = map[string]interface{}
type Meta = map[string]interface{}

// CallToolParameters contains parameters for tool calls.
type CallToolParameters struct {
	Arguments Arguments `json:"arguments"`
	Name      string    `json:"name"`
}

// CallToolResult is the server's response to a tool call.
type CallToolResult struct {
	Meta    Meta      `json:"_meta,omitempty"`   // Reserved for metadata
	Content []Content `json:"content"`           // Can be TextContent, ImageContent, or EmbeddedResource
	IsError bool      `json:"isError,omitempty"` // Whether the tool call ended in an error
	//Error   error     `json:"error,omitempty"`   // Error details
}

// ListToolsResult is the response to a tools list request.
type ListToolsResult struct {
	Meta       Meta    `json:"_meta,omitempty"`      // Reserved for metadata
	NextCursor *Cursor `json:"nextCursor,omitempty"` // Pagination token for next page
	Tools      []*Tool `json:"tools"`                // Available tools
}

// JSONSchemaProperty represents a property in a JSON Schema
type JSONSchemaProperty struct {
	Type                 string                        `json:"type,omitempty"`
	Description          string                        `json:"description,omitempty"`
	Properties           map[string]JSONSchemaProperty `json:"properties,omitempty"`
	Required             []string                      `json:"required,omitempty"`
	Items                *JSONSchemaProperty           `json:"items,omitempty"`
	AdditionalProperties interface{}                   `json:"additionalProperties,omitempty"`
	Const                interface{}                   `json:"const,omitempty"`
	Ref                  string                        `json:"$ref,omitempty"`
	Schema               string                        `json:"$schema,omitempty"`
	AnyOf                []JSONSchemaProperty          `json:"anyOf,omitempty"`
	OneOf                []JSONSchemaProperty          `json:"oneOf,omitempty"`
	AllOf                []JSONSchemaProperty          `json:"allOf,omitempty"`
	Not                  *JSONSchemaProperty           `json:"not,omitempty"`
	PatternProperties    map[string]JSONSchemaProperty `json:"patternProperties,omitempty"`
	Definitions          map[string]JSONSchemaProperty `json:"definitions,omitempty"`
	Format               string                        `json:"format,omitempty"`
	Enum                 []interface{}                 `json:"enum,omitempty"`
	Minimum              *float64                      `json:"minimum,omitempty"`
	Maximum              *float64                      `json:"maximum,omitempty"`
	ExclusiveMinimum     *float64                      `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *float64                      `json:"exclusiveMaximum,omitempty"`
	MultipleOf           *float64                      `json:"multipleOf,omitempty"`
	MinLength            *int                          `json:"minLength,omitempty"`
	MaxLength            *int                          `json:"maxLength,omitempty"`
	Pattern              string                        `json:"pattern,omitempty"`
	MinItems             *int                          `json:"minItems,omitempty"`
	MaxItems             *int                          `json:"maxItems,omitempty"`
	UniqueItems          *bool                         `json:"uniqueItems,omitempty"`
	MinProperties        *int                          `json:"minProperties,omitempty"`
	MaxProperties        *int                          `json:"maxProperties,omitempty"`
}

// Tool defines a callable tool the client can use.
type Tool struct {
	Description string             `json:"description,omitempty"` // Tool description
	InputSchema JSONSchemaProperty `json:"inputSchema"`           // JSON Schema for parameters
	Name        string             `json:"name"`                  // Tool name
}

// ToolListChangedNotification informs that available tools have changed.
type ToolListChangedNotification struct {
	Method string                 `json:"method"` // const: "notifications/tools/list_changed"
	Params map[string]interface{} `json:"params,omitempty"`
}
