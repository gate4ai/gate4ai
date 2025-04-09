package schema

// CancelledNotification indicates cancellation of a previously-issued request.
type CancelledNotification struct {
	Method string                      `json:"method"` // const: "notifications/cancelled"
	Params CancelledNotificationParams `json:"params"`
}

// CancelledNotificationParams contains parameters for cancellation notifications.
type CancelledNotificationParams struct {
	Reason    string    `json:"reason,omitempty"` // Optional reason for cancellation
	RequestID RequestID `json:"requestId"`        // The ID of the request to cancel
}

// CompleteRequest is a request from the client to the server for completion options.
type CompleteRequest struct {
	Method string                `json:"method"` // const: "completion/complete"
	Params CompleteRequestParams `json:"params"`
}

// CompleteRequestParams contains parameters for completion requests.
type CompleteRequestParams struct {
	Argument CompleteArgument `json:"argument"` // The argument's information
	Ref      interface{}      `json:"ref"`      // Can be PromptReference or ResourceReference
}

// CompleteArgument contains argument information for completions.
type CompleteArgument struct {
	Name  string `json:"name"`  // The name of the argument
	Value string `json:"value"` // The value of the argument to use for completion matching
}

// CompleteResult is the server's response to a completion request.
type CompleteResult struct {
	Meta       map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Completion CompletionInfo         `json:"completion"`
}

// CompletionInfo contains completion results.
type CompletionInfo struct {
	HasMore bool     `json:"hasMore,omitempty"` // Indicates if there are additional completion options
	Total   int      `json:"total,omitempty"`   // The total number of completion options available
	Values  []string `json:"values"`            // An array of completion values, max 100 items
}

// LoggingLevel represents the severity of a log message.
type LoggingLevel string

// Logging level constants
const (
	LoggingLevelEmergency LoggingLevel = "emergency"
	LoggingLevelAlert     LoggingLevel = "alert"
	LoggingLevelCritical  LoggingLevel = "critical"
	LoggingLevelError     LoggingLevel = "error"
	LoggingLevelWarning   LoggingLevel = "warning"
	LoggingLevelNotice    LoggingLevel = "notice"
	LoggingLevelInfo      LoggingLevel = "info"
	LoggingLevelDebug     LoggingLevel = "debug"
)

// LoggingMessageNotification carries log messages from server to client.
type LoggingMessageNotification struct {
	Method string                           `json:"method"` // const: "notifications/message"
	Params LoggingMessageNotificationParams `json:"params"`
}

// LoggingMessageNotificationParams contains logging message parameters.
type LoggingMessageNotificationParams struct {
	Data   interface{}  `json:"data"`             // The data to be logged
	Level  LoggingLevel `json:"level"`            // Message severity
	Logger string       `json:"logger,omitempty"` // Optional logger name
}

// ProgressNotification provides updates for long-running requests.
type ProgressNotification struct {
	Method string                     `json:"method"` // const: "notifications/progress"
	Params ProgressNotificationParams `json:"params"`
}

// ProgressNotificationParams contains progress information.
type ProgressNotificationParams struct {
	Progress      float64     `json:"progress"`        // Current progress value
	ProgressToken interface{} `json:"progressToken"`   // Associated request token (string or integer)
	Total         float64     `json:"total,omitempty"` // Total progress required, if known
}

// SetLevelRequest adjusts logging level.
type SetLevelRequest struct {
	Method string                `json:"method"` // const: "logging/setLevel"
	Params SetLevelRequestParams `json:"params"`
}

// SetLevelRequestParams contains parameters for log level setting.
type SetLevelRequestParams struct {
	Level LoggingLevel `json:"level"` // Desired logging level
}
