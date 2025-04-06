package schema

// Annotations contain metadata about objects.
type Annotations struct {
	// Describes who the intended customer of this object or data is.
	// It can include multiple entries to indicate content useful for multiple audiences (e.g., `["user", "assistant"]`).
	Audience []Role `json:"audience,omitempty"`
	// Describes how important this data is for operating the server.
	// A value of 1 means "most important," and indicates that the data is
	// effectively required, while 0 means "least important," and indicates that
	// the data is entirely optional.
	// Minimum: 0, Maximum: 1.
	Priority *float64 `json:"priority,omitempty"`
}

type Role = string
