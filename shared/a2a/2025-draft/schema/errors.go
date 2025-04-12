package schema

// PushNotificationNotSupportedError indicates the agent does not support push notifications.
type PushNotificationNotSupportedError struct {
	Code    int    `json:"code"`    // Always -32003
	Message string `json:"message"` // Always "Push Notification is not supported"
	Data    any    `json:"data"`    // Always null
}

func (e *PushNotificationNotSupportedError) Error() string {
	return e.Message
}

// TaskNotCancelableError indicates the task is in a state where it cannot be canceled.
type TaskNotCancelableError struct {
	Code    int    `json:"code"`    // Always -32002
	Message string `json:"message"` // Always "Task cannot be canceled"
	Data    any    `json:"data"`    // Always null
}

func (e *TaskNotCancelableError) Error() string {
	return e.Message
}

// TaskNotFoundError indicates the specified task ID was not found.
type TaskNotFoundError struct {
	Code    int    `json:"code"`    // Always -32001
	Message string `json:"message"` // Always "Task not found"
	Data    any    `json:"data"`    // Always null
}

func (e *TaskNotFoundError) Error() string {
	return e.Message
}

// UnsupportedOperationError indicates the requested operation is not supported by the agent.
type UnsupportedOperationError struct {
	Code    int    `json:"code"`    // Always -32004
	Message string `json:"message"` // Always "This operation is not supported"
	Data    any    `json:"data"`    // Always null
}

func (e *UnsupportedOperationError) Error() string {
	return e.Message
}

// ContentTypeNotSupportedError indicates a mismatch in supported content types between client and agent.
// Note: This error (-32005) was present in the summary but not in the schema definition provided.
// Adding a basic structure based on the pattern.
type ContentTypeNotSupportedError struct {
	Code    int    `json:"code"`    // Should be -32005 based on summary
	Message string `json:"message"` // E.g., "Content type not supported"
	Data    any    `json:"data,omitempty"`
}

func (e *ContentTypeNotSupportedError) Error() string {
	return e.Message
}
