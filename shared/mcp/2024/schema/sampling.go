package schema

// SamplingMessage describes a message for LLM interaction.
type SamplingMessage struct {
	Content interface{} `json:"content"` // Can be TextContent or ImageContent
	Role    string      `json:"role"`    // Message role
}

// CreateMessageRequest is a request from the server to sample an LLM via the client.
type CreateMessageRequest struct {
	Method string                     `json:"method"` // const: "sampling/createMessage"
	Params CreateMessageRequestParams `json:"params"`
}

// CreateMessageRequestParams contains parameters for LLM sampling requests.
type CreateMessageRequestParams struct {
	IncludeContext   string                 `json:"includeContext,omitempty"`   // Request to include context
	MaxTokens        int                    `json:"maxTokens"`                  // Maximum number of tokens to sample
	Messages         []SamplingMessage      `json:"messages"`                   // Messages for sampling
	Metadata         map[string]interface{} `json:"metadata,omitempty"`         // Optional provider-specific metadata
	ModelPreferences *ModelPreferences      `json:"modelPreferences,omitempty"` // Model preferences
	StopSequences    []string               `json:"stopSequences,omitempty"`    // Stop sequences
	SystemPrompt     string                 `json:"systemPrompt,omitempty"`     // Optional system prompt
	Temperature      float64                `json:"temperature,omitempty"`      // Temperature for sampling
}

// CreateMessageResult is the client's response to a sampling request.
type CreateMessageResult struct {
	Meta       map[string]interface{} `json:"_meta,omitempty"`      // Reserved for metadata
	Content    Content                `json:"content"`              // Can be TextContent or ImageContent
	Model      string                 `json:"model"`                // The name of the model that generated the message
	Role       string                 `json:"role"`                 // The role of the message
	StopReason string                 `json:"stopReason,omitempty"` // Reason why sampling stopped, if known
}
