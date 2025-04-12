package schema

// CancelTaskResponse represents a response to a 'tasks/cancel' request.
type CancelTaskResponse struct {
	JSONRPC string        `json:"jsonrpc"` // Always "2.0"
	Result  *Task         `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
	ID      any           `json:"id"` // Request ID
}

// GetTaskPushNotificationResponse represents a response to 'tasks/pushNotification/get'.
type GetTaskPushNotificationResponse struct {
	JSONRPC string                      `json:"jsonrpc"` // Always "2.0"
	Result  *TaskPushNotificationConfig `json:"result,omitempty"`
	Error   *JSONRPCError               `json:"error,omitempty"`
	ID      any                         `json:"id"` // Request ID
}

// GetTaskResponse represents a response to a 'tasks/get' request.
type GetTaskResponse struct {
	JSONRPC string        `json:"jsonrpc"` // Always "2.0"
	Result  *Task         `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
	ID      any           `json:"id"` // Request ID
}

// SendTaskResponse represents a response to a synchronous 'tasks/send' request.
type SendTaskResponse struct {
	JSONRPC string        `json:"jsonrpc"` // Always "2.0"
	Result  *Task         `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
	ID      any           `json:"id"` // Request ID
}

// SetTaskPushNotificationResponse represents a response to 'tasks/pushNotification/set'.
type SetTaskPushNotificationResponse struct {
	JSONRPC string                      `json:"jsonrpc"` // Always "2.0"
	Result  *TaskPushNotificationConfig `json:"result,omitempty"`
	Error   *JSONRPCError               `json:"error,omitempty"`
	ID      any                         `json:"id"` // Request ID
}

// Note on SendTaskStreamingResponse and TaskResubscriptionResponse:
// The JSON schema definition for SendTaskStreamingResponse includes TaskStatusUpdateEvent/TaskArtifactUpdateEvent
// directly in the `result` field. This is unusual for SSE, where events are sent as separate messages
// *after* an initial (often empty or simple status) HTTP response.
//
// For A2A streaming (tasks/sendSubscribe, tasks/resubscribe):
// 1. The client sends the JSON-RPC request via HTTP POST.
// 2. The server responds with an HTTP 200 OK and Content-Type: text/event-stream.
// 3. The server *then* sends `TaskStatusUpdateEvent` and `TaskArtifactUpdateEvent` messages over the SSE stream.
// 4. There isn't typically a single JSON-RPC *response* message that contains these events as its `result`.
//
// Therefore, we define the event types (`events.go`) which are the payloads sent over the stream,
// but we don't define a specific `SendTaskStreamingResponse` struct that tries to embed these events
// in its `result`, as that doesn't match the typical SSE flow. The initial HTTP response for SSE
// might be minimal or omitted in practice, depending on the server implementation.
