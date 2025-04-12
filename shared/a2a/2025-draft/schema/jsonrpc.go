package schema

import (
	"encoding/json"
	"fmt"
)

// JSONRPCVersion defines the JSON-RPC version used ("2.0").
const JSONRPCVersion = "2.0"

// JSONRPCMessage is the base structure for JSON-RPC messages.
type JSONRPCMessage struct {
	// Specifies the JSON-RPC version, must be "2.0".
	JSONRPC string `json:"jsonrpc"`
	// Request identifier (string or number). Null for notifications.
	ID *any `json:"id,omitempty"`
}

// JSONRPCRequest represents a JSON-RPC request object.
type JSONRPCRequest struct {
	// Specifies the JSON-RPC version, must be "2.0".
	JSONRPC string `json:"jsonrpc"`
	// The name of the method to be invoked.
	Method string `json:"method"`
	// Parameters for the method, can be an object or array.
	Params *json.RawMessage `json:"params,omitempty"`
	// Request identifier (string or number). If null/omitted, it's a notification.
	ID *any `json:"id,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC response object.
type JSONRPCResponse struct {
	// Specifies the JSON-RPC version, must be "2.0".
	JSONRPC string `json:"jsonrpc"`
	// The result of the method invocation (on success). Mutually exclusive with Error.
	Result *json.RawMessage `json:"result,omitempty"`
	// Error object if the request failed. Mutually exclusive with Result.
	Error *JSONRPCError `json:"error,omitempty"`
	// Must match the ID of the corresponding request. Null if could not be determined (e.g., parse error).
	ID *any `json:"id"` // Note: ID is nullable in error cases according to spec.
}

// JSONRPCError represents a JSON-RPC error object.
type JSONRPCError struct {
	// A number indicating the error type that occurred.
	Code int `json:"code"`
	// A string providing a short description of the error.
	Message string `json:"message"`
	// Optional additional information about the error.
	Data *any `json:"data,omitempty"`
}

// Error implements the Go error interface for JSONRPCError.
func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC Error %d: %s", e.Code, e.Message)
}

// Standard JSON-RPC error codes
const (
	ErrorParseError     = -32700 // Invalid JSON received.
	ErrorInvalidRequest = -32600 // JSON is not a valid Request object.
	ErrorMethodNotFound = -32601 // Method does not exist/is not available.
	ErrorInvalidParams  = -32602 // Invalid method parameter(s).
	ErrorInternalError  = -32603 // Internal JSON-RPC error.
	// -32000 to -32099: Implementation-defined server errors.
)
