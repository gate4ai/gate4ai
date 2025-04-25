package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema" // For RequestID type
	"go.uber.org/zap"
)

// handleA2APOST processes POST requests on the A2A endpoint.
func (t *Transport) handleA2APOST(w http.ResponseWriter, r *http.Request, logger *zap.Logger) {
	// 1. Read Request Body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("Failed to read A2A request body", zap.Error(err))
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 2. Parse JSON-RPC Request
	// A2A JSON-RPC body SHOULD contain a single Request object.
	// A2A spec generally assumes single request per POST, but let's handle potential batch just in case.
	msgs, err := shared.ParseMessages(nil, bodyBytes)
	if err != nil || len(msgs) == 0 {
		logger.Error("Failed to parse A2A JSON-RPC request", zap.Error(err))
		sendA2AErrorResponse(w, nil, shared.JSONRPCErrorParseError, "Invalid JSON-RPC request", nil, logger)
		return
	}

	if len(msgs) > 1 {
		logger.Warn("Received A2A request with batch")
		// JSON-RPC spec says for batch errors, return batch response.
		// A2A implies single requests, so maybe return Invalid Request for batch? Let's error.
		sendA2AErrorResponse(w, nil, shared.JSONRPCErrorInvalidRequest, "A2A endpoint does not support batch requests", nil, logger)
		return
	}

	// For A2A, we process only one message
	msg := msgs[0]

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

	var sessionID string
	if method == "tasks/send" || method == "tasks/sendSubscribe" {
		var params a2aSchema.TaskSendParams
		if err := json.Unmarshal(*msg.Params, &params); err != nil {
			logger.Error("Failed to unmarshal tasks/send params", zap.Error(err))
			http.Error(w, "Failed to unmarshal tasks/send params", http.StatusInternalServerError)
			return
		}
		if params.SessionID != nil {
			sessionID = *params.SessionID
		}
	}

	session, err := t.getSession(r, sessionID, logger, true)
	if err != nil {
		logger.Error("Failed to get/create session for A2A request", zap.Error(err))
		http.Error(w, "Session creation failed", http.StatusInternalServerError)
		return
	}
	session.SetStatus(shared.StatusConnected)
	defer session.SetStatus(shared.StatusDisconnected)

	msg.Session = session // Associate session context
	msg.Timestamp = time.Now()

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

	responseChan, ok := session.AcquireOutput()
	if !ok {
		logger.Error("Failed to acquire output channel for A2A request", zap.String("method", method), zap.Any("reqID", msg.ID))
		sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInternal, "Failed to acquire output channel", nil, logger)
		return
	}
	defer session.ReleaseOutput()

	// 6. Handle Response / SSE Stream
	if isStreamingRequest {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// A2A SSE Stream Handling
		// The initial handler for tasks/sendSubscribe in A2ACapability will return the initial task state.
		flusher, ok := w.(http.Flusher)
		if !ok {
			logger.Error("Streaming unsupported (http.Flusher missing) for A2A SSE", zap.String("sessionID", session.GetID()))
			t.sessionManager.CloseSession(session.GetID()) // Clean up session
			// Cannot send HTTP error here as headers might be sent already.
			return
		}

		eventID := 0
		// Wait for the response or timeout
		for {
			select {
			case response, ok := <-responseChan:
				if !ok {
					logger.Info("A2A SSE output channel closed, ending stream", zap.String("sessionId", session.GetID()))
					return
				}
				if response.Error != nil {
					logger.Error("tasks/sendSubscribe handler returned an error immediately", zap.Error(response.Error), zap.Any("reqID", msg.ID))
					sendA2AErrorResponse(w, msg.ID, response.Error.Code, response.Error.Message, response.Error.Data, logger)
					return
				}

				eventID++
				data, err := json.Marshal(response)
				if err != nil {
					logger.Error("Failed to marshal A2A SSE event", zap.Error(err), zap.Any("reqID", msg.ID))
					sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInternal, "Failed to marshal A2A SSE event", nil, logger)
					return
				}
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", eventID, data)
				flusher.Flush()
				logger.Debug("Sent A2A SSE event", zap.String("eventData", string(*response.Result)))

				// Is final?
				var statusEvent a2aSchema.TaskStatusUpdateEvent
				err = json.Unmarshal(*response.Result, &statusEvent)
				if err == nil && statusEvent.Final {
					logger.Info("Final A2A event sent, closing SSE stream", zap.String("sessionId", session.GetID()))
					return // Exit loop, which closes the stream
				}
			case <-time.After(responseTimeout): // Use constant defined elsewhere
				logger.Error("Timeout waiting for initial response from tasks/sendSubscribe handler", zap.Any("reqID", msg.ID))
				sendA2AErrorResponse(w, msg.ID, shared.JSONRPCErrorInternal, "Timeout waiting for initial task response", nil, logger)
				// Cleanup the registered callback
				session.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(fmt.Errorf("timeout"))})
				return
			case <-r.Context().Done():
				logger.Debug("Client disconnected while waiting for initial tasks/sendSubscribe response", zap.Any("reqID", msg.ID))
				// Cleanup the registered callback
				session.GetRequestManager().ProcessResponse(&shared.Message{ID: msg.ID, Error: shared.NewJSONRPCError(fmt.Errorf("client disconnected"))})
				return
			}
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		// Regular A2A Request (tasks/send, tasks/get, tasks/cancel)
		// Wait for a single response from the output channel or timeout
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

// --- Response Helpers ---

func sendA2ASuccessResponse(w http.ResponseWriter, id *schema.RequestID, result *json.RawMessage, logger *zap.Logger) {
	// A2A uses standard JSON-RPC success response format
	resp := shared.JSONRPCResponse{
		JSONRPC: shared.JSONRPCVersion,
		ID:      id,
		Result:  result,
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
