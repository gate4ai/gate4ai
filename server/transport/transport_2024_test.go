package transport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared"
	schema2024 "github.com/gate4ai/gate4ai/shared/mcp/2024/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Specification requirement: The server MUST provide two endpoints: 1. An SSE endpoint... 2. A regular HTTP POST endpoint...
// Test: Implicitly tested by setting up the transport and making requests to the expected V2024 path (/sse).
func Test_SRV_24_SSE_POS_01_ProvidesSseAndPostEndpoints(t *testing.T) {
	_, _, _, server, cleanup := setupServerTest(t) // Use shared helper
	defer cleanup()
	// Test setup registers handlers. Making requests in other tests verifies existence.
	resp, err := http.Head(server.URL + transport.MCP2024_PATH) // Check if path exists
	require.NoError(t, err)
	defer resp.Body.Close()
	// StatusMethodNotAllowed is expected for HEAD on the GET/POST handler
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, "Expected /sse path to exist")
}

// Specification requirement: When a client connects, the server MUST send an `endpoint` event containing a URI for the client to use for sending messages.
func Test_SRV_24_SSE_POS_02_SendsEndpointEventOnSseConnect(t *testing.T) {
	_, _, _, server, cleanup := setupServerTest(t)
	defer cleanup()

	// Make GET request to V2024 SSE endpoint
	resp, err := makeSseGetRequest(t, server.URL+transport.MCP2024_PATH+"?key=valid-key", nil) // Use V2024 path
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	// Read first event
	reader := bufio.NewReader(resp.Body)
	event, data, _, err := readNextSseEvent(t, reader) // Use more robust reader, ignore ID
	require.NoError(t, err)
	assert.Equal(t, "endpoint", event, "First event should be 'endpoint'")
	require.NotEmpty(t, data, "Endpoint data (URI) should not be empty")
	assert.True(t, strings.HasPrefix(data, transport.MCP2024_PATH+"?"+transport.SESSION_ID_KEY2024+"="), "Endpoint URI format mismatch")
	t.Logf("Received endpoint event with URI: %s", data)
}

// Specification requirement: All subsequent client messages MUST be sent as HTTP POST requests to this endpoint. Server messages are sent as SSE `message` events...
func Test_SRV_24_SSE_POS_03_AcceptsPostAndSendsSseMessage(t *testing.T) {
	_, mockManager, _, server, cleanup := setupServerTest(t)
	defer cleanup()

	// 1. Connect SSE & Get Endpoint URI + Session ID
	sseResp, err := makeSseGetRequest(t, server.URL+transport.MCP2024_PATH+"?key=valid-key", nil)
	require.NoError(t, err)
	defer sseResp.Body.Close()
	require.Equal(t, http.StatusOK, sseResp.StatusCode)
	reader := bufio.NewReader(sseResp.Body)
	event, endpointData, err := readFirstSseEvent(t, sseResp.Body)
	require.NoError(t, err)
	require.Equal(t, "endpoint", event)
	require.NotEmpty(t, endpointData)
	parsedURL, _ := url.Parse(endpointData)
	sessionID := parsedURL.Query().Get(transport.SESSION_ID_KEY2024)
	require.NotEmpty(t, sessionID)
	t.Logf("Client connected, got sessionID: %s", sessionID)

	// Get the session object from the manager
	session, errGet := mockManager.GetSession(sessionID)
	require.NoError(t, errGet, "Session should exist in manager")
	require.NotNil(t, session, "Session retrieved from manager should not be nil")
	require.NotNil(t, session.Input(), "Session Input processor should not be nil")

	// 2. Send POST to the received endpoint path
	postURL := server.URL + endpointData // Includes path and session_id query
	requestBody := createJsonRpcRequestBody(1, "test/method", map[string]string{"data": "test"})
	postResp, err := makePostRequest(t, postURL, requestBody, map[string]string{"Content-Type": "application/json"})
	require.NoError(t, err)
	defer postResp.Body.Close()
	// V2024 handler returns 202 for POST
	require.Equal(t, http.StatusAccepted, postResp.StatusCode, "POST request should be accepted")

	// 3. Use a WaitGroup to ensure the goroutine completes before test ends
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		// Give server handler time to process POST and add message to session input
		time.Sleep(100 * time.Millisecond)

		session, errGet := mockManager.GetSession(sessionID)
		if errGet != nil {
			t.Logf("Error getting session in goroutine: %v", errGet)
			return
		}

		respResult := map[string]string{"status": "ok"}
		respID := schema2024.RequestID_FromUInt64(1)
		session.SendResponse(&respID, respResult, nil) // Send response back through the session channel
		t.Logf("Mock server sent response for ID 1 via session %s output", sessionID)
	}()

	// 4. Read SSE stream for the "message" event
	found := false
	for i := 0; i < 5; i++ { // Read a few events or timeout
		msgEvent, msgData, _, errRead := readNextSseEvent(t, reader) // Use _ to ignore the id
		if errRead == io.EOF {
			break
		}
		if errRead != nil {
			t.Logf("Error reading SSE: %v", errRead)
			break
		}

		if msgEvent == "message" {
			t.Logf("Received message event: %s", msgData)
			var receivedMsg shared.Message
			errUnmarshal := json.Unmarshal([]byte(msgData), &receivedMsg)
			if errUnmarshal != nil {
				t.Logf("Error unmarshaling message: %v", errUnmarshal)
				continue
			}

			if receivedMsg.ID != nil && receivedMsg.ID.String() == "1" { // Compare value correctly
				require.Nil(t, receivedMsg.Error, "Expected no error, got: %v", receivedMsg.Error)
				require.NotNil(t, receivedMsg.Result, "Expected non-nil result when error is nil")
				assert.Contains(t, string(*receivedMsg.Result), `"status":"ok"`)
				found = true
				break
			}
		} else {
			t.Logf("Received other SSE event: %s", msgEvent)
		}
	}

	// Wait for the goroutine to finish
	wg.Wait()

	require.True(t, found, "Did not receive expected 'message' event with ID 1")
}

// Specification requirement: ...the server operates as an independent process that can handle multiple client connections.
func Test_SRV_24_SSE_POS_04_HandlesMultipleClients(t *testing.T) {
	_, mockManager, _, server, cleanup := setupServerTest(t)
	defer cleanup()

	numClients := 3
	var wg sync.WaitGroup
	wg.Add(numClients)

	runClient := func(clientID int) {
		defer wg.Done() // Signal main WaitGroup completion

		// Use a separate WaitGroup for the internal goroutine
		var internalWg sync.WaitGroup
		internalWg.Add(1) // Add 1 for the internal goroutine

		// Create a context for this specific client run
		clientCtx, clientCancel := context.WithTimeout(context.Background(), 10*time.Second) // Add timeout for client run
		defer clientCancel()

		// 1. Connect SSE & Get Endpoint URI + Session ID
		sseResp, err := makeSseGetRequest(t, server.URL+transport.MCP2024_PATH+"?key=valid-key", nil)
		require.NoError(t, err, "Client %d SSE connect failed", clientID)
		defer sseResp.Body.Close()
		require.Equal(t, http.StatusOK, sseResp.StatusCode, "Client %d SSE status", clientID)
		reader := bufio.NewReader(sseResp.Body)

		// Read endpoint event with timeout
		var endpointData string
		endpointReadTimeout := time.After(2 * time.Second)
	endpointLoop:
		for {
			select {
			case <-endpointReadTimeout:
				t.Errorf("[Client %d] Timed out waiting for endpoint event", clientID)
				return // Exit runClient on timeout
			case <-clientCtx.Done():
				t.Errorf("[Client %d] Context cancelled while waiting for endpoint event", clientID)
				return // Exit runClient on cancellation
			default:
				event, data, _, errRead := readNextSseEvent(t, reader)
				if errRead != nil {
					require.NoError(t, errRead, "Client %d error reading initial SSE", clientID)
					return
				}
				if event == "endpoint" {
					endpointData = data
					break endpointLoop
				}
			}
		}

		require.NotEmpty(t, endpointData, "Client %d endpoint data missing", clientID)
		parsedURL, _ := url.Parse(endpointData)
		sessionID := parsedURL.Query().Get(transport.SESSION_ID_KEY2024)
		require.NotEmpty(t, sessionID, "Client %d session ID", clientID)
		t.Logf("[Client %d] Connected, got sessionID: %s", clientID, sessionID)

		// 2. Simulate Server Sending Response via Session Output (Unique for each client)
		go func() {
			defer internalWg.Done()                                       // Signal internal goroutine completion
			time.Sleep(time.Duration(100+clientID*20) * time.Millisecond) // Stagger

			// Check context before getting session
			select {
			case <-clientCtx.Done():
				t.Logf("[Client %d] Context cancelled before sending response", clientID)
				return
			default:
			}

			session, errGet := mockManager.GetSession(sessionID)
			// Use require.NoError within the test's main goroutine or handle error carefully here
			if errGet != nil {
				// Log error instead of panicking in the goroutine
				t.Errorf("[Client %d] Failed to get session in goroutine: %v", clientID, errGet)
				return // Don't proceed if session is gone
			}
			respID := schema2024.RequestID_FromUInt64(uint64(clientID + 100)) // Unique request ID
			session.SendResponse(&respID, map[string]int{"clientId": clientID}, nil)
			t.Logf("[Client %d] Server simulated sending response for ID %d via session %s output", clientID, clientID+100, sessionID)
		}()

		// 3. Send POST request
		postURL := server.URL + endpointData
		requestBody := createJsonRpcRequestBody(clientID+100, "test", nil)
		postReq, _ := http.NewRequestWithContext(clientCtx, "POST", postURL, strings.NewReader(requestBody))
		postReq.Header.Set("Content-Type", "application/json")
		postReq.Header.Set("Accept", "application/json, text/event-stream")
		httpClient := &http.Client{Timeout: 3 * time.Second}
		postResp, err := httpClient.Do(postReq)

		require.NoError(t, err, "Client %d POST failed", clientID)
		defer postResp.Body.Close()
		require.Equal(t, http.StatusAccepted, postResp.StatusCode, "Client %d POST status", clientID)
		t.Logf("[Client %d] POST request sent and accepted", clientID)

		// 4. Read SSE stream for the "message" event
		found := false
		readTimeout := time.After(5 * time.Second) // Increased timeout
	readLoop:
		for {
			select {
			case <-readTimeout:
				t.Errorf("[Client %d] Timed out waiting for SSE message", clientID)
				break readLoop
			case <-clientCtx.Done():
				t.Errorf("[Client %d] Context cancelled while waiting for SSE message", clientID)
				break readLoop
			default:
				// Non-blocking read attempt (or use select with a small timer)
				// Reading from SSE can block. Use select with context cancellation.
				msgEvent, msgData, _, errRead := readNextSseEvent(t, reader)
				if errRead == io.EOF {
					t.Logf("[Client %d] SSE stream closed unexpectedly", clientID)
					break readLoop
				}
				// Check context cancellation again after potential block
				select {
				case <-clientCtx.Done():
					t.Errorf("[Client %d] Context cancelled during SSE read", clientID)
					break readLoop
				default:
				}

				require.NoError(t, errRead, "Client %d error reading SSE", clientID)
				if msgEvent == "message" {
					var receivedMsg shared.Message
					errUnmarshal := json.Unmarshal([]byte(msgData), &receivedMsg)
					require.NoError(t, errUnmarshal, "Client %d bad JSON", clientID)
					// Compare the JSON string representation from ID.String() with the expected marshalled ID
					if receivedMsg.ID != nil && receivedMsg.ID.String() == strconv.Itoa(clientID+100) {
						var resultData map[string]float64 // JSON numbers default to float64
						errUnmarshal = json.Unmarshal(*receivedMsg.Result, &resultData)
						require.NoError(t, errUnmarshal, "Client %d bad result JSON", clientID)
						require.EqualValues(t, clientID, resultData["clientId"], "Client %d got wrong response data", clientID)
						t.Logf("[Client %d] Received correct SSE message", clientID)
						found = true
						break readLoop
					} else {
						t.Logf("[Client %d] Received message event with wrong/no ID: %s", clientID, msgData)
					}
				} else {
					t.Logf("[Client %d] Received other SSE event: %s", clientID, msgEvent)
				}
			}
		}

		// Wait for the internal goroutine to finish *before* declaring runClient done
		internalWg.Wait()
		t.Logf("[Client %d] Internal goroutine finished", clientID)

		require.True(t, found, "Client %d did not receive expected message event", clientID)
	}

	for i := 0; i < numClients; i++ {
		go runClient(i)
	}
	wg.Wait()
}

// Specification requirement: Sequence diagram shows "Client->>Server: Close SSE connection". Implicit requirement for clean server handling.
func Test_SRV_24_SSE_POS_05_HandlesClientSseDisconnect(t *testing.T) {
	_, mockManager, _, server, cleanup := setupServerTest(t)
	defer cleanup() // This will call CloseAllSessions eventually

	// Connect client
	sseResp, err := makeSseGetRequest(t, server.URL+transport.MCP2024_PATH+"?key=valid-key", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sseResp.StatusCode)

	// Get session ID
	reader := bufio.NewReader(sseResp.Body)
	event, endpointData, _, err := readNextSseEvent(t, reader) // Use more robust reader
	require.NoError(t, err)
	require.Equal(t, "endpoint", event)
	parsedURL, _ := url.Parse(endpointData)
	sessionID := parsedURL.Query().Get(transport.SESSION_ID_KEY2024)
	require.NotEmpty(t, sessionID)

	t.Logf("Client connected, obtained session ID: %s", sessionID)

	// Verify session exists
	_, err = mockManager.GetSession(sessionID)
	require.NoError(t, err, "Session should exist after connect")

	// Close the connection from the client side
	sseResp.Body.Close()
	t.Logf("Client closed connection for session %s", sessionID)

	// Check if the session is eventually removed (depends on server's disconnect handling)
	// Give it a bit of time. In a real scenario, the server's read loop would error out.
	removed := assert.Eventually(t, func() bool {
		_, errGet := mockManager.GetSession(sessionID)

		// Transport closes the session, check the mock manager's tracking
		mockManager.mu.RLock()
		closed := mockManager.ClosedSessions[sessionID]
		mockManager.mu.RUnlock()

		// Session manager's CloseSession is called when transport detects disconnect.
		t.Logf("Checking session %s: GetSession err=%v, Closed tracked=%v", sessionID, errGet, closed)
		return errGet != nil || closed
	}, 2*time.Second, 100*time.Millisecond, "Session should be closed or removed after client disconnect")

	if !removed {
		t.Errorf("Session %s was not closed/removed after client disconnect", sessionID)
	}
}

// Specification requirement: Server MUST provide an SSE endpoint. Test robustness against other methods.
func Test_SRV_24_SSE_NEG_01_RejectsNonSseMethodOnSseEndpoint(t *testing.T) {
	_, _, _, server, cleanup := setupServerTest(t)
	defer cleanup()

	resp, err := makeDeleteRequest(t, server.URL+transport.MCP2024_PATH, nil)

	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// Specification requirement: All subsequent client messages MUST be sent as HTTP POST requests to this endpoint. (Implies endpoint is tied to a connection).
func Test_SRV_24_SSE_NEG_04_PostToUriNotAssociatedWithSse(t *testing.T) {
	_, _, _, server, cleanup := setupServerTest(t)
	defer cleanup()

	// 1. Establish an SSE connection and keep it open
	sseResp, err := makeSseGetRequest(t, server.URL+transport.MCP2024_PATH+"?key=valid-key", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sseResp.StatusCode, "Failed to connect SSE")
	defer sseResp.Body.Close() // Ensure it's closed eventually

	// Read the endpoint event in a goroutine to avoid blocking
	endpointChan := make(chan string, 1)
	go func() {
		event, endpointData, err := readFirstSseEvent(t, sseResp.Body)
		if err != nil {
			t.Logf("Error reading SSE in goroutine: %v", err)
			close(endpointChan)
			return
		}
		if event == "endpoint" {
			endpointChan <- endpointData
		} else {
			t.Logf("Expected endpoint event, got %s", event)
			close(endpointChan)
		}
		// Keep reading to simulate active client, will block until connection closes or test finishes
		io.Copy(io.Discard, sseResp.Body)
	}()

	// Wait for the endpoint event
	endpointData, ok := <-endpointChan
	require.True(t, ok, "Did not receive endpoint event")
	require.NotEmpty(t, endpointData)

	parsedURL, _ := url.Parse(endpointData)
	validSessionID := parsedURL.Query().Get(transport.SESSION_ID_KEY2024)
	require.NotEmpty(t, validSessionID)

	t.Logf("Obtained valid session ID: %s", validSessionID)

	// 2. Try POSTing with an *invalid* session ID
	invalidPostURL := server.URL + transport.MCP2024_PATH + "?session_id=invalid-session-id" // Use V2024 path
	postRespInvalid, err := makePostRequest(t, invalidPostURL, createJsonRpcRequestBody(1, "test", nil), nil)
	require.NoError(t, err)
	defer postRespInvalid.Body.Close()
	// Expect 404 Not Found because the session manager won't find "invalid-session-id"
	assert.Equal(t, http.StatusNotFound, postRespInvalid.StatusCode, "POST with invalid session ID should be Not Found")

	// 3. Try POSTing with the *valid* session ID (while SSE is still connected) and extra params
	slightlyWrongPath := server.URL + transport.MCP2024_PATH + "?session_id=" + validSessionID + "&extra=stuff" // Use V2024 path
	postRespWrongPath, err := makePostRequest(t, slightlyWrongPath, createJsonRpcRequestBody(2, "test2", nil), nil)
	require.NoError(t, err)
	defer postRespWrongPath.Body.Close()
	// Now that the session is kept alive, this POST should be accepted.
	assert.Equal(t, http.StatusAccepted, postRespWrongPath.StatusCode, "POST with valid session ID but extra query params")
}

// Specification requirement: Test POST endpoint robustness. Implicit requirement to handle malformed data.
func Test_SRV_24_SSE_NEG_05_PostWithInvalidMcpMessage(t *testing.T) {
	_, _, _, server, cleanup := setupServerTest(t)
	defer cleanup()

	// 1. Establish an SSE connection and keep it open
	sseResp, err := makeSseGetRequest(t, server.URL+transport.MCP2024_PATH+"?key=valid-key", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sseResp.StatusCode, "Failed to connect SSE")
	defer sseResp.Body.Close() // Ensure connection remains open until test completion

	// Read the endpoint event in a goroutine to keep the connection active
	endpointChan := make(chan string, 1)
	go func() {
		event, endpointData, err := readFirstSseEvent(t, sseResp.Body)
		if err != nil {
			t.Logf("Error reading SSE in goroutine: %v", err)
			close(endpointChan)
			return
		}
		if event == "endpoint" {
			endpointChan <- endpointData
		} else {
			t.Logf("Expected endpoint event, got %s", event)
			close(endpointChan)
		}
		// Keep reading to simulate active client, will block until connection closes
		io.Copy(io.Discard, sseResp.Body)
	}()

	// Wait for the endpoint event
	endpointData, ok := <-endpointChan
	require.True(t, ok, "Did not receive endpoint event")
	require.NotEmpty(t, endpointData)

	parsedURL, _ := url.Parse(endpointData)
	sessionID := parsedURL.Query().Get(transport.SESSION_ID_KEY2024)
	require.NotEmpty(t, sessionID)

	// 2. Send POST with invalid JSON
	postURL := server.URL + endpointData
	postResp, err := makePostRequest(t, postURL, `{"invalid json`, nil)
	require.NoError(t, err)
	defer postResp.Body.Close()

	// V2024 POST handler logs the error and returns 202 Accepted. It doesn't return a JSON-RPC error on the POST itself.
	assert.Equal(t, http.StatusAccepted, postResp.StatusCode, "POST with invalid JSON should return 202 Accepted in V2024 handler")
	// Verification requires checking server logs (outside the scope of this HTTP test).
}
