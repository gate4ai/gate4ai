package schema

import (
	"encoding/json"

	schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"
)

// PROTOCOL_VERSION specifies the version of the MCP protocol defined in this schema.
const PROTOCOL_VERSION = "2025-03-26"

// Capability represents a basic capability marker.
type Capability = schema2024.Capability

// CapabilityWithSubscribe represents a capability that includes subscription support.
type CapabilityWithSubscribe = schema2024.CapabilityWithSubscribe

// ClientCapabilities describes capabilities a client may support.
type ClientCapabilities = schema2024.ClientCapabilities

// Implementation describes the name and version of an MCP implementation.
type Implementation = schema2024.Implementation

// InitializeRequestParams contains parameters for initialization.
type InitializeRequestParams = schema2024.InitializeRequestParams

// ServerCapabilities describes features the server supports.
type ServerCapabilities struct {
	Experimental map[string]json.RawMessage `json:"experimental,omitempty"` // Experimental, non-standard capabilities
	Logging      *struct{}                  `json:"logging,omitempty"`      // Present if the server supports sending log messages to the client
	Completions  *struct{}                  `json:"completions,omitempty"`  // Present if the server supports argument autocompletion suggestions
	Prompts      *Capability                `json:"prompts,omitempty"`      // Present if the server offers any prompt templates
	Resources    *CapabilityWithSubscribe   `json:"resources,omitempty"`    // Present if the server offers any resources to read
	Tools        *Capability                `json:"tools,omitempty"`        // Present if the server offers any tools to call
}

// InitializeResult is the server's response to initialization.
type InitializeResult struct {
	Meta            map[string]interface{} `json:"_meta,omitempty"`        // Reserved for metadata
	ProtocolVersion string                 `json:"protocolVersion"`        // Server's chosen protocol version
	Capabilities    ServerCapabilities     `json:"capabilities"`           // Server capabilities
	ServerInfo      Implementation         `json:"serverInfo"`             // Server implementation info
	Instructions    string                 `json:"instructions,omitempty"` // Instructions describing how to use the server and its features
}

// InitializedNotification informs the server that initialization is complete.
// This notification is sent from the client to the server after initialization has finished.
type InitializedNotification struct {
	Method string                 `json:"method"` // const: "notifications/initialized"
	Params map[string]interface{} `json:"params,omitempty"`
}

// InitializeRequest is sent by the client to start initialization.
// This request is sent from the client to the server when it first connects.
type InitializeRequest struct {
	Method string                  `json:"method"` // const: "initialize"
	Params InitializeRequestParams `json:"params"`
}
