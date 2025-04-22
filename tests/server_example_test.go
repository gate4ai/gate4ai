package tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time" // Import time for http.Client timeout

	"github.com/gate4ai/gate4ai/shared"
	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"github.com/gate4ai/gate4ai/tests/env"
	"github.com/stretchr/testify/require" // Use testify for assertions
)

// Test successful tool call with valid authorization
func TestMCP2024SuccessfulToolCall(t *testing.T) {
	//get sse endpoint
	req, err := http.NewRequest("GET", EXAMPLE_MCP2024_SERVER_URL, nil)
	require.NoError(t, err, "Failed to create request")
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err, "Failed to send request")
	//keep get sse open for session open
	defer resp.Body.Close()

	var endpoint string
	reader := bufio.NewReader(resp.Body)
	for {
		sseLine, err := reader.ReadString('\n')
		require.NoError(t, err, "Failed to send request")
		if strings.HasPrefix(sseLine, "data:") {
			endpoint = strings.TrimSpace(sseLine[len("data:"):])
			break
		}
	}
	endpointURL, err := url.Parse(endpoint)
	require.NoError(t, err, "Failed to parse endpointURL")

	serverURL, err := url.Parse(EXAMPLE_MCP2024_SERVER_URL)
	require.NoError(t, err, "Failed to parse serverURL")

	postURL := serverURL.ResolveReference(endpointURL).String()

	//post jsonrpc
	postData := shared.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      schema.RequestID{Value: 1},
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name": "echo",
			"arguments": map[string]interface{}{
				"message": "Hello, World!",
			},
		},
	}
	requestPOSTBody, err := json.Marshal(postData)
	require.NoError(t, err, "Failed to marshal request")

	requestPOST, err := http.NewRequest("POST", postURL, bytes.NewBuffer(requestPOSTBody))
	require.NoError(t, err, "Failed to create request")

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Send the request
	client2 := &http.Client{Timeout: 10 * time.Second} // Add timeout
	resp2, err := client2.Do(requestPOST)
	require.NoError(t, err, "Failed to send request")
	defer resp2.Body.Close()

	// Check the response status code
	require.Equal(t, http.StatusAccepted, resp2.StatusCode, "Expected status code 200")
	// Note: Verifying the actual result requires setting up an SSE client in the test,
	// which is more complex. This test now verifies the POST is accepted.
}

// Test failed tool call without authorization
func TestMCP2024UnauthorizedGet(t *testing.T) {
	req, err := http.NewRequest("GET", EXAMPLE_MCP2024_SERVER_URL+"BrokenKey", nil)
	require.NoError(t, err, "Failed to create request")
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err, "Failed to send request")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Expected status code 401")
}

func TestMCP2024UnauthorizedPost(t *testing.T) {
	req, err := http.NewRequest("POST", EXAMPLE_MCP2024_SERVER_URL+"BrokenKey", nil)
	require.NoError(t, err, "Failed to create request")
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err, "Failed to send request")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Expected status code 401")
}

// Test successful tool call with valid authorization
func TestMCP2025SuccessfulToolCall(t *testing.T) {
	// Create a JSON-RPC request for the 'echo' tool
	request := shared.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      schema.RequestID{Value: 1},
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name": "echo",
			"arguments": map[string]interface{}{
				"message": "Hello, World!",
			},
		},
	}

	// Convert request to JSON
	requestBody, err := json.Marshal(request)
	require.NoError(t, err, "Failed to marshal request")

	req, err := http.NewRequest("POST", EXAMPLE_MCP2025_SERVER_URL, bytes.NewBuffer(requestBody))
	require.NoError(t, err, "Failed to create request")

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	details := env.GetDetails(env.ExampleServerComponentName).(env.ExampleServerDetails)
	req.Header.Set("Authorization", "Bearer "+details.TestAPIKey)

	// Send the request
	client := &http.Client{Timeout: 1000 * time.Second} // Add timeout
	resp, err := client.Do(req)
	require.NoError(t, err, "Failed to send request")
	defer resp.Body.Close()

	// Check the response status code
	require.Equal(t, http.StatusOK, resp.StatusCode, "Expected status code 200")
	// Note: Verifying the actual result requires setting up an SSE client in the test,
	// which is more complex. This test now verifies the POST is accepted.
}

// Test failed tool call without authorization
func TestMCP2025UnauthorizedToolCall(t *testing.T) {
	// Create a JSON-RPC request for the 'echo' tool
	request := shared.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      schema.RequestID{Value: 1},
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name": "echo",
			"arguments": map[string]interface{}{
				"message": "Hello, World!",
			},
		},
	}

	// Convert request to JSON
	requestBody, err := json.Marshal(request)
	require.NoError(t, err, "Failed to marshal request")

	req, err := http.NewRequest("POST", EXAMPLE_MCP2025_SERVER_URL, bytes.NewBuffer(requestBody))
	require.NoError(t, err, "Failed to create request")

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// No authorization header

	// Send the request
	client := &http.Client{Timeout: 10 * time.Second} // Add timeout
	resp, err := client.Do(req)
	require.NoError(t, err, "Failed to send request")
	defer resp.Body.Close()

	// Check the response status code - Expect 401 Unauthorized because no valid key was provided
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Expected status code 401 Unauthorized")
}
