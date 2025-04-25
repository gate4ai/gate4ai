package schema

// --- Request Parameter Structures ---

// TaskIdParams provides the task ID for operations like cancel or get push config.
type TaskIdParams struct {
	// The unique identifier of the task. (Required)
	ID string `json:"id"`
	// Optional metadata for the request context.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// TaskQueryParams provides parameters for retrieving task state, including optional history.
type TaskQueryParams struct {
	// The unique identifier of the task. (Required)
	ID string `json:"id"`
	// Optional: Maximum number of historical messages to retrieve for the task.
	// If omitted/negative, no history. If 0, empty history array.
	HistoryLength *int `json:"historyLength,omitempty"`
	// Optional metadata for the request context.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// TaskSendParams provides parameters for sending a message to initiate or continue a task.
type TaskSendParams struct {
	// The unique identifier of the task. Client SHOULD generate a unique ID (e.g., UUID) for new tasks. (Required)
	ID string `json:"id"`
	// Optional identifier to group related tasks into a session. Server generates if omitted for new tasks.
	SessionID *string `json:"sessionId,omitempty"`
	// The message content being sent. (Required)
	Message Message `json:"message"`
	// Optional: Configuration for push notifications for this task.
	PushNotification *PushNotificationConfig `json:"pushNotification,omitempty"`
	// Optional: Maximum number of historical messages to retrieve in the response/stream updates.
	HistoryLength *int `json:"historyLength,omitempty"`
	// Optional metadata for the request context.
	Metadata *map[string]interface{} `json:"metadata,omitempty"`
}

// --- Concrete Request Structures ---
// These represent the top-level JSON-RPC object for each method.

// CancelTaskRequest represents a 'tasks/cancel' JSON-RPC request.
type CancelTaskRequest struct {
	JSONRPC string       `json:"jsonrpc"` // Always "2.0"
	Method  string       `json:"method"`  // Always "tasks/cancel"
	Params  TaskIdParams `json:"params"`
	ID      any          `json:"id"` // Request ID
}

// GetTaskPushNotificationRequest represents a 'tasks/pushNotification/get' JSON-RPC request.
type GetTaskPushNotificationRequest struct {
	JSONRPC string       `json:"jsonrpc"` // Always "2.0"
	Method  string       `json:"method"`  // Always "tasks/pushNotification/get"
	Params  TaskIdParams `json:"params"`
	ID      any          `json:"id"` // Request ID
}

// GetTaskRequest represents a 'tasks/get' JSON-RPC request.
type GetTaskRequest struct {
	JSONRPC string          `json:"jsonrpc"` // Always "2.0"
	Method  string          `json:"method"`  // Always "tasks/get"
	Params  TaskQueryParams `json:"params"`
	ID      any             `json:"id"` // Request ID
}

// SendTaskRequest represents a 'tasks/send' JSON-RPC request for synchronous processing.
type SendTaskRequest struct {
	JSONRPC string         `json:"jsonrpc"` // Always "2.0"
	Method  string         `json:"method"`  // Always "tasks/send"
	Params  TaskSendParams `json:"params"`
	ID      any            `json:"id"` // Request ID
}

// SendTaskStreamingRequest represents a 'tasks/sendSubscribe' JSON-RPC request for streaming updates.
type SendTaskStreamingRequest struct {
	JSONRPC string         `json:"jsonrpc"` // Always "2.0"
	Method  string         `json:"method"`  // Always "tasks/sendSubscribe"
	Params  TaskSendParams `json:"params"`
	ID      any            `json:"id"` // Request ID
}

// SetTaskPushNotificationRequest represents a 'tasks/pushNotification/set' JSON-RPC request.
type SetTaskPushNotificationRequest struct {
	JSONRPC string                     `json:"jsonrpc"` // Always "2.0"
	Method  string                     `json:"method"`  // Always "tasks/pushNotification/set"
	Params  TaskPushNotificationConfig `json:"params"`
	ID      any                        `json:"id"` // Request ID
}

// TaskResubscriptionRequest represents a 'tasks/resubscribe' JSON-RPC request to resume streaming.
type TaskResubscriptionRequest struct {
	JSONRPC string          `json:"jsonrpc"` // Always "2.0"
	Method  string          `json:"method"`  // Always "tasks/resubscribe"
	Params  TaskQueryParams `json:"params"`  // Uses QueryParams according to spec example
	ID      any             `json:"id"`      // Request ID
}
