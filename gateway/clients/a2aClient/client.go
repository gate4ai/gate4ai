package a2aClient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema" // For RequestID type only
	"go.uber.org/zap"
)

// Client provides methods to interact with an A2A server.
type Client struct {
	baseURL           string // Base URL from Agent Card (or provided at creation)
	httpClient        *http.Client
	Headers           map[string]string
	logger            *zap.Logger
	requestManager    *shared.RequestManager
	agentInfo         *AgentInfo
	agentInfoMu       sync.RWMutex
	trustAgentInfoURL bool // Whether to trust and use the URL from AgentInfo
}

// New creates a new A2A client instance.
func New(baseURL string, options ...ClientOption) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL cannot be empty")
	}

	client := &Client{
		baseURL:           baseURL,
		httpClient:        http.DefaultClient,
		Headers:           make(map[string]string),
		logger:            zap.NewNop(),
		trustAgentInfoURL: true, // Trust by default for backward compatibility
	}

	// Apply provided options
	for _, option := range options {
		option(client)
	}

	// Initialize request manager after logger is set via options
	client.requestManager = shared.NewRequestManager(client.logger.Named("a2aClientRequests"))

	client.logger.Info("A2A client created")
	return client, nil
}

// FetchAgentInfo retrieves the AgentCard from the server and stores it.
func (c *Client) FetchAgentInfo(ctx context.Context) (*AgentInfo, error) {
	c.agentInfoMu.Lock()
	defer c.agentInfoMu.Unlock()

	info, err := FetchAgentCard(ctx, c.baseURL, c.httpClient, c.logger)
	if err != nil {
		c.logger.Error("Failed to fetch agent info", zap.Error(err))
		return nil, err
	}
	c.agentInfo = info

	// Update client's baseURL if the AgentCard specifies a different one and client is configured to trust it
	if c.trustAgentInfoURL && c.agentInfo.URL != c.baseURL {
		c.logger.Info("Updating client baseURL based on fetched AgentCard",
			zap.String("oldURL", c.baseURL),
			zap.String("newURL", c.agentInfo.URL))
		c.baseURL = c.agentInfo.URL
	}
	return c.agentInfo, nil
}

// GetAgentInfo returns the cached AgentInfo, fetching it if necessary.
func (c *Client) GetAgentInfo(ctx context.Context) (*AgentInfo, error) {
	c.agentInfoMu.RLock()
	if c.agentInfo != nil {
		info := *c.agentInfo // Return a copy
		c.agentInfoMu.RUnlock()
		return &info, nil
	}
	c.agentInfoMu.RUnlock()
	// Fetch if not cached
	return c.FetchAgentInfo(ctx)
}

// GetCachedAgentInfo returns the cached AgentInfo without fetching if not present.
func (c *Client) GetCachedAgentInfo() *AgentInfo {
	c.agentInfoMu.RLock()
	defer c.agentInfoMu.RUnlock()
	if c.agentInfo == nil {
		return nil
	}
	info := *c.agentInfo // Return a copy
	return &info
}

// _sendRequest performs a synchronous JSON-RPC POST request.
// It marshals params, sends the request, and unmarshals the JSON-RPC result into targetResponse.
// Handles basic JSON-RPC error responses.
func (c *Client) _sendRequest(ctx context.Context, method string, params interface{}, targetResponse interface{}) error {
	logger := c.logger.With(zap.String("method", method))

	// Use a helper from shared if available, otherwise implement here
	msgID := shared.RandomID() // Generate a unique ID for the request
	reqID := &schema.RequestID{Value: msgID}

	var paramsRaw *json.RawMessage
	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			logger.Error("Failed to marshal request params", zap.Error(err))
			return fmt.Errorf("failed to marshal params for %s: %w", method, err)
		}
		raw := json.RawMessage(paramsBytes)
		paramsRaw = &raw
	}

	rpcRequest := shared.JSONRPCMessage{
		JSONRPC: shared.JSONRPCVersion,
		ID:      reqID,
		Method:  &method,
		Params:  paramsRaw,
	}

	reqBytes, err := json.Marshal(rpcRequest)
	if err != nil {
		logger.Error("Failed to marshal JSON-RPC request", zap.Error(err))
		return fmt.Errorf("failed to marshal JSON-RPC request for %s: %w", method, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		logger.Error("Failed to create HTTP request", zap.Error(err))
		return fmt.Errorf("failed to create HTTP request for %s: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json") // Expect JSON response for sync calls
	for key, value := range c.Headers {
		httpReq.Header.Set(key, value)
	}

	logger.Debug("Sending synchronous request", zap.Any("reqID", reqID))
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		logger.Error("HTTP request failed", zap.Error(err))
		return fmt.Errorf("HTTP request for %s failed: %w", method, err)
	}
	defer httpResp.Body.Close()

	// Check for non-2xx status codes first
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		logger.Error("HTTP request returned non-success status", zap.Int("status", httpResp.StatusCode))
		// Attempt to read body for more details
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("HTTP error %d for %s: %s", httpResp.StatusCode, method, string(bodyBytes))
	}

	// Decode the JSON-RPC response
	var rpcResponse struct { // Temporary struct for decoding
		JSONRPC string               `json:"jsonrpc"`
		ID      *schema.RequestID    `json:"id"`
		Result  *json.RawMessage     `json:"result"`
		Error   *shared.JSONRPCError `json:"error"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&rpcResponse); err != nil {
		logger.Error("Failed to decode JSON-RPC response", zap.Error(err))
		return fmt.Errorf("failed to decode JSON-RPC response for %s: %w", method, err)
	}

	// Validate JSON-RPC fields
	if rpcResponse.JSONRPC != shared.JSONRPCVersion {
		return fmt.Errorf("invalid JSON-RPC version in response: %s", rpcResponse.JSONRPC)
	}
	if rpcResponse.ID == nil || rpcResponse.ID.String() != reqID.String() {
		// Allow nil ID only if the request ID couldn't be parsed by the server
		// This shouldn't happen often for successful requests.
		if rpcResponse.ID != nil {
			logger.Warn("JSON-RPC response ID mismatch", zap.Any("expectedID", reqID), zap.Any("receivedID", rpcResponse.ID))
			// Allow proceeding, but log it. Could be an error depending on strictness.
			// return fmt.Errorf("JSON-RPC response ID mismatch (expected %v, got %v)", reqID, rpcResponse.ID)
		} else if rpcResponse.Error == nil {
			// Successful response MUST have the original ID
			logger.Error("JSON-RPC successful response missing ID")
			return fmt.Errorf("JSON-RPC successful response missing ID")
		}
	}

	// Check for JSON-RPC level error
	if rpcResponse.Error != nil {
		logger.Error("Received JSON-RPC error", zap.Int("code", rpcResponse.Error.Code), zap.String("message", rpcResponse.Error.Message), zap.Any("data", rpcResponse.Error.Data))
		// Wrap the JSON-RPC error in a Go error
		return rpcResponse.Error
	}

	// Check if result is expected and non-nil
	if targetResponse == nil && rpcResponse.Result != nil && string(*rpcResponse.Result) != "null" {
		logger.Warn("Received unexpected result data when targetResponse was nil", zap.ByteString("result", *rpcResponse.Result))
	}
	if targetResponse != nil && rpcResponse.Result == nil {
		logger.Error("Expected result data, but received nil result")
		return fmt.Errorf("JSON-RPC response missing expected result for %s", method)
	}

	// Unmarshal the result into the target structure if provided
	if targetResponse != nil && rpcResponse.Result != nil {
		if err := json.Unmarshal(*rpcResponse.Result, targetResponse); err != nil {
			logger.Error("Failed to unmarshal result data", zap.Error(err), zap.ByteString("result", *rpcResponse.Result))
			return fmt.Errorf("failed to unmarshal result for %s: %w", method, err)
		}
	}

	logger.Debug("Synchronous request successful")
	return nil
}

// _handleStreamingRequest initiates a streaming request and returns a channel for events.
func (c *Client) _handleStreamingRequest(ctx context.Context, method string, params interface{}) (<-chan shared.A2AStreamEvent, error) {
	logger := c.logger.With(zap.String("method", method))

	msgID := shared.RandomID() // Generate a unique ID for the request
	reqID := &schema.RequestID{Value: msgID}

	var paramsRaw *json.RawMessage
	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			logger.Error("Failed to marshal streaming request params", zap.Error(err))
			return nil, fmt.Errorf("failed to marshal params for %s: %w", method, err)
		}
		raw := json.RawMessage(paramsBytes)
		paramsRaw = &raw
	}

	rpcRequest := shared.JSONRPCMessage{
		JSONRPC: shared.JSONRPCVersion,
		ID:      reqID,
		Method:  &method,
		Params:  paramsRaw,
	}

	reqBytes, err := json.Marshal(rpcRequest)
	if err != nil {
		logger.Error("Failed to marshal JSON-RPC streaming request", zap.Error(err))
		return nil, fmt.Errorf("failed to marshal JSON-RPC request for %s: %w", method, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		logger.Error("Failed to create HTTP request for streaming", zap.Error(err))
		return nil, fmt.Errorf("failed to create HTTP request for %s: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream") // Crucial for SSE
	for key, value := range c.Headers {
		httpReq.Header.Set(key, value)
	}

	logger.Debug("Sending streaming request", zap.Any("reqID", reqID))
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		logger.Error("HTTP streaming request failed", zap.Error(err))
		return nil, fmt.Errorf("HTTP request for %s failed: %w", method, err)
	}
	// Do NOT defer httpResp.Body.Close() here, the _processSSEStream goroutine needs it.

	// --- Validate Response for SSE ---
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(httpResp.Body) // Try to read error body
		httpResp.Body.Close()                     // Close body after reading
		logger.Error("HTTP streaming request returned non-success status", zap.Int("status", httpResp.StatusCode))
		return nil, fmt.Errorf("HTTP error %d for %s: %s", httpResp.StatusCode, method, string(bodyBytes))
	}

	contentType := httpResp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		httpResp.Body.Close() // Close body as we are not processing it
		logger.Error("Invalid Content-Type for streaming response", zap.String("contentType", contentType))
		return nil, fmt.Errorf("expected Content-Type 'text/event-stream', got '%s'", contentType)
	}

	// --- Start SSE Processing Goroutine ---
	eventChan := make(chan shared.A2AStreamEvent, 10) // Buffered channel
	go c._processSSEStream(ctx, httpResp, reqID, eventChan)

	logger.Debug("SSE stream initiated", zap.Any("reqID", reqID))
	return eventChan, nil
}

// _processSSEStream reads the SSE stream and sends parsed events to the channel.
func (c *Client) _processSSEStream(ctx context.Context, resp *http.Response, reqID *schema.RequestID, eventChan chan<- shared.A2AStreamEvent) {
	logger := c.logger.With(zap.String("operation", "processSSEStream"), zap.Any("reqID", reqID))
	defer close(eventChan)  // Ensure channel is closed when done
	defer resp.Body.Close() // Ensure response body is closed

	// Use bufio.Scanner for manual line-by-line parsing
	scanner := bufio.NewScanner(resp.Body)
	var currentEvent shared.A2AStreamEvent
	var dataBuffer bytes.Buffer

	for scanner.Scan() {
		line := scanner.Text()
		logger.Debug("SSE line received", zap.String("line", line))

		if line == "" { // End of event
			if dataBuffer.Len() > 0 {
				// Parse the accumulated data
				var rawResult json.RawMessage // Expecting the "result" field of JSONRPCResponse
				var rpcResponse struct {      // Temp struct to get the result part
					Result *json.RawMessage     `json:"result"`
					Error  *shared.JSONRPCError `json:"error"`
					// Ignoring jsonrpc, id for events per A2A interpretation
				}

				dataBytes := dataBuffer.Bytes()
				if err := json.Unmarshal(dataBytes, &rpcResponse); err != nil {
					logger.Error("Failed to unmarshal SSE data object", zap.Error(err), zap.ByteString("data", dataBytes))
					currentEvent.Error = fmt.Errorf("failed to parse SSE data: %w", err)
					select {
					case eventChan <- currentEvent:
					case <-ctx.Done():
						logger.Info("Context cancelled while sending SSE parse error")
						return
					}
					// Reset buffer and continue to next event
					dataBuffer.Reset()
					currentEvent = shared.A2AStreamEvent{} // Reset for next event
					continue
				}

				// Check for JSON-RPC error within the event payload
				if rpcResponse.Error != nil {
					logger.Error("Received JSON-RPC error via SSE", zap.Int("code", rpcResponse.Error.Code), zap.String("message", rpcResponse.Error.Message))
					currentEvent.Error = rpcResponse.Error
					select {
					case eventChan <- currentEvent:
					case <-ctx.Done():
						logger.Info("Context cancelled while sending SSE error event")
						return
					}
					// Reset buffer and continue to next event
					dataBuffer.Reset()
					currentEvent = shared.A2AStreamEvent{}
					continue
				}

				if rpcResponse.Result == nil {
					logger.Warn("SSE data object missing 'result' field", zap.ByteString("data", dataBytes))
					// Reset buffer and continue to next event
					dataBuffer.Reset()
					currentEvent = shared.A2AStreamEvent{}
					continue
				}

				rawResult = *rpcResponse.Result

				// Try unmarshalling as TaskStatusUpdateEvent
				var statusEvent a2aSchema.TaskStatusUpdateEvent
				if err := json.Unmarshal(rawResult, &statusEvent); err == nil && statusEvent.Status.State != "" {
					logger.Debug("Parsed TaskStatusUpdateEvent", zap.String("taskID", statusEvent.ID), zap.String("state", string(statusEvent.Status.State)), zap.Bool("final", statusEvent.Final))
					currentEvent.Type = "status"
					currentEvent.Status = &statusEvent
					currentEvent.Final = statusEvent.Final // Propagate final flag
				} else {
					// Try unmarshalling as TaskArtifactUpdateEvent
					var artifactEvent a2aSchema.TaskArtifactUpdateEvent
					if err := json.Unmarshal(rawResult, &artifactEvent); err == nil && len(artifactEvent.Artifact.Parts) > 0 {
						logger.Debug("Parsed TaskArtifactUpdateEvent", zap.String("taskID", artifactEvent.ID), zap.Int("partCount", len(artifactEvent.Artifact.Parts)), zap.Int("index", artifactEvent.Artifact.Index))
						currentEvent.Type = "artifact"
						currentEvent.Artifact = &artifactEvent
						// Artifact updates themselves don't mark the stream end usually
					} else {
						logger.Warn("Failed to parse SSE data as known A2A event type", zap.ByteString("resultData", rawResult))
						// Reset buffer and continue to next event
						dataBuffer.Reset()
						currentEvent = shared.A2AStreamEvent{}
						continue // Skip unknown event types
					}
				}

				// Send the parsed event
				select {
				case eventChan <- currentEvent:
				case <-ctx.Done():
					logger.Info("Context cancelled while sending parsed SSE event")
					return
				}

				// Reset for next event
				dataBuffer.Reset()
				currentEvent = shared.A2AStreamEvent{} // Reset for next event

				// Check if the stream should end based on the 'final' flag of a status update
				if currentEvent.Type == "status" && currentEvent.Final {
					logger.Info("Final event received, closing SSE processing")
					return
				}

			} // End if dataBuffer has data
			continue // Move to next line after processing event
		} // End if line is empty (end of event)

		// Process field lines (id, event, data, retry)
		if strings.HasPrefix(line, "data:") {
			// Append data line (strip prefix and space)
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data) // Trim leading/trailing space
			dataBuffer.WriteString(data)   // Append data line
			// Note: SSE spec allows multi-line data, bufio.Scanner splits by newline.
			// For multi-line data, the server sends multiple `data:` lines.
			// We accumulate them in dataBuffer until an empty line.
		} else if strings.HasPrefix(line, "event:") {
			// Handle event type if needed, though A2A uses result structure primarily
			eventName := strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			logger.Debug("Received event name field", zap.String("eventName", eventName))
		} else if strings.HasPrefix(line, "id:") {
			// Handle event ID if needed
			eventIDStr := strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			logger.Debug("Received event ID field", zap.String("eventID", eventIDStr))
		} else if strings.HasPrefix(line, "retry:") {
			// Handle retry field if needed
		} else {
			// Ignore unknown fields or comments (starting with :)
			logger.Debug("Ignoring SSE line", zap.String("line", line))
		}

	} // End for scanner.Scan()

	if err := scanner.Err(); err != nil {
		logger.Error("Error reading SSE stream", zap.Error(err))
		// Send error event if channel is still open
		select {
		case eventChan <- shared.A2AStreamEvent{Error: fmt.Errorf("SSE stream read error: %w", err)}:
		case <-ctx.Done():
			logger.Info("Context cancelled while sending SSE read error")
		default:
			logger.Warn("Event channel already closed when trying to send SSE read error")
		}
	} else {
		logger.Info("SSE stream closed (EOF)")
	}
}

// SendTaskSubscribe sends a task and subscribes to streaming updates via SSE.
func (c *Client) SendTaskSubscribe(ctx context.Context, params a2aSchema.TaskSendParams) (<-chan shared.A2AStreamEvent, error) {
	return c._handleStreamingRequest(ctx, "tasks/sendSubscribe", params)
}

// ResubscribeTask attempts to resume streaming updates for a task.
func (c *Client) ResubscribeTask(ctx context.Context, params a2aSchema.TaskQueryParams) (<-chan shared.A2AStreamEvent, error) {
	// Note: A2A resubscribe might require additional logic like sending Last-Event-ID header,
	// which isn't directly supported by the current approach easily.
	// A custom HTTP request might be needed to set that header if the server supports it.
	// For now, it behaves like sendSubscribe.
	c.logger.Warn("ResubscribeTask currently acts like SendTaskSubscribe; Last-Event-ID not implemented.")
	return c._handleStreamingRequest(ctx, "tasks/resubscribe", params)
}

// SendTask sends a task for synchronous processing.
func (c *Client) SendTask(ctx context.Context, params a2aSchema.TaskSendParams) (*a2aSchema.Task, error) {
	var responseTask a2aSchema.Task
	err := c._sendRequest(ctx, "tasks/send", params, &responseTask)
	if err != nil {
		return nil, err
	}
	return &responseTask, nil
}

// GetTask retrieves the current state of a task.
func (c *Client) GetTask(ctx context.Context, params a2aSchema.TaskQueryParams) (*a2aSchema.Task, error) {
	var responseTask a2aSchema.Task
	err := c._sendRequest(ctx, "tasks/get", params, &responseTask)
	if err != nil {
		return nil, err
	}
	return &responseTask, nil
}

// CancelTask requests cancellation of a running task.
func (c *Client) CancelTask(ctx context.Context, params a2aSchema.TaskIdParams) (*a2aSchema.Task, error) {
	var responseTask a2aSchema.Task
	// Target response might be nil if cancel returns no body on success? Check schema/spec.
	// A2A spec implies it returns the updated Task object.
	err := c._sendRequest(ctx, "tasks/cancel", params, &responseTask)
	if err != nil {
		// Handle specific A2A errors like TaskNotCancelableError
		var jsonRpcErr *shared.JSONRPCError
		if errors.As(err, &jsonRpcErr) {
			if jsonRpcErr.Code == a2aSchema.ErrorCodeTaskNotCancelable {
				// Optionally return a more specific Go error type or just the JSONRPCError
				return nil, err // Return the original JSONRPCError
			}
		}
		return nil, err // Return other errors
	}
	return &responseTask, nil
}

// SetPushNotification sets or updates push notification config for a task.
func (c *Client) SetPushNotification(ctx context.Context, params a2aSchema.TaskPushNotificationConfig) (*a2aSchema.TaskPushNotificationConfig, error) {
	var responseConfig a2aSchema.TaskPushNotificationConfig
	err := c._sendRequest(ctx, "tasks/pushNotification/set", params, &responseConfig)
	if err != nil {
		// Handle specific A2A errors like PushNotificationNotSupportedError
		var jsonRpcErr *shared.JSONRPCError
		if errors.As(err, &jsonRpcErr) {
			if jsonRpcErr.Code == a2aSchema.ErrorCodePushNotificationNotSupported {
				return nil, err // Return the original JSONRPCError
			}
		}
		return nil, err // Return other errors
	}
	return &responseConfig, nil
}

// GetPushNotification retrieves the push notification config for a task.
func (c *Client) GetPushNotification(ctx context.Context, params a2aSchema.TaskIdParams) (*a2aSchema.TaskPushNotificationConfig, error) {
	var responseConfig a2aSchema.TaskPushNotificationConfig
	err := c._sendRequest(ctx, "tasks/pushNotification/get", params, &responseConfig)
	if err != nil {
		// Handle specific A2A errors like TaskNotFound or PushNotificationNotSupportedError
		var jsonRpcErr *shared.JSONRPCError
		if errors.As(err, &jsonRpcErr) {
			if jsonRpcErr.Code == a2aSchema.ErrorCodeTaskNotFound || jsonRpcErr.Code == a2aSchema.ErrorCodePushNotificationNotSupported {
				return nil, err // Return the original JSONRPCError
			}
		}
		return nil, err
	}
	// Handle case where config might not be set (server returns null result?)
	// The current implementation assumes targetResponse will receive data or an error occurs.
	// If null result is valid, _sendRequest might need adjustment or check here.
	return &responseConfig, nil
}
