package schema

import (
	"encoding/json"
)

const PROTOCOL_VERSION = "2024-11-05"

// Implementation describes the name and version of an MCP implementation.
type Implementation struct {
	Name    string `json:"name"`    // Implementation name
	Version string `json:"version"` // Implementation version
}

type Capability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type CapabilityWithSubscribe struct {
	ListChanged bool `json:"listChanged,omitempty"`
	Subscribe   bool `json:"subscribe,omitempty"`
}

// ClientCapabilities describes capabilities a client may support.
type ClientCapabilities struct {
	Experimental map[string]map[string]json.RawMessage `json:"experimental,omitempty"` // Non-standard capabilities
	Roots        *Capability                           `json:"roots,omitempty"`        // Present if client supports listing roots
	Sampling     *struct{}                             `json:"sampling,omitempty"`     // Present if client supports sampling from an LLM
}

// ServerCapabilities describes capabilities a server may support.
type ServerCapabilities struct {
	Experimental map[string]map[string]json.RawMessage `json:"experimental,omitempty"` // Non-standard capabilities
	Logging      map[string]json.RawMessage            `json:"logging,omitempty"`      // Present if supports logging
	Prompts      *Capability                           `json:"prompts,omitempty"`      // Present if offers prompts
	Resources    *CapabilityWithSubscribe              `json:"resources,omitempty"`    // Present if offers resources
	Tools        *Capability                           `json:"tools,omitempty"`        // Present if offers tools
}

// InitializeRequestParams contains parameters for initialization.
type InitializeRequestParams struct {
	Capabilities    ClientCapabilities `json:"capabilities"`    // Client capabilities
	ClientInfo      Implementation     `json:"clientInfo"`      // Client implementation info
	ProtocolVersion string             `json:"protocolVersion"` // Latest supported protocol version
}

// InitializeResult is the server's response to an initialize request.
type InitializeResult struct {
	Meta            map[string]json.RawMessage `json:"_meta,omitempty"`        // Reserved for metadata
	Capabilities    ServerCapabilities         `json:"capabilities"`           // Server capabilities
	Instructions    string                     `json:"instructions,omitempty"` // Usage instructions
	ProtocolVersion string                     `json:"protocolVersion"`        // Protocol version to use
	ServerInfo      Implementation             `json:"serverInfo"`             // Server implementation info
}
