package schema

import "encoding/json"

// Root represents a root directory or file that the server can operate on.
type Root struct {
	// The URI identifying the root. This *must* start with file:// for now.
	URI string `json:"uri"` // @format uri
	// An optional name for the root.
	Name string `json:"name,omitempty"`
}

// RootsListChangedNotification informs that available roots have changed.
// A notification from the client to the server.
type RootsListChangedNotification struct {
	Method string                 `json:"method"` // const: "notifications/roots/list_changed"
	Params map[string]interface{} `json:"params,omitempty"`
}

// ListRootsRequest is sent from the server to request root URIs from the client.
type ListRootsRequest struct {
	Method string          `json:"method"`           // const: "roots/list"
	Params json.RawMessage `json:"params,omitempty"` // Allows for _meta field
}

// ListRootsResult is the client's response to a roots/list request.
type ListRootsResult struct {
	Meta  map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Roots []Root                 `json:"roots"`           // Available roots
}
