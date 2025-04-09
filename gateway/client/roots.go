package client

// Root represents a root directory or file for server operations.
type Root struct {
	Name string `json:"name,omitempty"` // Optional human-readable name
	URI  string `json:"uri"`            // Root URI (must start with file://)
}

// RootsListChangedNotification informs that available roots have changed.
type RootsListChangedNotification struct {
	Method string                 `json:"method"` // const: "notifications/roots/list_changed"
	Params map[string]interface{} `json:"params,omitempty"`
}

// ListRootsRequest is sent from the server to request root URIs.
type ListRootsRequest struct {
	Method string                 `json:"method"` // const: "roots/list"
	Params map[string]interface{} `json:"params,omitempty"`
}

// ListRootsResult is the client's response to a roots/list request.
type ListRootsResult struct {
	Meta  map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Roots []Root                 `json:"roots"`           // Available roots
}
