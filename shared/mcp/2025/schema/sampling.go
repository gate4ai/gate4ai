package schema

// SamplingMessage describes a message issued to or received from an LLM API.
type SamplingMessage struct {
	Role    Role    `json:"role"`    // Message sender (user or assistant)
	Content Content `json:"content"` // Message content (TextContent, ImageContent, or AudioContent)
}

// CreateMessageRequest requests LLM sampling from the client.
// Sent from the server to the client.
type CreateMessageRequest struct {
	Method string                     `json:"method"` // const: "sampling/createMessage"
	Params CreateMessageRequestParams `json:"params"`
}

// CreateMessageRequestParams contains parameters for LLM sampling.
type CreateMessageRequestParams struct {
	Messages         []SamplingMessage `json:"messages"`                   // Messages to use for sampling
	ModelPreferences *ModelPreferences `json:"modelPreferences,omitempty"` // Server's preferences for model selection
	SystemPrompt     string            `json:"systemPrompt,omitempty"`     // Optional system prompt
	IncludeContext   string            `json:"includeContext,omitempty"`   // Request context inclusion ("none", "thisServer", "allServers")
	Temperature      *float64          `json:"temperature,omitempty"`      // Sampling temperature
	MaxTokens        int               `json:"maxTokens"`                  // Maximum tokens to sample
	StopSequences    []string          `json:"stopSequences,omitempty"`    // Sequences that should stop sampling
	Metadata         interface{}       `json:"metadata,omitempty"`         // Optional provider-specific metadata
}

// CreateMessageResult contains the result of LLM sampling.
// The client's response to a sampling/create_message request.
type CreateMessageResult struct {
	Meta       map[string]interface{} `json:"_meta,omitempty"`      // Reserved for metadata
	Role       Role                   `json:"role"`                 // Role of the generated message (usually "assistant")
	Content    Content                `json:"content"`              // Generated message content (TextContent, ImageContent, AudioContent)
	Model      string                 `json:"model"`                // Name of the model that generated the message
	StopReason string                 `json:"stopReason,omitempty"` // Reason why sampling stopped, if known
}
