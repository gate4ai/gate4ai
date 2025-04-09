package transport_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gate4ai/mcp/server/transport"
	"github.com/gate4ai/mcp/shared"
	schema2025 "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Specification requirement: The server MUST provide a single HTTP endpoint path... that supports both POST and GET methods.
func Test_SRV_25_HTTP_POS_01_ProvidesSingleEndpointForPostGet(t *testing.T) {
	_, _, _, server, cleanup := setupServerTest(t) // Uses helper from transport_server_helpers_test.go
	defer cleanup()

	// Test OPTIONS method
	req, _ := http.NewRequest("OPTIONS", server.URL+transport.PATH, nil) // Use V2025 path
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNoContent, resp.StatusCode) // OPTIONS usually returns 200 or 204
	allowHeader := resp.Header.Get("Allow")
	assert.Contains(t, allowHeader, "POST", "Allow header should contain POST")
	assert.Contains(t, allowHeader, "GET", "Allow header should contain GET")
	assert.Contains(t, allowHeader, "DELETE", "Allow header should contain DELETE")
}

// Specification requirement: If the input contains any number of JSON-RPC _requests_, the server MUST either return ... `Content-Type: application/json`, to return one JSON object.
func Test_SRV_25_HTTP_POS_02_PostRequestReturnsJsonResponse(t *testing.T) {
	tp, _, _, server, cleanup := setupServerTest(t)
	defer cleanup()
	tp.NoStream2025 = true // Configure transport *not* to stream for this test

	requestBody := createJsonRpcRequestBody(1, "initialize", schema2025.InitializeRequestParams{ // Send initialize as a valid request
		ProtocolVersion: schema2025.PROTOCOL_VERSION,
		ClientInfo:      schema2025.Implementation{Name: "test-client", Version: "1.0"},
		Capabilities:    schema2025.ClientCapabilities{},
	})
	resp, err := makePostRequest(t, server.URL+transport.PATH, requestBody, nil) // Use V2025 path
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	// Assert the body is a valid InitializeResult
	bodyBytes, _ := io.ReadAll(resp.Body)

	// Check top-level JSON-RPC structure first
	var rawResp shared.JSONRPCResponse
	errRpc := json.Unmarshal(bodyBytes, &rawResp)
	require.NoError(t, errRpc, "Response is not valid JSON-RPC: %s", string(bodyBytes))
	require.NotNil(t, rawResp.Result, "JSON-RPC result field is missing")

	// Now unmarshal the result field specifically
	var result schema2025.InitializeResult
	err = json.Unmarshal(*rawResp.Result, &result)
	require.NoError(t, err, "Failed to unmarshal InitializeResult from result field: %s", string(*rawResp.Result))
	assert.Equal(t, schema2025.PROTOCOL_VERSION, result.ProtocolVersion)
	assert.Equal(t, "TestServer", result.ServerInfo.Name) // Check against mock value
}

// Specification requirement: If the input contains any number of JSON-RPC _requests_, the server MUST either return `Content-Type: text/event-stream`, to initiate an SSE stream...
// Specification requirement: If the server initiates an SSE stream: The SSE stream SHOULD eventually include one JSON-RPC _response_ per each JSON-RPC _request_... After all JSON-RPC _responses_ have been sent, the server SHOULD close the SSE stream.
func Test_SRV_25_HTTP_POS_03_PostRequestReturnsSseStreamResponse(t *testing.T) {
	tp, mockManager, _, server, cleanup := setupServerTest(t)
	defer cleanup()
	tp.NoStream2025 = false // Configure transport *to* stream
	sessionID := ""         // To store the session ID received from initialize

	// 1. Send POST request
	initBody := createJsonRpcRequestBody(1, "initialize", schema2025.InitializeRequestParams{ // Send initialize first
		ProtocolVersion: schema2025.PROTOCOL_VERSION,
		ClientInfo:      schema2025.Implementation{Name: "test-client", Version: "1.0"},
		Capabilities:    schema2025.ClientCapabilities{},
	})
	respInit, err := makePostRequest(t, server.URL+transport.PATH, initBody, nil)
	require.NoError(t, err)
	defer respInit.Body.Close() // Ensure body is closed eventually

	// 2. Assert SSE headers and status for Initialize
	require.Equal(t, http.StatusOK, respInit.StatusCode)
	require.Equal(t, "text/event-stream", respInit.Header.Get("Content-Type"))
	sessionID = respInit.Header.Get(transport.MCP_SESSION_HEADER)
	require.NotEmpty(t, sessionID, "Mcp-Session-Id header missing in initialize response")

	// 3. Read SSE stream for the Initialize response
	sseReader := bufio.NewReader(respInit.Body)
	found := false
	var initRespMsg shared.Message
	readTimeoutInit := time.After(3 * time.Second)
readLoop:
	for {
		select {
		case <-readTimeoutInit:
			t.Fatalf("Timed out waiting for Initialize SSE message response")
		default:
			event, data, _, errRead := readNextSseEvent(t, sseReader) // Use _ to ignore the id
			if errRead == io.EOF {
				break readLoop
			} // Stream closed unexpectedly
			require.NoError(t, errRead)
			if event == "message" {
				errUnmarshal := json.Unmarshal([]byte(data), &initRespMsg)
				require.NoError(t, errUnmarshal)
				if initRespMsg.ID != nil && initRespMsg.ID.String() == "1" { // Check if it's the init response
					found = true
					break readLoop // Found the response
				}
			}
		}
	}
	require.True(t, found, "Did not receive Initialize response message via SSE")
	assert.Nil(t, initRespMsg.Error)
	assert.NotNil(t, initRespMsg.Result)

	// 4. Send Ping request on the *same* session via POST, including Session ID header
	pingBody := createJsonRpcRequestBody(2, "ping", nil)
	pingHeaders := map[string]string{transport.MCP_SESSION_HEADER: sessionID}
	respPing, err := makePostRequest(t, server.URL+transport.PATH, pingBody, pingHeaders)
	require.NoError(t, err)
	defer respPing.Body.Close()
	// Server should return 202 Accepted for the POST containing the request,
	// because the actual response will come via the existing SSE stream.
	require.Equal(t, http.StatusOK, respPing.StatusCode, "Status should be 200 OK")

	sseReader = bufio.NewReader(respPing.Body)

	// 5. Read SSE stream for the Ping response
	foundPing := false
	var pingRespMsg shared.Message
	readTimeoutPing := time.After(3 * time.Second)
readLoopPing:
	for {
		select {
		case <-readTimeoutPing:
			t.Fatalf("Timed out waiting for Ping SSE message response")
		default:
			event, data, _, errRead := readNextSseEvent(t, sseReader) // Use _ to ignore the id
			if errRead == io.EOF {
				break readLoopPing
			} // Stream closed
			require.NoError(t, errRead)
			if event == "message" {
				errUnmarshal := json.Unmarshal([]byte(data), &pingRespMsg)
				require.NoError(t, errUnmarshal)
				if pingRespMsg.ID != nil && pingRespMsg.ID.String() == "2" { // Check for ping response ID
					foundPing = true
					break readLoopPing // Found the response
				}
			}
		}
	}

	require.True(t, foundPing, "Did not receive expected Ping response message via SSE")
	assert.Nil(t, pingRespMsg.Error)
	assert.NotNil(t, pingRespMsg.Result) // Ping returns {} result

	// 6. Assert stream *should* close after response (may need longer timeout/check)
	// We can try reading again and expect EOF or timeout.
	_, _, _, errRead := readNextSseEvent(t, sseReader)
	assert.ErrorIs(t, errRead, io.EOF, "Expected SSE stream to close after response")

	// Verify session state if possible
	if sessionID != "" {
		_, err = mockManager.GetSession(sessionID)
		assert.NoError(t, err, "Session should still exist after POST->SSE response")
	}
}

// Specification requirement: If the input consists solely of (any number of) JSON-RPC _responses_ or _notifications_... If the server accepts the input, the server MUST return HTTP status code 202 Accepted with no body.
func Test_SRV_25_HTTP_POS_04_PostNotificationOrResponseReturnsAccepted(t *testing.T) {
	_, _, _, server, cleanup := setupServerTest(t)
	defer cleanup()
	// init session
	initBody := createJsonRpcRequestBody(1, "initialize", schema2025.InitializeRequestParams{ // Send initialize first
		ProtocolVersion: schema2025.PROTOCOL_VERSION,
		ClientInfo:      schema2025.Implementation{Name: "test-client", Version: "1.0"},
		Capabilities:    schema2025.ClientCapabilities{},
	})
	respInit, err := makePostRequest(t, server.URL+transport.PATH, initBody, nil)
	defer respInit.Body.Close() // Ensure body is closed eventually
	sessionID := respInit.Header.Get(transport.MCP_SESSION_HEADER)
	sessionIDHeader := map[string]string{transport.MCP_SESSION_HEADER: sessionID}

	// Test with Notification
	notifBody := createJsonRpcNotificationBody("notifications/test", map[string]bool{"done": true})
	respNotif, err := makePostRequest(t, server.URL+transport.PATH, notifBody, sessionIDHeader)
	require.NoError(t, err)
	defer respNotif.Body.Close()
	assert.Equal(t, http.StatusAccepted, respNotif.StatusCode, "POST Notification status")
	bodyBytesNotif, _ := io.ReadAll(respNotif.Body)
	assert.Empty(t, bodyBytesNotif, "POST Notification body should be empty")

	// Test with Response
	respBody := createJsonRpcResponseBody(5, map[string]string{"status": "processed"}) // Response to server request ID 5
	respResp, err := makePostRequest(t, server.URL+transport.PATH, respBody, sessionIDHeader)
	require.NoError(t, err)
	defer respResp.Body.Close()
	assert.Equal(t, http.StatusAccepted, respResp.StatusCode, "POST Response status")
	bodyBytesResp, _ := io.ReadAll(respResp.Body)
	assert.Empty(t, bodyBytesResp, "POST Response body should be empty")
}

// Specification requirement: The body of the POST request MUST be one of the following: ... An array [batching] one or more _requests and/or notifications_. ...server MUST either return `Content-Type: text/event-stream`... or `Content-Type: application/json`...
func Test_SRV_25_HTTP_POS_05_PostBatchRequestNotificationReturnsResponse(t *testing.T) {
	// Scenario 1: JSON response
	t.Run("JSON Response", func(t *testing.T) {
		tp, _, _, server, cleanup := setupServerTest(t)
		defer cleanup()
		tp.NoStream2025 = true // Force JSON response

		// init session
		initBody := createJsonRpcRequestBody(1, "initialize", schema2025.InitializeRequestParams{ // Send initialize first
			ProtocolVersion: schema2025.PROTOCOL_VERSION,
			ClientInfo:      schema2025.Implementation{Name: "test-client", Version: "1.0"},
			Capabilities:    schema2025.ClientCapabilities{},
		})
		respInit, err := makePostRequest(t, server.URL+transport.PATH, initBody, nil)
		defer respInit.Body.Close() // Ensure body is closed eventually
		sessionID := respInit.Header.Get(transport.MCP_SESSION_HEADER)
		sessionIDHeader := map[string]string{transport.MCP_SESSION_HEADER: sessionID}

		req1 := createJsonRpcRequestBody(10, "ping", nil)
		notif1 := createJsonRpcNotificationBody("notify/1", nil)
		req2 := createJsonRpcRequestBody(11, "ping", nil)
		batchBody := createJsonRpcBatchRequestBody(req1, notif1, req2)

		resp, err := makePostRequest(t, server.URL+transport.PATH, batchBody, sessionIDHeader)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		batchResp := assertJsonRpcBatchResponse(t, resp.Body, 2) // Expect 2 responses for 2 requests

		// Basic check on response IDs
		var resp1, resp2 shared.JSONRPCResponse
		err = json.Unmarshal(batchResp[0], &resp1)
		require.NoError(t, err)
		err = json.Unmarshal(batchResp[1], &resp2)
		require.NoError(t, err)
		assert.Contains(t, []interface{}{resp1.ID.Value, resp2.ID.Value}, float64(10)) // Use uint64 because default JSON unmarshal is float64
		assert.Contains(t, []interface{}{resp1.ID.Value, resp2.ID.Value}, float64(11))
		assert.NotNil(t, resp1.Result)
		assert.NotNil(t, resp2.Result)
	})

	// Scenario 2: SSE response
	t.Run("SSE Response", func(t *testing.T) {
		tp, _, _, server, cleanup := setupServerTest(t)
		defer cleanup()
		tp.NoStream2025 = false // Force SSE response

		// init session
		initBody := createJsonRpcRequestBody(1, "initialize", schema2025.InitializeRequestParams{ // Send initialize first
			ProtocolVersion: schema2025.PROTOCOL_VERSION,
			ClientInfo:      schema2025.Implementation{Name: "test-client", Version: "1.0"},
			Capabilities:    schema2025.ClientCapabilities{},
		})
		respInit, _ := makePostRequest(t, server.URL+transport.PATH, initBody, nil)
		defer respInit.Body.Close() // Ensure body is closed eventually
		sessionID := respInit.Header.Get(transport.MCP_SESSION_HEADER)
		sessionIDHeader := map[string]string{transport.MCP_SESSION_HEADER: sessionID}

		req1 := createJsonRpcRequestBody(20, "ping", nil)
		notif1 := createJsonRpcNotificationBody("notify/2", nil)
		req2 := createJsonRpcRequestBody(21, "ping", nil)
		batchBody := createJsonRpcBatchRequestBody(req1, notif1, req2)

		resp, err := makePostRequest(t, server.URL+transport.PATH, batchBody, sessionIDHeader)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		// Read SSE stream for 2 responses
		sseReader := bufio.NewReader(resp.Body)
		receivedIDs := make(map[string]bool)
		responsesReceived := 0
		readTimeout := time.After(3 * time.Second)
	readLoop:
		for responsesReceived < 2 {
			select {
			case <-readTimeout:
				t.Fatalf("Timed out waiting for SSE responses for batch")
			default:
				event, data, _, errRead := readNextSseEvent(t, sseReader) // Use _ to ignore the id
				if errRead == io.EOF {
					break readLoop
				}
				require.NoError(t, errRead)
				if event == "message" {
					var receivedMsg shared.Message
					errUnmarshal := json.Unmarshal([]byte(data), &receivedMsg)
					require.NoError(t, errUnmarshal)
					if receivedMsg.ID != nil { // Only count responses
						idStr := receivedMsg.ID.String()
						require.Contains(t, []string{"20", "21"}, idStr)
						if !receivedIDs[idStr] {
							receivedIDs[idStr] = true
							responsesReceived++
							assert.Nil(t, receivedMsg.Error)
							assert.NotNil(t, receivedMsg.Result)
						}
					}
				}
			}
		}
		require.Equal(t, 2, responsesReceived, "Should have received 2 responses via SSE")
		// Check stream closure
		_, _, _, errRead := readNextSseEvent(t, sseReader)
		assert.ErrorIs(t, errRead, io.EOF, "Expected SSE stream to close after batch responses")
	})
}
