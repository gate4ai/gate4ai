package transport_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gate4ai/gate4ai/server/mcp/capability"
	"github.com/gate4ai/gate4ai/server/transport"
	"github.com/gate4ai/gate4ai/shared"
	"github.com/gate4ai/gate4ai/shared/config"
	schema2025 "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- Mock Implementations ---

var _ transport.ISessionManager = &MockMCPManager{}

type MockMCPManager struct {
	mu       sync.RWMutex
	sessions map[string]transport.IDownstreamSession
	Cfg      config.IConfig
	Logger   *zap.Logger
	// Control fields for testing
	ReturnSessionError error
	ClosedSessions     map[string]bool
	NotificationsSent  []NotificationInfo
	ServerInfo         schema2025.Implementation
	capabilities       []shared.IServerCapability
}

type NotificationInfo struct {
	Method string
	Params map[string]interface{}
}

func NewMockMCPManager(cfg config.IConfig, logger *zap.Logger) *MockMCPManager {
	serverName, _ := cfg.ServerName()
	serverVersion, _ := cfg.ServerVersion()

	return &MockMCPManager{
		sessions:          make(map[string]transport.IDownstreamSession),
		Cfg:               cfg,
		Logger:            logger,
		ClosedSessions:    make(map[string]bool),
		NotificationsSent: make([]NotificationInfo, 0),
		ServerInfo: schema2025.Implementation{
			Name:    serverName,
			Version: serverVersion,
		},
		capabilities: make([]shared.IServerCapability, 0),
	}
}

type MockTestCapability struct{}

func (m *MockTestCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return map[string]func(*shared.Message) (interface{}, error){
		"test/method": func(msg *shared.Message) (interface{}, error) {
			return map[string]string{"status": "ok"}, nil
		},
		// Echo handler for test - returns client ID derived from requestID
		"test": func(msg *shared.Message) (interface{}, error) {
			// ID is 100 more than client ID in multiclient test
			var clientID int
			switch v := msg.ID.Value.(type) {
			case float64:
				clientID = int(v) - 100
			case int:
				clientID = v - 100
			case uint64:
				// Handle potential overflow if v > max int
				if v <= uint64(^uint(0)>>1)+100 {
					clientID = int(v) - 100
				} else {
					msg.Session.GetLogger().Warn("Request ID uint64 too large to convert to int",
						zap.Uint64("requestID", v))
					return map[string]string{"status": "error request ID too large"}, nil
				}
			case string:
				// Request ID is JSON encoded, remove quotes if present
				cleanID := v
				if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
					cleanID = v[1 : len(v)-1]
				}
				idNum, err := strconv.Atoi(cleanID)

				if err == nil {
					clientID = idNum - 100
				} else {
					msg.Session.GetLogger().Warn("Failed to parse numerical request ID from string",
						zap.String("requestID", v),
						zap.Error(err))
					return map[string]string{"status": "error parsing string ID"}, nil
				}
			default:
				msg.Session.GetLogger().Warn("Unhandled request ID type",
					zap.Any("type", fmt.Sprintf("%T", v)),
					zap.Any("id", v))
				return map[string]string{"status": "error unhandled ID type"}, nil
			}
			return map[string]int{"clientId": clientID}, nil
		},
	}
}

// Implement IServerCapability (empty method is fine for this mock)
func (m *MockTestCapability) SetCapabilities(s *schema2025.ServerCapabilities) {}

func (m *MockMCPManager) CreateSession(userID string, id string, params *sync.Map) shared.ISession {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Use a simplified mock session or BaseSession for testing
	// For testing transport, BaseSession is often sufficient.
	session := transport.NewSession(m, id, userID, shared.NewInput(m.Logger), params)
	m.sessions[session.ID] = session
	m.Logger.Debug("MockManager: Created session", zap.String("sessionID", session.ID), zap.String("userID", userID))
	session.Input().AddServerCapability(m.capabilities...)
	go session.Input().Process()
	time.Sleep(10 * time.Millisecond)
	m.Logger.Debug("Started input processor for session", zap.String("sessionID", session.ID))

	return session
}

func (m *MockMCPManager) GetSession(id string) (shared.ISession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.ReturnSessionError != nil {
		return nil, m.ReturnSessionError
	}
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	m.Logger.Debug("MockManager: Retrieved session", zap.String("sessionID", id))
	return s, nil
}

func (m *MockMCPManager) CloseSession(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Close() // Close the underlying BaseSession
		delete(m.sessions, id)
		m.ClosedSessions[id] = true // Track closed sessions
		m.Logger.Debug("MockManager: Closed session", zap.String("sessionID", id))
	}
}
func (m *MockMCPManager) CloseAllSessions() {
	m.mu.RLock() // Lock for reading IDs
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock() // Unlock before closing individually
	for _, id := range ids {
		m.CloseSession(id)
	}
	m.Logger.Info("MockManager: Closed all sessions")
}
func (m *MockMCPManager) NotifyEligibleSessions(method string, params map[string]any) {
	m.mu.Lock() // Lock for writing to NotificationsSent
	defer m.mu.Unlock()
	m.NotificationsSent = append(m.NotificationsSent, NotificationInfo{Method: method, Params: params})
	m.Logger.Debug("MockManager: NotifyEligibleSessions called", zap.String("method", method), zap.Any("params", params))
	// In a real test, you might iterate sessions and check eligibility
}
func (m *MockMCPManager) CleanupIdleSessions(timeout time.Duration) {
	m.Logger.Debug("MockManager: CleanupIdleSessions called", zap.Duration("timeout", timeout))
	// Simulate cleanup logic if needed for specific tests
}
func (m *MockMCPManager) AddValidator(validators ...shared.MessageValidator) {
	m.Logger.Debug("MockManager: AddValidator called")
}

// GetServerInfo returns information about the server implementation
func (m *MockMCPManager) GetServerInfo() *schema2025.Implementation {
	return &m.ServerInfo
}

// AddCapability adds server capabilities to the manager
func (m *MockMCPManager) AddCapability(capabilities ...shared.IServerCapability) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capabilities = append(m.capabilities, capabilities...)
	m.Logger.Debug("MockManager: AddCapability called", zap.Int("total_capabilities", len(m.capabilities)))
}

// MockAuthenticator implements transport.AuthenticationManager
type MockAuthenticator struct {
	Users        map[string]string // Map authKey -> userID
	AllowAnon    bool
	ReturnError  error
	SessionParam map[string]interface{} // Params to add on successful auth
}

func (a *MockAuthenticator) Authenticate(authKey string, remoteAddr string) (string, *sync.Map, error) {
	if a.ReturnError != nil {
		return "", nil, a.ReturnError
	}
	if userID, ok := a.Users[authKey]; ok && authKey != "" {
		params := &sync.Map{}
		if a.SessionParam != nil {
			for k, v := range a.SessionParam {
				params.Store(k, v)
			}
		}
		transport.SaveAuthKey(params, authKey)
		transport.SaveUserId(params, userID)
		return userID, params, nil
	}
	if a.AllowAnon && authKey == "" {
		params := &sync.Map{}
		if a.SessionParam != nil {
			for k, v := range a.SessionParam {
				params.Store(k, v)
			}
		}
		transport.SaveAuthKey(params, "")
		transport.SaveUserId(params, "anonymous_user") // Assign a default anonymous ID
		return "anonymous_user", params, nil
	}
	return "", nil, fmt.Errorf("unauthorized")
}

// --- Test Setup Helper ---

func setupServerTest(t *testing.T) (*transport.Transport, *MockMCPManager, *config.InternalConfig, *httptest.Server, func()) {
	t.Helper()
	logger, _ := zap.NewDevelopment() // Or NewNop() for less output
	cfg := config.NewInternalConfig()
	cfg.ServerNameValue = "TestServer" // Set a specific name for testing
	cfg.ServerVersionValue = "1.2.3"

	mockManager := NewMockMCPManager(cfg, logger)

	tp, err := transport.New(mockManager, logger, cfg)
	require.NoError(t, err)

	mockAuth := &MockAuthenticator{
		Users: map[string]string{
			"valid-key":   "test-user", // Used by many V2024 tests
			"key1":        "user1",
			"key-no-sse":  "user-no-sse",
			"another-key": "another-user", // Add other keys if needed by tests
		},
		AllowAnon: true, // Allow anonymous access for some tests
	}
	tp.SetAuthManager(mockAuth)

	baseCap := capability.NewBase(logger, mockManager)
	testCap := &MockTestCapability{}
	mockManager.AddCapability(baseCap, testCap)

	mux := http.NewServeMux()
	tp.RegisterMCPHandlers(mux)
	server := httptest.NewServer(mux)

	// Update config with actual server URL (mainly for logging/debugging in tests)
	cfg.ServerAddress = server.URL

	cleanup := func() {
		server.Close()
		mockManager.CloseAllSessions() // Ensure all sessions are cleaned up
		t.Log("Test server and manager cleaned up.")
	}

	return tp, mockManager, cfg, server, cleanup
}

func (m *MockMCPManager) GetLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

// --- Client Interaction Helpers ---

// Helper to make SSE GET request and return response/error
func makeSseGetRequest(t *testing.T, url string, headers map[string]string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 3 * time.Second} // Use a timeout
	return client.Do(req)
}

// Helper to make POST request
func makePostRequest(t *testing.T, url string, body string, headers map[string]string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest("POST", url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	return client.Do(req)
}

// Helper to make DELETE request
func makeDeleteRequest(t *testing.T, url string, headers map[string]string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest("DELETE", url, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	return client.Do(req)
}

// Helper to read *first* SSE event (basic implementation)
func readFirstSseEvent(t *testing.T, body io.Reader) (event, data string, err error) {
	t.Helper()
	reader := bufio.NewReader(body)
	var inEvent bool = false

	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			if readErr == io.EOF && data != "" { // EOF after data means end of event
				err = nil
				return
			}
			err = fmt.Errorf("reading SSE: %w", readErr)
			return
		}

		line = strings.TrimSpace(line)

		if line == "" { // End of event
			if inEvent { // Return only if we've collected a complete event
				return
			}
			continue // Skip empty lines if we're not in an event yet
		}

		// We found a non-empty line, we're in an event
		inEvent = true

		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
}

// readNextSseEvent reads the *next* complete SSE event from the reader.
// Returns event type, data, id, and error. Handles multi-line data.
func readNextSseEvent(t *testing.T, reader *bufio.Reader) (event, data, id string, err error) {
	t.Helper()
	var dataBuilder strings.Builder
	event = "message" // Default event type

	for {
		lineBytes, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF && dataBuilder.Len() > 0 { // EOF after some data means end of last event
				return event, dataBuilder.String(), id, nil
			}
			return event, dataBuilder.String(), id, err // Return error or EOF if no data pending
		}
		line := string(lineBytes)

		if isPrefix {
			// Line too long, need to handle this more robustly if expected
			t.Logf("Warning: Encountered long line in SSE stream, potential data loss")
			continue // Skip potentially incomplete prefix
		}

		if line == "" { // Blank line signifies end of event
			if dataBuilder.Len() > 0 { // Return event if we collected data
				return event, dataBuilder.String(), id, nil
			}
			// If no data, reset and wait for next event (ignore empty events)
			event = "message"
			id = ""
			dataBuilder.Reset()
			continue
		}

		if strings.HasPrefix(line, ":") { // Ignore comments
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			// Invalid line format, ignore? Log?
			// t.Logf("Warning: Ignoring invalid SSE line: %s", line)
			continue
		}
		field := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch field {
		case "event":
			event = value
		case "data":
			if dataBuilder.Len() > 0 {
				dataBuilder.WriteString("\n") // Add newline for multi-line data
			}
			dataBuilder.WriteString(value)
		case "id":
			id = value
		case "retry":
			// Handle retry if needed
		default:
			// Ignore unknown fields
		}
	}
}

// Helper to read *all* SSE events until timeout or EOF
func readAllSseEvents(t *testing.T, body io.ReadCloser, timeout time.Duration) []map[string]string {
	t.Helper()
	defer body.Close()
	reader := bufio.NewReader(body)
	events := make([]map[string]string, 0)
	currentEvent := make(map[string]string)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	readDone := make(chan struct{})

	go func() {
		defer close(readDone)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					t.Logf("Error reading SSE stream: %v", err)
				}
				if len(currentEvent) > 0 { // Add last event if any
					events = append(events, currentEvent)
				}
				return
			}
			line = strings.TrimSpace(line)
			if line == "" { // End of event
				if len(currentEvent) > 0 {
					events = append(events, currentEvent)
					currentEvent = make(map[string]string)
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue
			} // Comment

			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				field := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				currentEvent[field] = value
			}
		}
	}()

	select {
	case <-readDone:
	case <-timer.C:
		t.Logf("Timed out reading all SSE events after %v", timeout)
	}

	return events
}

// Helper to create a standard JSON-RPC request body
func createJsonRpcRequestBody(id interface{}, method string, params interface{}) string {
	var rawParams *json.RawMessage
	if params != nil {
		pBytes, err := json.Marshal(params)
		if err == nil {
			raw := json.RawMessage(pBytes)
			rawParams = &raw
		}
	}
	req := shared.JSONRPCMessage{
		JSONRPC: shared.JSONRPCVersion,
		ID:      &schema2025.RequestID{Value: id}, // Use V2025 RequestID
		Method:  &method,
		Params:  rawParams,
	}
	reqBytes, _ := json.Marshal(req)
	return string(reqBytes)
}

// Helper to create a standard JSON-RPC notification body
func createJsonRpcNotificationBody(method string, params interface{}) string {
	var rawParams *json.RawMessage
	if params != nil {
		pBytes, err := json.Marshal(params)
		if err == nil {
			raw := json.RawMessage(pBytes)
			rawParams = &raw
		}
	}
	req := shared.JSONRPCNotification{ // Use Notification struct
		JSONRPC: shared.JSONRPCVersion,
		Method:  &method,
		Params:  rawParams,
	}
	reqBytes, _ := json.Marshal(req)
	return string(reqBytes)
}

// Helper to create a standard JSON-RPC response body
func createJsonRpcResponseBody(id interface{}, result interface{}) string {
	var rawResult *json.RawMessage
	if result != nil {
		pBytes, err := json.Marshal(result)
		if err == nil {
			raw := json.RawMessage(pBytes)
			rawResult = &raw
		}
	}
	req := shared.JSONRPCResponse{ // Use Response struct
		JSONRPC: shared.JSONRPCVersion,
		ID:      &schema2025.RequestID{Value: id},
		Result:  rawResult,
	}
	reqBytes, _ := json.Marshal(req)
	return string(reqBytes)
}

// Helper to create a batch request body
func createJsonRpcBatchRequestBody(messages ...string) string {
	rawMessages := make([]json.RawMessage, len(messages))
	for i, msg := range messages {
		rawMessages[i] = json.RawMessage(msg)
	}
	batchBytes, _ := json.Marshal(rawMessages)
	return string(batchBytes)
}

// Helper to check response body for JSON-RPC error
func assertJsonRpcError(t *testing.T, body io.Reader, expectedCode int, expectedMessagePart string) {
	t.Helper()
	bodyBytes, err := io.ReadAll(body)
	require.NoError(t, err)

	var errResp shared.JSONRPCErrorResponse
	err = json.Unmarshal(bodyBytes, &errResp)
	require.NoError(t, err, "Response body is not a valid JSON-RPC Error Response: %s", string(bodyBytes))
	require.NotNil(t, errResp.Error, "JSON-RPC response does not contain an error object")
	assert.Equal(t, expectedCode, errResp.Error.Code, "JSON-RPC error code mismatch")
	if expectedMessagePart != "" {
		assert.Contains(t, errResp.Error.Message, expectedMessagePart, "JSON-RPC error message mismatch")
	}
}

// Helper to check response body for JSON-RPC success result
func assertJsonRpcSuccess(t *testing.T, body io.Reader, expectedID interface{}) json.RawMessage {
	t.Helper()
	bodyBytes, err := io.ReadAll(body)
	require.NoError(t, err)

	var resp shared.JSONRPCResponse
	err = json.Unmarshal(bodyBytes, &resp)
	require.NoError(t, err, "Response body is not a valid JSON-RPC Success Response: %s", string(bodyBytes))
	// JSONRPCResponse doesn't have Error field, so we can't check resp.Error here
	require.NotNil(t, resp.Result, "Expected success result to have a 'result' field")
	if expectedID != nil {
		require.NotNil(t, resp.ID, "Expected response ID to be non-nil")
		require.Equal(t, expectedID, resp.ID.Value, "Response ID does not match request ID")
	} else {
		require.Nil(t, resp.ID, "Expected response ID to be nil for notification-only errors, but got one")
	}
	return *resp.Result
}

// Helper to check response body for a JSON-RPC Batch Response
func assertJsonRpcBatchResponse(t *testing.T, body io.Reader, expectedCount int) []json.RawMessage {
	t.Helper()
	bodyBytes, err := io.ReadAll(body)
	require.NoError(t, err)

	var batchResp []json.RawMessage
	err = json.Unmarshal(bodyBytes, &batchResp)
	require.NoError(t, err, "Response body is not a valid JSON Array (Batch Response): %s", string(bodyBytes))
	require.Len(t, batchResp, expectedCount, "Batch response count mismatch")
	return batchResp
}
