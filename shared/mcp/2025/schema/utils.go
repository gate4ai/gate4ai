package schema

import schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"

// ProgressNotification provides updates for long-running requests.
// An out-of-band notification used to inform the receiver of a progress update.
type ProgressNotification struct {
	Method string                     `json:"method"` // const: "notifications/progress"
	Params ProgressNotificationParams `json:"params"`
}

// ProgressNotificationParams contains progress information.
type ProgressNotificationParams struct {
	// The progress token associated with the original request.
	ProgressToken ProgressToken `json:"progressToken"` // string or integer
	// The progress thus far. Should increase over time.
	Progress float64 `json:"progress"`
	// Total progress required, if known.
	Total *float64 `json:"total,omitempty"`
	// An optional message describing the current progress.
	Message *string `json:"message,omitempty"` // ADDED in 2025
}

// CancelledNotification indicates cancellation of a previously-issued request.
// This notification can be sent by either side.
type CancelledNotification struct {
	Method string                      `json:"method"` // const: "notifications/cancelled"
	Params CancelledNotificationParams `json:"params"`
}

// CancelledNotificationParams contains parameters for cancellation notifications.
type CancelledNotificationParams = schema2024.CancelledNotificationParams

// LoggingLevel represents the severity of a log message (syslog levels).
type LoggingLevel = schema2024.LoggingLevel

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
type LoggingMessageNotificationParams = schema2024.LoggingMessageNotificationParams

// SetLevelRequest adjusts logging level.
// A request from the client to the server, to enable or adjust logging.
type SetLevelRequest struct {
	Method string                `json:"method"` // const: "logging/setLevel"
	Params SetLevelRequestParams `json:"params"`
}

// SetLevelRequestParams contains parameters for log level setting.
type SetLevelRequestParams = schema2024.SetLevelRequestParams

// PingRequest checks if the other party is alive.
// A ping, issued by either the server or the client.
type PingRequest struct {
	Method string                 `json:"method"`           // const: "ping"
	Params map[string]interface{} `json:"params,omitempty"` // Allows for _meta field
}
