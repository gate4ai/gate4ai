package schema

// GetPromptRequestParams contains parameters for prompt retrieval.
type GetPromptRequestParams struct {
	Arguments map[string]string `json:"arguments,omitempty"` // Arguments for templating
	Name      string            `json:"name"`                // Name of the prompt or template
}

// GetPromptResult is the server's response to a prompts/get request.
type GetPromptResult struct {
	Meta        map[string]interface{} `json:"_meta,omitempty"`       // Reserved for metadata
	Description string                 `json:"description,omitempty"` // Optional prompt description
	Messages    []PromptMessage        `json:"messages"`              // Prompt messages
	IsError     bool
	Error       error
}

// ListPromptsResult is the server's response to a prompts/list request.
type ListPromptsResult struct {
	Meta       map[string]interface{} `json:"_meta,omitempty"`      // Reserved for metadata
	NextCursor *Cursor                `json:"nextCursor,omitempty"` // Pagination token for next page
	Prompts    []Prompt               `json:"prompts"`              // Available prompts
}

// Prompt describes a prompt or template offered by the server.
type Prompt struct {
	Arguments   []PromptArgument `json:"arguments,omitempty"`   // Template arguments
	Description string           `json:"description,omitempty"` // Optional description
	Name        string           `json:"name"`                  // Prompt name
}

// PromptArgument describes an argument for a prompt template.
type PromptArgument struct {
	Description string `json:"description,omitempty"` // Argument description
	Name        string `json:"name"`                  // Argument name
	Required    bool   `json:"required,omitempty"`    // Whether argument is required
}

// PromptListChangedNotification informs that available prompts have changed.
type PromptListChangedNotification struct {
	Method string         `json:"method"` // const: "notifications/prompts/list_changed"
	Params map[string]any `json:"params,omitempty"`
}

// PromptMessage describes a message in a prompt.
type PromptMessage struct {
	Content Content `json:"content"` // Can be TextContent, ImageContent, or EmbeddedResource
	Role    string  `json:"role"`    // Message role
}

// PromptReference identifies a prompt.
type PromptReference struct {
	Name string `json:"name"` // Prompt name
	Type string `json:"type"` // const: "ref/prompt"
}
