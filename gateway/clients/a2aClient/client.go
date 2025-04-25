package a2aClient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	baseURL           string
	httpClient        *http.Client
	logger            *zap.Logger
	requestManager    *shared.RequestManager
	agentInfo         *AgentInfo
	agentInfoMu       sync.RWMutex
	trustAgentInfoURL bool
	headers           map[string]string
}

// New creates a new A2A client instance.
func New(baseURL string, options ...ClientOption) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL cannot be empty")
	}
	client := &Client{
		baseURL:           baseURL,
		httpClient:        http.DefaultClient,
		headers:           make(map[string]string),
		logger:            zap.NewNop(),
		trustAgentInfoURL: true,
	}
	for _, option := range options {
		option(client)
	}
	client.requestManager = shared.NewRequestManager(client.logger.Named("a2aClientRequests"))
	client.logger.Info("A2A client created")
	return client, nil
}

// FetchAgentInfo retrieves the AgentCard from the server and stores it.
func (c *Client) FetchAgentInfo(ctx context.Context) (*AgentInfo, error) {
	c.agentInfoMu.Lock()
	defer c.agentInfoMu.Unlock()
	info, err := FetchAgentCard(ctx, c.baseURL, c.httpClient, c.logger) // FetchAgentCard remains same
	if err != nil {
		c.logger.Error("Failed to fetch agent info", zap.Error(err))
		return nil, err
	}
	c.agentInfo = info
	if c.trustAgentInfoURL && c.agentInfo.URL != c.baseURL {
		c.logger.Info("Updating client baseURL from AgentCard", zap.String("newURL", c.agentInfo.URL))
		c.baseURL = c.agentInfo.URL
	}
	return c.agentInfo, nil
}

// GetAgentInfo returns the cached AgentInfo, fetching it if necessary.
func (c *Client) GetAgentInfo(ctx context.Context) (*AgentInfo, error) {
	c.agentInfoMu.RLock()
	info := c.agentInfo
	c.agentInfoMu.RUnlock()
	if info != nil {
		infoCopy := *info
		return &infoCopy, nil
	}
	return c.FetchAgentInfo(ctx)
}

// GetCachedAgentInfo returns the cached AgentInfo without fetching.
func (c *Client) GetCachedAgentInfo() *AgentInfo {
	c.agentInfoMu.RLock()
	defer c.agentInfoMu.RUnlock()
	if c.agentInfo == nil {
		return nil
	}
	info := *c.agentInfo
	return &info
}

// _sendRequest performs a synchronous JSON-RPC POST request, now accepting headers.
func (c *Client) _sendRequest(ctx context.Context, method string, params interface{}, targetResponse interface{}) error {
	logger := c.logger.With(zap.String("method", method))
	msgID := shared.RandomID()
	reqID := &schema.RequestID{Value: msgID}
	var paramsRaw *json.RawMessage
	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params for %s: %w", method, err)
		}
		raw := json.RawMessage(paramsBytes)
		paramsRaw = &raw
	}
	rpcRequest := shared.JSONRPCMessage{JSONRPC: shared.JSONRPCVersion, ID: reqID, Method: &method, Params: paramsRaw}
	reqBytes, err := json.Marshal(rpcRequest)
	if err != nil {
		return fmt.Errorf("marshal JSON-RPC request for %s: %w", method, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return fmt.Errorf("create HTTP request for %s: %w", method, err)
	}

	// Set headers passed to the function
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for key, value := range c.headers {
		httpReq.Header.Set(key, value)
	}

	logger.Debug("Sending synchronous A2A request", zap.Any("reqID", reqID), zap.Int("headerCount", len(c.headers)))
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("HTTP request for %s failed: %w", method, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("HTTP error %d for %s: %s", httpResp.StatusCode, method, string(bodyBytes))
	}

	var rpcResponse struct { // Standard JSON-RPC parsing logic
		JSONRPC string               `json:"jsonrpc"`
		ID      *schema.RequestID    `json:"id"`
		Result  *json.RawMessage     `json:"result"`
		Error   *shared.JSONRPCError `json:"error"`
	}
	if err := json.NewDecoder(httpResp.Body).Decode(&rpcResponse); err != nil {
		return fmt.Errorf("decode JSON-RPC response for %s: %w", method, err)
	}
	if rpcResponse.JSONRPC != shared.JSONRPCVersion {
		return fmt.Errorf("invalid JSON-RPC version: %s", rpcResponse.JSONRPC)
	}
	if rpcResponse.ID == nil || rpcResponse.ID.String() != reqID.String() {
		if rpcResponse.ID != nil {
			logger.Warn("JSON-RPC response ID mismatch", zap.Any("expected", reqID), zap.Any("received", rpcResponse.ID))
		} else if rpcResponse.Error == nil {
			return fmt.Errorf("JSON-RPC successful response missing ID")
		}
	}
	if rpcResponse.Error != nil {
		return rpcResponse.Error
	}
	if targetResponse != nil && rpcResponse.Result == nil {
		return fmt.Errorf("JSON-RPC response missing expected result for %s", method)
	}
	if targetResponse != nil && rpcResponse.Result != nil {
		if err := json.Unmarshal(*rpcResponse.Result, targetResponse); err != nil {
			return fmt.Errorf("unmarshal result for %s: %w", method, err)
		}
	}
	logger.Debug("Synchronous A2A request successful")
	return nil
}

// _handleStreamingRequest initiates a streaming request, now accepting headers.
func (c *Client) _handleStreamingRequest(ctx context.Context, method string, params interface{}) (<-chan shared.A2AStreamEvent, error) {
	logger := c.logger.With(zap.String("method", method))
	msgID := shared.RandomID()
	reqID := &schema.RequestID{Value: msgID}
	var paramsRaw *json.RawMessage
	if params != nil {
		paramsBytes, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal streaming params for %s: %w", method, err)
		}
		raw := json.RawMessage(paramsBytes)
		paramsRaw = &raw
	}
	rpcRequest := shared.JSONRPCMessage{JSONRPC: shared.JSONRPCVersion, ID: reqID, Method: &method, Params: paramsRaw}
	reqBytes, err := json.Marshal(rpcRequest)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON-RPC streaming request for %s: %w", method, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("create HTTP streaming request for %s: %w", method, err)
	}

	// Set headers passed to the function
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream") // Crucial for SSE
	for key, value := range c.headers {
		httpReq.Header.Set(key, value)
	}

	logger.Debug("Sending streaming A2A request", zap.Any("reqID", reqID), zap.Int("headerCount", len(c.headers)))
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP streaming request for %s failed: %w", method, err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, fmt.Errorf("HTTP streaming error %d for %s: %s", httpResp.StatusCode, method, string(bodyBytes))
	}
	contentType := httpResp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		httpResp.Body.Close()
		return nil, fmt.Errorf("expected Content-Type 'text/event-stream', got '%s'", contentType)
	}

	eventChan := make(chan shared.A2AStreamEvent, 10)
	go c._processSSEStream(ctx, httpResp, reqID, eventChan) // SSE processing logic remains the same
	logger.Debug("SSE stream initiated", zap.Any("reqID", reqID))
	return eventChan, nil
}

// _processSSEStream remains the same as before.
func (c *Client) _processSSEStream(ctx context.Context, resp *http.Response, reqID *schema.RequestID, eventChan chan<- shared.A2AStreamEvent) {
	logger := c.logger.With(zap.String("operation", "processSSEStream"), zap.Any("reqID", reqID))
	defer close(eventChan)
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	var dataBuffer bytes.Buffer
	for scanner.Scan() {
		line := scanner.Text()
		//logger.Debug("SSE line received", zap.String("line", line)) // Reduce logging
		if line == "" {
			if dataBuffer.Len() > 0 {
				var rpcResponse struct {
					Result *json.RawMessage     `json:"result"`
					Error  *shared.JSONRPCError `json:"error"`
				}
				dataBytes := dataBuffer.Bytes()
				if err := json.Unmarshal(dataBytes, &rpcResponse); err != nil {
					logger.Error("Failed to unmarshal SSE data", zap.Error(err))
					select {
					case eventChan <- shared.A2AStreamEvent{Error: fmt.Errorf("parse SSE data: %w", err)}:
					case <-ctx.Done():
						return
					}
					dataBuffer.Reset()
					continue
				}
				if rpcResponse.Error != nil {
					logger.Error("Received JSON-RPC error via SSE", zap.Any("error", rpcResponse.Error))
					select {
					case eventChan <- shared.A2AStreamEvent{Error: rpcResponse.Error, Final: true}:
					case <-ctx.Done():
						return
					} // Assume SSE error is final
					dataBuffer.Reset()
					return // Stop processing on error event
				}
				if rpcResponse.Result == nil {
					logger.Warn("SSE data missing 'result'", zap.ByteString("data", dataBytes))
					dataBuffer.Reset()
					continue
				}

				rawResult := *rpcResponse.Result
				currentEvent := shared.A2AStreamEvent{}
				var statusEvent a2aSchema.TaskStatusUpdateEvent
				if err := json.Unmarshal(rawResult, &statusEvent); err == nil && statusEvent.Status.State != "" {
					currentEvent.Type = "status"
					currentEvent.Status = &statusEvent
					currentEvent.Final = statusEvent.Final
				} else {
					var artifactEvent a2aSchema.TaskArtifactUpdateEvent
					if err := json.Unmarshal(rawResult, &artifactEvent); err == nil && len(artifactEvent.Artifact.Parts) > 0 {
						currentEvent.Type = "artifact"
						currentEvent.Artifact = &artifactEvent
					} else {
						logger.Warn("Failed to parse SSE data as known type", zap.ByteString("result", rawResult))
						dataBuffer.Reset()
						continue
					}
				}
				select {
				case eventChan <- currentEvent:
				case <-ctx.Done():
					logger.Info("Ctx cancelled sending SSE event")
					return
				}
				dataBuffer.Reset()
				if currentEvent.Type == "status" && currentEvent.Final {
					logger.Info("Final event received, closing SSE processing")
					return
				}
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataBuffer.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// Ignore other SSE fields (event:, id:, retry:) for simplicity
	}
	if err := scanner.Err(); err != nil {
		logger.Error("Error reading SSE stream", zap.Error(err))
		select {
		case eventChan <- shared.A2AStreamEvent{Error: fmt.Errorf("SSE read error: %w", err)}:
		case <-ctx.Done():
		default:
		}
	} else {
		logger.Info("SSE stream closed (EOF)")
	}
}

// --- Public Methods Updated to Accept Headers ---

// SendTaskSubscribe sends a task and subscribes to streaming updates via SSE.
func (c *Client) SendTaskSubscribe(ctx context.Context, params a2aSchema.TaskSendParams) (<-chan shared.A2AStreamEvent, error) {
	return c._handleStreamingRequest(ctx, "tasks/sendSubscribe", params)
}

// ResubscribeTask attempts to resume streaming updates for a task.
func (c *Client) ResubscribeTask(ctx context.Context, params a2aSchema.TaskQueryParams) (<-chan shared.A2AStreamEvent, error) {
	c.logger.Warn("ResubscribeTask acts like SendTaskSubscribe; Last-Event-ID not implemented.")
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
	err := c._sendRequest(ctx, "tasks/cancel", params, &responseTask)
	if err != nil {
		return nil, err
	}
	return &responseTask, nil
}

// SetPushNotification sets or updates push notification config for a task.
func (c *Client) SetPushNotification(ctx context.Context, params a2aSchema.TaskPushNotificationConfig) (*a2aSchema.TaskPushNotificationConfig, error) {
	var responseConfig a2aSchema.TaskPushNotificationConfig
	err := c._sendRequest(ctx, "tasks/pushNotification/set", params, &responseConfig)
	if err != nil {
		return nil, err
	}
	return &responseConfig, nil
}

// GetPushNotification retrieves the push notification config for a task.
func (c *Client) GetPushNotification(ctx context.Context, params a2aSchema.TaskIdParams) (*a2aSchema.TaskPushNotificationConfig, error) {
	var responseConfig a2aSchema.TaskPushNotificationConfig
	err := c._sendRequest(ctx, "tasks/pushNotification/get", params, &responseConfig)
	if err != nil {
		return nil, err
	}
	return &responseConfig, nil
}
