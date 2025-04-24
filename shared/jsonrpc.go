package shared

import (
	"encoding/json"
	"fmt"

	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
)

const (
	JSONRPCVersion = "2.0"

	// Standard JSON-RPC 2.0 error codes
	JSONRPCErrorParseError     = -32700 // Invalid JSON was received
	JSONRPCErrorInvalidRequest = -32600 // The JSON sent is not a valid Request object
	JSONRPCErrorMethodNotFound = -32601 // The method does not exist / is not available
	JSONRPCErrorInvalidParams  = -32602 // Invalid method parameter(s)
	JSONRPCErrorInternal       = -32603 // Internal JSON-RPC error

	// -32000 to -32099 are reserved for implementation-defined server errors
	JSONRPCErrorServerError = -32000 // Generic server error

	JSONRPCErrorUnauthorized = -32001 // Unauthorized
)

type JSONRPCErrorResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      *schema.RequestID `json:"id,omitempty"`
	Error   *JSONRPCError     `json:"error"`
}

// JSONRPCResponse represents the structure for sending successful JSON-RPC responses.
type JSONRPCResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      *schema.RequestID `json:"id"` // Must be present and same as request ID
	Result  *json.RawMessage  `json:"result"`
}

type JSONRPCMessage struct {
	JSONRPC string            `json:"jsonrpc"` // Must be "2.0"
	ID      *schema.RequestID `json:"id,omitempty"`
	Method  *string           `json:"method,omitempty"`
	Params  *json.RawMessage  `json:"params,omitempty"`
	Error   *JSONRPCError     `json:"error,omitempty"`
}

type JSONRPCNotification struct {
	JSONRPC string           `json:"jsonrpc"` // Must be "2.0"
	Method  *string          `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

type JSONRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"` // Must be "2.0"
	ID      schema.RequestID `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  map[string]any   `json:"params,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int         `json:"code"`           // Error type code
	Message string      `json:"message"`        // Short error description
	Data    interface{} `json:"data,omitempty"` // Additional error information
}

// Error implements the Go error interface for JSONRPCError.
func (e *JSONRPCError) Error() string {
	// Provide a standard Go error string representation
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

func NewJSONRPCError(err error) *JSONRPCError {
	if err == nil {
		return nil
	}
	return &JSONRPCError{
		Code:    JSONRPCErrorInternal,
		Message: err.Error(),
	}
}
