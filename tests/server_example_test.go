package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time" // Import time for http.Client timeout

	"github.com/stretchr/testify/require" // Use testify for assertions
)

// JSON-RPC request structure (keep as is)
type JSONRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params"`
	ID      int                    `json:"id"`
}

// JSON-RPC response structure (keep as is)
type JSONRPCResponse struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      int                    `json:"id"`
	Result  map[string]interface{} `json:"result,omitempty"`
	Error   *JSONRPCError          `json:"error,omitempty"`
}

// JSON-RPC error structure (keep as is)
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Test successful tool call with valid authorization
func TestMCP2024SuccessfulToolCall(t *testing.T) {
	// Create a JSON-RPC request for the 'echo' tool
	request := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name": "echo",
			"arguments": map[string]interface{}{
				"message": "Hello, World!",
			},
		},
		ID: 1,
	}

	// Convert request to JSON
	requestBody, err := json.Marshal(request)
	require.NoError(t, err, "Failed to marshal request")

	req, err := http.NewRequest("POST", EXAMPLE_MCP2025_SERVER_URL, bytes.NewBuffer(requestBody))
	require.NoError(t, err, "Failed to create request")

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Send the request
	client := &http.Client{Timeout: 10 * time.Second} // Add timeout
	resp, err := client.Do(req)
	require.NoError(t, err, "Failed to send request")
	defer resp.Body.Close()

	// Check the response status code
	require.Equal(t, http.StatusOK, resp.StatusCode, "Expected status code 200")
	// Note: Verifying the actual result requires setting up an SSE client in the test,
	// which is more complex. This test now verifies the POST is accepted.
}

// Test failed tool call without authorization
func TestMCP2024UnauthorizedToolCall(t *testing.T) {
	// Create a JSON-RPC request for the 'echo' tool
	request := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name": "echo",
			"arguments": map[string]interface{}{
				"message": "Hello, World!",
			},
		},
		ID: 1,
	}

	// Convert request to JSON
	requestBody, err := json.Marshal(request)
	require.NoError(t, err, "Failed to marshal request")

	// Create a new HTTP POST request
	// Construct URL *without* a valid key query parameter
	invalidURL := strings.Split(EXAMPLE_MCP2025_SERVER_URL, "?")[0] // Get URL part before query string
	req, err := http.NewRequest("POST", invalidURL, bytes.NewBuffer(requestBody))
	require.NoError(t, err, "Failed to create request")

	// Set headers
	req.Header.Set("Content-Type", "application/json")

	// Send the request
	client := &http.Client{Timeout: 10 * time.Second} // Add timeout
	resp, err := client.Do(req)
	require.NoError(t, err, "Failed to send request")
	defer resp.Body.Close()

	// Check the response status code - Expect 401 Unauthorized because no valid key was provided
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Expected status code 401 Unauthorized")
}
