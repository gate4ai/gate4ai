package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gate4ai/mcp/shared"
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

const (
	// Timeout for waiting on responses
	responseTimeout = 5 * time.Second
)

// handlePOST processes POST requests on the unified MCP endpoint.
// It handles V2025 message posting according to the 2025-03-26 specification.
func (t *Transport) handlePOST(w http.ResponseWriter, r *http.Request, logger *zap.Logger) {
	session, err := t.getSession(w, r, logger, true)
	if err != nil {
		logger.Error("Failed to get session", zap.Error(err))
		sendJSONRPCErrorResponse(w, nil, shared.JSONRPCErrorParseError, "Failed to get session", nil, logger)
		return
	}

	// --- Process Message(s) ---
	bodyBytes, bodyErr := io.ReadAll(r.Body)
	if bodyErr != nil {
		logger.Error("Failed to read request body", zap.Error(bodyErr))
		sendJSONRPCErrorResponse(w, nil, shared.JSONRPCErrorParseError, "Failed to read request body", nil, logger)
		return
	}
	defer r.Body.Close()

	msgs, err := shared.ParseMessages(session, bodyBytes)
	if err != nil {
		logger.Error("Failed to parse JSON-RPC message(s)", zap.Error(err), zap.ByteString("body", bodyBytes))
		sendJSONRPCErrorResponse(w, nil, shared.JSONRPCErrorParseError, "Invalid JSON", err.Error(), logger)
		return
	}

	// Determine the type of the first message to handle initialization correctly
	isInitializeRequest := false
	if len(msgs) > 0 && msgs[0].Method != nil && *msgs[0].Method == "initialize" {
		isInitializeRequest = true
	}

	// Ensure session is connected for non-initialize requests
	if !isInitializeRequest &&
		session.GetStatus() != shared.StatusConnecting && // clients often do not send "notifications/initialized" before sending requests
		session.GetStatus() != shared.StatusConnected {
		logger.Warn("Received non-initialize request for non-connected session", zap.String("sessionId", session.GetID()), zap.Int("status", int(session.GetStatus())))
		sendJSONRPCErrorResponse(w, nil, shared.JSONRPCErrorInvalidRequest, "Session not initialized", nil, logger)
		return
	}

	// Check if client accepts SSE
	clientAcceptsSSE := false
	// V2025 Spec: Server *MUST* check Accept header for POST containing requests.
	// It *MAY* initiate SSE if text/event-stream is present.
	acceptHeader := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(acceptHeader, "text/event-stream") && !t.NoStream2025 {
		clientAcceptsSSE = true
	}

	// Determine message types in the batch
	hasError := false
	var requestIDs []*schema.RequestID // Store request IDs for potential use later
	for _, msg := range msgs {
		msg.Session = session
		msg.Timestamp = time.Now()

		// Check if this is a request (has ID and Method)
		if msg.Method != nil && msg.ID != nil && !msg.ID.IsEmpty() {
			requestIDs = append(requestIDs, msg.ID) // Store original request ID
		} // Don't store IDs for notifications or responses

		// Try to put the message in the input channel
		if handleErr := session.Input().Put(msg); handleErr != nil {
			logger.Error("Error handling message", zap.Error(handleErr), zap.String("sessionId", session.GetID()), zap.Any("msgId", msg.ID))
			hasError = true
			// Continue processing other messages
		}
	}

	// If the input consists solely of notifications or there are no messages expecting responses, return 202 Accepted
	if len(requestIDs) == 0 {
		w.WriteHeader(http.StatusAccepted)
		logger.Debug("POST processed, returning 202 Accepted", zap.String("sessionId", session.GetID()), zap.Int("messageCount", len(msgs)))
		return
	}

	// At this point, we have requests that expect responses

	// When there's an error but we need to respond to requests, we should still try to handle
	// the requests properly rather than just returning 202
	if hasError {
		// Log the error but continue processing
		logger.Warn("Some messages were not processed due to input channel being full", zap.String("sessionId", session.GetID()), zap.Int("messageCount", len(msgs)))
	}

	// Decide whether to respond with JSON or SSE
	if clientAcceptsSSE {
		t.responseToStream(w, r, session, logger, requestIDs) // Keep stream open
		logger.Info("SSE connection handler finished", zap.String("sessionId", session.GetID()))
	} else {
		t.responseAndCloseConnection(w, r, session, logger, requestIDs)
	}
}

// responseAndCloseConnection handles sending JSON response for V2025 POST requests.
func (t *Transport) responseAndCloseConnection(w http.ResponseWriter, r *http.Request, session shared.ISession, logger *zap.Logger, requestIDs []*schema.RequestID) {
	// Set necessary headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*") // Consider restricting this

	// Attach session ID header if available
	if session.GetID() != "" {
		w.Header().Set(MCP_SESSION_HEADER, session.GetID())
	}

	// Collect responses until all are received or timeout
	responses := make([]interface{}, 0)
	responseTimer := time.NewTimer(responseTimeout) // Use a timer for better control
	defer responseTimer.Stop()

	output, ok := session.AcquireOutput()
	if !ok {
		logger.Error("Failed to acquire output channel", zap.String("sessionId", session.GetID()))
	}
	defer session.ReleaseOutput()

	// Collect responses loop
collectLoop:
	for {
		select {
		case respMsg, ok := <-output:
			if !ok {
				logger.Info("Session output channel closed", zap.String("sessionId", session.GetID()))
				break collectLoop
			}

			if respMsg == nil {
				logger.Error("Received nil message from session output channel", zap.String("sessionId", session.GetID()))
				continue
			}

			if respMsg.Error != nil {
				// For error responses, add error response
				logger.Debug("Adding error response", zap.Any("msgId", respMsg.ID), zap.Error(respMsg.Error))
				responses = append(responses, shared.JSONRPCErrorResponse{
					JSONRPC: "2.0",
					ID:      respMsg.ID,
					Error:   respMsg.Error,
				})
			} else {
				// For successful responses, marshal Result to RawMessage for proper JSON handling
				var resultRaw json.RawMessage
				var err error

				if respMsg.Result != nil {
					resultRaw, err = json.Marshal(respMsg.Result)
				} else {
					// If result is nil, use "null" as the result value to ensure Result is non-nil
					resultRaw = json.RawMessage("null")
					err = nil
				}

				if err == nil {
					responses = append(responses, shared.JSONRPCResponse{
						JSONRPC: "2.0",
						ID:      respMsg.ID,
						Result:  &resultRaw, // Always use a non-nil Result pointer
					})
				} else {
					logger.Error("Failed to unmarshal response payload", zap.Error(err))
					// Create error response
					responses = append(responses, shared.JSONRPCErrorResponse{
						JSONRPC: "2.0",
						ID:      respMsg.ID,
						Error:   &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to unmarshal response payload"},
					})
				}
			}

			// If we have collected all the expected responses, we can send them immediately
			if len(responses) >= len(requestIDs) {
				break collectLoop
			}

		case <-responseTimer.C:
			logger.Warn("Timeout waiting for response(s)", zap.String("sessionId", session.GetID()))
			break collectLoop // Exit loop on timeout

		case <-r.Context().Done(): // Client disconnected while waiting
			logger.Warn("Client disconnected while waiting for response", zap.String("sessionId", session.GetID()))
			return // Stop processing
		}
	}

	// Send responses
	w.WriteHeader(http.StatusOK)

	// Check if it was a single request or a batch
	if len(requestIDs) == 1 && len(responses) == 1 {
		// Encode single response directly
		if err := json.NewEncoder(w).Encode(responses[0]); err != nil {
			logger.Error("Failed to encode single response", zap.Error(err))
		}
	} else {
		// Encode the batch (slice) of responses
		if err := json.NewEncoder(w).Encode(responses); err != nil {
			logger.Error("Failed to encode batch response", zap.Error(err))
		}
	}
}

// responseToStream handles streaming responses via SSE for V2025 POST requests.
func (t *Transport) responseToStream(w http.ResponseWriter, r *http.Request, session shared.ISession, logger *zap.Logger, requestIDs []*schema.RequestID) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Error("Streaming unsupported for SSE", zap.String("sessionId", session.GetID()))
		t.sessionManager.CloseSession(session.GetID()) // Clean up session
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	pendingRequests := make(map[string]bool)
	for _, id := range requestIDs {
		if id != nil {
			pendingRequests[id.String()] = true
		}
	}

	// Prepare SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*") // Consider restricting this
	w.Header().Set(MCP_SESSION_HEADER, session.GetID())
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second) // Keepalive ticker // TODO: Make configurable
	defer ticker.Stop()
	defer logger.Debug("Exiting responseToStream goroutine", zap.String("sessionId", session.GetID()))

	closeSSE := make(chan struct{})
	timeout := time.After(3 * time.Second) // Timeout after 3 seconds

	output, ok := session.AcquireOutput()
	if !ok {
		logger.Error("Failed to acquire output channel", zap.String("sessionId", session.GetID()))
	}
	defer session.ReleaseOutput()

	go func() {
		defer close(closeSSE)
		ctx := r.Context() // Store the context for checking cancellation

		eventID := time.Now().UnixNano() // Initial event ID for resumability
		for {
			select {
			case <-ctx.Done(): // Use the handler's context for cancellation
				logger.Info("responseToStream context cancelled", zap.String("sessionId", session.GetID()))
				return
			case <-timeout:
				logger.Warn("Timeout waiting for responses", zap.String("sessionId", session.GetID()))
				return
			case msg, ok := <-output:
				if !ok {
					logger.Info("Session output channel closed", zap.String("sessionId", session.GetID()))
					return
				}
				if msg == nil {
					logger.Error("Received nil message from session output channel", zap.String("sessionId", session.GetID()))
					continue
				}

				// Process the message based on ID
				if msg.ID != nil {
					// Check if this is expected response
					msgID := msg.ID.String()
					if _, expected := pendingRequests[msgID]; expected {
						// Format the response as JSON and send it as an SSE event
						var resp interface{}
						if msg.Error != nil {
							resp = shared.JSONRPCErrorResponse{
								JSONRPC: "2.0",
								ID:      msg.ID,
								Error:   msg.Error,
							}
						} else {
							// For successful responses, marshal Result to RawMessage for proper JSON handling
							var resultRaw json.RawMessage
							var err error

							if msg.Result != nil {
								resultRaw, err = json.Marshal(msg.Result)
							} else {
								// If result is nil, use "null" as the result value to ensure Result is non-nil
								resultRaw = json.RawMessage("null")
								err = nil
							}

							if err == nil {
								resp = shared.JSONRPCResponse{
									JSONRPC: "2.0",
									ID:      msg.ID,
									Result:  &resultRaw, // Always use a non-nil Result pointer
								}
							} else {
								logger.Error("Failed to unmarshal response payload", zap.Error(err))
								// Create error response
								resp = shared.JSONRPCErrorResponse{
									JSONRPC: "2.0",
									ID:      msg.ID,
									Error:   &shared.JSONRPCError{Code: shared.JSONRPCErrorInternal, Message: "Failed to unmarshal response payload"},
								}
							}
						}

						eventData, err := json.Marshal(resp)
						if err != nil {
							logger.Error("Failed to marshal SSE event data", zap.Error(err))
							continue
						}

						// Send event with incrementing ID for potential resumption
						fmt.Fprintf(w, "id: %d\ndata: %s\n\n", eventID, eventData)
						eventID++
						flusher.Flush()

						// Remove from pending requests
						delete(pendingRequests, msgID)
					}
				}

				// If all requests have been processed, close the connection
				if len(pendingRequests) == 0 {
					logger.Info("All requests processed, closing SSE connection", zap.String("sessionId", session.GetID()))

					return
				}
			case <-ticker.C:
				// Send keepalive ping event
				// Check context again before sending
				select {
				case <-ctx.Done():
					return
				default:
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sseEventPing, `{}`)
					flusher.Flush()
				}
			}
		}
	}()

	// Keep the handler alive while the goroutine runs.
	// The client disconnecting will cancel the request context.
	select {
	case <-r.Context().Done(): // Client disconnected
		logger.Info("Client disconnected (request context done)", zap.String("sessionId", session.GetID()))
	case <-closeSSE:
		logger.Info("SSE response goroutine finished", zap.String("sessionId", session.GetID()))
	}
	logger.Debug("responseToStream handler returning", zap.String("sessionId", session.GetID()))
}

// extractAuthKey tries to get the auth key from Header or Query params.
func (t *Transport) extractAuthKey(r *http.Request) string {
	// Try Authorization header first
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	// Fallback to query parameter
	return r.URL.Query().Get(AUTH_KEY2024)
}
