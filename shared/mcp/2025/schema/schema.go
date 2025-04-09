package schema

import "encoding/json"

// Request is the base structure for JSON-RPC requests.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Meta   *struct {
		// If specified, the caller is requesting out-of-band progress notifications.
		ProgressToken ProgressToken `json:"progressToken,omitempty"`
	} `json:"_meta,omitempty"`
}

// Notification is the base structure for JSON-RPC notifications.
type Notification struct {
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params,omitempty"`
	Meta   map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata in params
}

// Result is the base structure for JSON-RPC results.
type Result struct {
	Meta map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
}

// ProgressToken is a type alias for request progress tracking tokens (string or integer).
type ProgressToken = interface{}

// === Aggregated Types ===
// These interfaces act as union types for messages flowing between client and server.

// ClientRequest aggregates all possible client->server requests.
type ClientRequest interface {
	isClientRequest() // Marker method
}

// Implement marker method for specific request types
func (*InitializeRequest) isClientRequest()            {}
func (*PingRequest) isClientRequest()                  {}
func (*ListResourcesRequest) isClientRequest()         {} // Added for completeness
func (*ListResourceTemplatesRequest) isClientRequest() {}
func (*ReadResourceRequest) isClientRequest()          {}
func (*SubscribeRequest) isClientRequest()             {}
func (*UnsubscribeRequest) isClientRequest()           {}
func (*ListPromptsRequest) isClientRequest()           {}
func (*GetPromptRequest) isClientRequest()             {}
func (*ListToolsRequest) isClientRequest()             {}
func (*CallToolRequest) isClientRequest()              {}
func (*SetLevelRequest) isClientRequest()              {}
func (*CompleteRequest) isClientRequest()              {}

// ClientNotification aggregates all possible client->server notifications.
type ClientNotification interface {
	isClientNotification() // Marker method
}

// Implement marker method for specific notification types
func (*CancelledNotification) isClientNotification()        {}
func (*InitializedNotification) isClientNotification()      {}
func (*ProgressNotification) isClientNotification()         {}
func (*RootsListChangedNotification) isClientNotification() {}

// ClientResult aggregates all possible server->client results (responses to client requests).
type ClientResult interface {
	isClientResult() // Marker method
}

// Implement marker method for specific result types
// Note: Many requests might return a simple Result (or EmptyResult).
func (*Result) isClientResult()                      {} // For Ping, SetLevel, Subscribe, Unsubscribe
func (*InitializeResult) isClientResult()            {}
func (*ListResourcesResult) isClientResult()         {}
func (*ListResourceTemplatesResult) isClientResult() {}
func (*ReadResourceResult) isClientResult()          {}
func (*ListPromptsResult) isClientResult()           {}
func (*GetPromptResult) isClientResult()             {}
func (*ListToolsResult) isClientResult()             {}
func (*CallToolResult) isClientResult()              {}
func (*CompleteResult) isClientResult()              {}

// ServerRequest aggregates all possible server->client requests.
type ServerRequest interface {
	isServerRequest() // Marker method
}

// Implement marker method for specific request types
func (*PingRequest) isServerRequest()          {}
func (*CreateMessageRequest) isServerRequest() {}
func (*ListRootsRequest) isServerRequest()     {}

// ServerNotification aggregates all possible server->client notifications.
type ServerNotification interface {
	isServerNotification() // Marker method
}

// Implement marker method for specific notification types
func (*CancelledNotification) isServerNotification()           {}
func (*ProgressNotification) isServerNotification()            {}
func (*ResourceListChangedNotification) isServerNotification() {}
func (*ResourceUpdatedNotification) isServerNotification()     {}
func (*PromptListChangedNotification) isServerNotification()   {}
func (*ToolListChangedNotification) isServerNotification()     {}
func (*LoggingMessageNotification) isServerNotification()      {}

// ServerResult aggregates all possible client->server results (responses to server requests).
type ServerResult interface {
	isServerResult() // Marker method
}

// Implement marker method for specific result types
func (*Result) isServerResult()              {} // For Ping
func (*CreateMessageResult) isServerResult() {}
func (*ListRootsResult) isServerResult()     {}
