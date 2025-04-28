package discovering

import "time"

// LogDetails holds specific error information for a discovery step.
type LogDetails struct {
	Type              string `json:"type,omitempty"` // "HTTP", "Timeout", "Connection", "Parse", "Validation", "RequestCreation", "ReadBody", "Configuration", "Unknown"
	Message           string `json:"message,omitempty"`
	StatusCode        *int   `json:"statusCode,omitempty"`         // Pointer to handle absence of status code
	ResponseBodyPreview string `json:"responseBodyPreview,omitempty"` // Max 1000 chars
}

// DiscoveryLogEntry represents a single entry in the discovery process log.
// It includes a StepID to correlate initial attempts with final results.
type DiscoveryLogEntry struct {
	StepID    string        `json:"stepId"`      // Unique ID for this specific discovery step attempt
	Timestamp time.Time     `json:"timestamp"`   // Timestamp of this specific log event (attempt or result)
	Protocol  string        `json:"protocol"`    // "MCP", "A2A", "REST", "General"
	Method    string        `json:"method"`      // "Handshake", "GET", "POST", "ParseURL", "Internal", etc.
	Step      string        `json:"step"`        // "Attempt", "WellKnown", "/openapi.json", etc.
	URL       string        `json:"url,omitempty"` // Full URL attempted
	Status    string        `json:"status"`      // "attempting", "success", "error"
	Details   *LogDetails   `json:"details,omitempty"` // Populated on success/error
}