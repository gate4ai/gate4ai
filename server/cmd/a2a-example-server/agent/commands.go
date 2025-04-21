package agent

// ParsedCommand represents a command extracted from the message text parts.
type ParsedCommand struct {
	Type   string                 // e.g., "wait", "respond", "ask", "stream", "error"
	Params map[string]interface{} // Command-specific parameters extracted during parsing
}
