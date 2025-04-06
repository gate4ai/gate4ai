package schema

import schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"

// ModelHint provides hints for model selection.
// Keys not declared here are currently left unspecified by the spec and are up
// to the client to interpret.
type ModelHint = schema2024.ModelHint

// ModelPreferences expresses the server's preferences for model selection, requested of the client during sampling.
// These preferences are always advisory. The client MAY ignore them.
type ModelPreferences struct {
	// Optional hints to use for model selection.
	// If multiple hints are specified, the client MUST evaluate them in order
	// (such that the first match is taken).
	Hints []*ModelHint `json:"hints,omitempty"`

	// How much to prioritize cost when selecting a model. A value of 0 means cost
	// is not important, while a value of 1 means cost is the most important factor.
	CostPriority *float64 `json:"costPriority,omitempty"` // @minimum 0 @maximum 1

	// How much to prioritize sampling speed (latency) when selecting a model. A
	// value of 0 means speed is not important, while a value of 1 means speed is
	// the most important factor.
	SpeedPriority *float64 `json:"speedPriority,omitempty"` // @minimum 0 @maximum 1

	// How much to prioritize intelligence and capabilities when selecting a
	// model. A value of 0 means intelligence is not important, while a value of 1
	// means intelligence is the most important factor.
	IntelligencePriority *float64 `json:"intelligencePriority,omitempty"` // @minimum 0 @maximum 1
}
