package schema

import schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"

// Role represents the sender or recipient of messages and data in a conversation.
type Role = schema2024.Role

// Role constants
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// PromptMessage describes a message returned as part of a prompt.
// This is similar to `SamplingMessage`, but also supports the embedding of
// resources from the MCP server.
type PromptMessage struct {
	Role    Role    `json:"role"`    // Message sender/recipient (user or assistant)
	Content Content `json:"content"` // Message content (TextContent, ImageContent, AudioContent, or EmbeddedResource)
}

// PromptArgument describes an argument that a prompt can accept.
type PromptArgument = schema2024.PromptArgument

// Prompt describes a prompt or prompt template that the server offers.
type Prompt struct {
	Name        string           `json:"name"`                  // The name of the prompt or prompt template
	Description string           `json:"description,omitempty"` // An optional description of what this prompt provides
	Arguments   []PromptArgument `json:"arguments,omitempty"`   // A list of arguments to use for templating the prompt
}

// ListPromptsRequest requests a list of available prompts.
// Sent from the client to request a list of prompts and prompt templates the server has.
type ListPromptsRequest struct {
	Method string                   `json:"method"` // const: "prompts/list"
	Params ListPromptsRequestParams `json:"params,omitempty"`
}

// ListPromptsRequestParams contains parameters for prompt listing requests.
type ListPromptsRequestParams struct {
	PaginatedRequestParams // Embeds pagination cursor
}

// ListPromptsResult is the server's response to a prompts/list request.
type ListPromptsResult struct {
	PaginatedResult                        // Embeds next cursor
	Meta            map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Prompts         []Prompt               `json:"prompts"`         // Available prompts
}

// GetPromptRequest requests a specific prompt from the server.
// Used by the client to get a prompt provided by the server.
type GetPromptRequest struct {
	Method string                 `json:"method"` // const: "prompts/get"
	Params GetPromptRequestParams `json:"params"`
}

// GetPromptRequestParams contains parameters for prompt retrieval.
type GetPromptRequestParams struct {
	Name      string            `json:"name"`                // The name of the prompt or prompt template
	Arguments map[string]string `json:"arguments,omitempty"` // Arguments to use for templating the prompt
}

// GetPromptResult contains the retrieved prompt.
// The server's response to a prompts/get request from the client.
type GetPromptResult struct {
	Meta        *Meta           `json:"_meta,omitempty"`       // Reserved for metadata
	Description string          `json:"description,omitempty"` // An optional description for the prompt
	Messages    []PromptMessage `json:"messages"`              // Prompt messages
}

// PromptListChangedNotification informs that available prompts have changed.
// An optional notification from the server to the client.
type PromptListChangedNotification struct {
	Method string                 `json:"method"` // const: "notifications/prompts/list_changed"
	Params map[string]interface{} `json:"params,omitempty"`
}

// PromptReference identifies a prompt.
type PromptReference = schema2024.PromptReference
