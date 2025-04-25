package schema

// AgentAuthentication describes the authentication schemes supported or required by an agent.
type AgentAuthentication struct {
	// List of supported authentication schemes (e.g., "apiKey", "oauth2", "jwt").
	Schemes []string `json:"schemes"`
	// Placeholder for required credentials or configuration details (structure depends on the scheme).
	Credentials *string `json:"credentials,omitempty"`
}

// AgentCapabilities lists the optional capabilities supported by the agent.
type AgentCapabilities struct {
	// Indicates if the agent supports Server-Sent Events (SSE) for streaming updates via `tasks/sendSubscribe`.
	Streaming bool `json:"streaming,omitempty"`
	// Indicates if the agent supports receiving push notification configurations via `tasks/pushNotification/set`.
	PushNotifications bool `json:"pushNotifications,omitempty"`
	// Indicates if the agent supports returning task history via the `historyLength` parameter.
	StateTransitionHistory bool `json:"stateTransitionHistory,omitempty"`
}

// AgentProvider contains information about the organization providing the agent.
type AgentProvider struct {
	// Name of the organization.
	Organization string `json:"organization"`
	// URL of the organization's website.
	URL *string `json:"url,omitempty"`
}

// AgentSkill describes a specific skill or capability offered by the agent.
type AgentSkill struct {
	// Unique identifier for the skill.
	ID string `json:"id"`
	// Human-readable name of the skill.
	Name string `json:"name"`
	// Detailed description of the skill.
	Description *string `json:"description,omitempty"`
	// Keywords or tags associated with the skill.
	Tags []string `json:"tags,omitempty"`
	// Examples demonstrating how to use the skill.
	Examples []string `json:"examples,omitempty"`
	// Input content types supported specifically by this skill (overrides agent defaults).
	InputModes []string `json:"inputModes,omitempty"`
	// Output content types produced specifically by this skill (overrides agent defaults).
	OutputModes []string `json:"outputModes,omitempty"`
}

// AgentCard provides metadata about an AI agent, enabling discovery and capability understanding.
// Typically served at `/.well-known/agent.json`.
type AgentCard struct {
	// Human-readable name of the agent. (Required)
	Name string `json:"name"`
	// A brief description of the agent's purpose. (Optional)
	Description *string `json:"description,omitempty"`
	// The base URL endpoint for the agent's A2A JSON-RPC service. (Required)
	URL string `json:"url"`
	// Information about the agent's provider. (Optional)
	Provider *AgentProvider `json:"provider,omitempty"`
	// Version of the agent or its API. (Required)
	Version string `json:"version"`
	// URL pointing to the agent's documentation. (Optional)
	DocumentationURL *string `json:"documentationUrl,omitempty"`
	// Capabilities supported by the agent. (Required)
	Capabilities AgentCapabilities `json:"capabilities"`
	// Authentication details required to interact with the agent. (Optional)
	Authentication *AgentAuthentication `json:"authentication,omitempty"`
	// Default input content types supported by the agent (e.g., "text", "file"). (Optional, defaults inferred)
	DefaultInputModes []string `json:"defaultInputModes,omitempty"`
	// Default output content types produced by the agent (e.g., "text", "file"). (Optional, defaults inferred)
	DefaultOutputModes []string `json:"defaultOutputModes,omitempty"`
	// List of specific skills the agent offers. (Required, can be empty array if no specific skills)
	Skills []AgentSkill `json:"skills"`
}
