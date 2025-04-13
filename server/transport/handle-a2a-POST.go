package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gate4ai/mcp/shared"
	"github.com/gate4ai/mcp/shared/mcp/2025/schema" // For RequestID type
	"go.uber.org/zap"
)

// handleA2APOST processes POST requests on the A2A endpoint.
func (t *Transport) handleA2APOST(w http.ResponseWriter, r *http.Request, logger *zap.Logger) {
	// 1. Get or Create Session Context (Authentication happens here)
	// For A2A, creating a session per request might be okay if auth is per-request.
	// Or, we might need to correlate based on headers if a persistent connection concept applies.
	// Let's use getSession(allowCreate=true) for now, assuming auth handles A2A schemes.
	session, err := t.getSession(w, r, logger, true)
	if err != nil {
		logger.Error("Failed to get/create session for A2A POST", zap.Error(err))
		// getSession already wrote error response
		return
	}
	// Note: 'session' here might represent the authenticated context rather than a persistent MCP session.

	// 2. Read Request Body
	bodyBytes, bodyErr := io.ReadAll(r.Body)
	if bodyErr != nil {
		logger.Error("Failed to read A2A request body", zap.Error(bodyErr))
		sendA2AErrorResponse(w, nil, shared.JSONRPCErrorParseError, "Failed to read request body", nil, logger)
		return
	}
	defer r.Body.Close()

	// 3. Parse JSON-RPC Request(s)
	// A2A spec generally assumes single request per POST, but let's handle potential batch just in case.
	msgs, err := shared.ParseMessages(session, bodyBytes)
	if err != nil || len(msgs) == 0 {
		logger.Error("Failed to parse A2A JSON-RPC message(s)", zap.Error(err), zap.ByteString("body", bodyBytes))
		sendA2AErrorResponse(w, nil, shared.JSONRPCErrorParseError, "Invalid JSON", err.Error(), logger)
		return
	}

	// For A2A, we process only the first message in the batch if multiple are sent.
	msg := msgs[0]
	msg.Session = session // Associate session context
	msg.Timestamp = time.Now()

	if msg.Method == nil {
		sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInvalidRequest, "Method is required", nil, logger)
		return
	}
	method := *msg.Method

	// Check if method is A2A task related
	if !strings.HasPrefix(method, "tasks/") {
		sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorMethodNotFound, fmt.Sprintf("Method '%s' not supported on A2A endpoint", method), nil, logger)
		return
	}

	// 4. Handle A2A Streaming Request (`tasks/sendSubscribe`)
	isStreamingRequest := method == "tasks/sendSubscribe"
	clientAcceptsSSE := false
	acceptHeader := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(acceptHeader, "text/event-stream") {
		clientAcceptsSSE = true
	}

	if isStreamingRequest && !clientAcceptsSSE {
		sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInvalidRequest, "tasks/sendSubscribe requires 'Accept: text/event-stream' header", nil, logger)
		return
	}

	// 5. Put message into Input processor
	if handleErr := session.Input().Put(msg); handleErr != nil {
		logger.Error("Error putting A2A message into input queue", zap.Error(handleErr), zap.String("method", method), zap.Any("msgId", msg.ID))
		// Send Internal Error back
		sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInternal, "Internal server error processing request", handleErr.Error(), logger)
		return
	}

	// 6. Handle Response / SSE Stream
	if isStreamingRequest {
		// A2A SSE Stream Handling
		// The initial handler for tasks/sendSubscribe in A2ACapability will return the initial task state.
		// We need to wait for *that specific* response from the output channel.
		// Then, we start the SSE stream using streamA2AResponse.

		// Wait for the initial task response from the handler
		initialResponseChan := make(chan *shared.Message, 1)
		callback := func(respMsg *shared.Message) {
			select {
			case initialResponseChan <- respMsg:
			default:
				logger.Warn("Initial response channel full or closed for tasks/sendSubscribe", zap.String("taskID", msg.ID.String()))
			}
		}
		session.GetRequestManager().RegisterRequest(msg.ID, callback)

		// Wait for the response or timeout
		select {
		case initialResponse := <-initialResponseChan:
			if initialResponse.Error != nil {
				logger.Error("tasks/sendSubscribe handler returned an error immediately", zap.Error(initialResponse.Error), zap.Any("reqID", msg.ID))
				sendA2AErrorResponse(w, msg.ID, initialResponse.Error.Code, initialResponse.Error.Message, initialResponse.Error.Data, logger)
				return
			}
			// Got initial response (likely Task object), now start streaming
			logger.Info("Starting A2A SSE stream", zap.String("sessionID", session.GetID()), zap.Any("reqID", msg.ID))
			t.streamA2AResponse(w, r, session, logger) // This function takes over the response writer
			logger.Info("A2A SSE stream handler finished", zap.String("sessionID", session.GetID()))

		case <-time.After(responseTimeout): // Use constant defined elsewhere
			logger.Error("Timeout waiting for initial response from tasks/sendSubscribe handler", zap.Any("reqID", msg.ID))
			sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInternal, "Timeout waiting for initial task response", nil, logger)
			// Cleanup the registered callback
			session.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(fmt.Errorf("timeout"))})

		case <-r.Context().Done():
			logger.Warn("Client disconnected while waiting for initial tasks/sendSubscribe response", zap.Any("reqID", msg.ID))
			// Cleanup the registered callback
			session.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(fmt.Errorf("client disconnected"))})
		}

	} else {
		// Regular A2A Request (tasks/send, tasks/get, tasks/cancel)
		// Wait for a single response from the output channel

		responseChan := make(chan *shared.Message, 1)
		callback := func(respMsg *shared.Message) {
			select {
			case responseChan <- respMsg:
			default:
				logger.Warn("Response channel full or closed for A2A request", zap.String("method", method), zap.Any("reqID", msg.ID))
			}
		}
		session.GetRequestManager().RegisterRequest(msg.ID, callback)

		// Wait for the response or timeout
		select {
		case response := <-responseChan:
			if response.Error != nil {
				logger.Error("A2A handler returned an error", zap.Error(response.Error), zap.String("method", method), zap.Any("reqID", msg.ID))
				sendA2AErrorResponse(w, msg.ID, response.Error.Code, response.Error.Message, response.Error.Data, logger)
			} else {
				logger.Debug("Sending successful A2A response", zap.String("method", method), zap.Any("reqID", msg.ID))
				sendA2ASuccessResponse(w, msg.ID, response.Result, logger)
			}

		case <-time.After(responseTimeout):
			logger.Error("Timeout waiting for A2A response", zap.String("method", method), zap.Any("reqID", msg.ID))
			sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInternal, "Timeout waiting for response", nil, logger)
			// Cleanup the registered callback
			session.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(fmt.Errorf("timeout"))})

		case <-r.Context().Done():
			logger.Warn("Client disconnected while waiting for A2A response", zap.String("method", method), zap.Any("reqID", msg.ID))
			// Cleanup the registered callback
			session.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(fmt.Errorf("client disconnected"))})
		}
	}
}

// --- A2A Specific Response Helpers ---

func sendA2ASuccessResponse(w http.ResponseWriter, id *schema.RequestID, result *json.RawMessage, logger *zap.Logger) {
	// A2A uses standard JSON-RPC success response format
	resp := shared.JSONRPCResponse{
		JSONRPC: shared.JSONRPCVersion,
		ID:      id,
		Result:  result, // Already marshalled json.RawMessage
	}
	logger.Debug("Sending A2A Success", zap.Any("reqID", id))
	sendJSONResponse(w, http.StatusOK, resp, logger) // Reuse existing helper
}

func sendA2AErrorResponse(w http.ResponseWriter, id *schema.RequestID, code int, message string, data interface{}, logger *zap.Logger) {
	// A2A uses standard JSON-RPC error response format
	jsonRpcError := &shared.JSONRPCError{
		Code:    code,
		Message: message,
		Data:    data,
	}
	// Use existing helper
	sendJSONRPCErrorResponse(w, id, jsonRpcError.Code, jsonRpcError.Message, jsonRpcError.Data, logger)
}
