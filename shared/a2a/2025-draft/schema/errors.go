package schema

import "fmt"

// A2A specific error codes
const (
	ErrorCodeTaskNotFound                 = -32001
	ErrorCodeTaskNotCancelable            = -32002
	ErrorCodePushNotificationNotSupported = -32003
	ErrorCodeUnsupportedOperation         = -32004
	ErrorCodeContentTypeNotSupported      = -32005
)

// PushNotificationNotSupportedError indicates the agent does not support push notifications.
type PushNotificationNotSupportedError struct {
	Code    int    `json:"code"`    // Always -32003
	Message string `json:"message"` // Always "Push Notification is not supported"
	Data    any    `json:"data,omitempty"`
}

func NewPushNotificationNotSupportedError() *JSONRPCError {
	return &JSONRPCError{
		Code:    ErrorCodePushNotificationNotSupported,
		Message: "Push Notification is not supported",
	}
}

func (e *PushNotificationNotSupportedError) Error() string {
	return e.Message
}

// TaskNotCancelableError indicates the task is in a state where it cannot be canceled.
type TaskNotCancelableError struct {
	Code    int    `json:"code"`    // Always -32002
	Message string `json:"message"` // Always "Task cannot be canceled"
	Data    any    `json:"data,omitempty"`
}

func NewTaskNotCancelableError(taskId string) *JSONRPCError {
	data := map[string]string{"taskId": taskId}
	var anyData any = data
	return &JSONRPCError{
		Code:    ErrorCodeTaskNotCancelable,
		Message: fmt.Sprintf("Task '%s' cannot be canceled", taskId),
		Data:    &anyData,
	}
}

func (e *TaskNotCancelableError) Error() string {
	return e.Message
}

// TaskNotFoundError indicates the specified task ID was not found.
type TaskNotFoundError struct {
	Code    int    `json:"code"`    // Always -32001
	Message string `json:"message"` // Always "Task not found"
	Data    any    `json:"data,omitempty"`
}

func NewTaskNotFoundError(taskId string) *JSONRPCError {
	data := map[string]string{"taskId": taskId}
	var anyData any = data
	return &JSONRPCError{
		Code:    ErrorCodeTaskNotFound,
		Message: fmt.Sprintf("Task not found: %s", taskId),
		Data:    &anyData,
	}
}

func (e *TaskNotFoundError) Error() string {
	return e.Message
}

// UnsupportedOperationError indicates the requested operation is not supported by the agent.
type UnsupportedOperationError struct {
	Code    int    `json:"code"`    // Always -32004
	Message string `json:"message"` // Always "This operation is not supported"
	Data    any    `json:"data,omitempty"`
}

func NewUnsupportedOperationError(operation string) *JSONRPCError {
	data := map[string]string{"operation": operation}
	var anyData any = data
	return &JSONRPCError{
		Code:    ErrorCodeUnsupportedOperation,
		Message: fmt.Sprintf("Operation '%s' is not supported", operation),
		Data:    &anyData,
	}
}

func (e *UnsupportedOperationError) Error() string {
	return e.Message
}

// ContentTypeNotSupportedError indicates a mismatch in supported content types between client and agent.
type ContentTypeNotSupportedError struct {
	Code    int    `json:"code"`    // Should be -32005 based on summary
	Message string `json:"message"` // E.g., "Content type not supported"
	Data    any    `json:"data,omitempty"`
}

func NewContentTypeNotSupportedError(contentType string) *JSONRPCError {
	data := map[string]string{"contentType": contentType}
	var anyData any = data
	return &JSONRPCError{
		Code:    ErrorCodeContentTypeNotSupported,
		Message: fmt.Sprintf("Content type '%s' not supported", contentType),
		Data:    &anyData,
	}
}

func (e *ContentTypeNotSupportedError) Error() string {
	return e.Message
}
