package schema

// ModelHint provides hints for model selection.
type ModelHint struct {
	Name string `json:"name,omitempty"` // Hint for model name
}

// ModelPreferences expresses server preferences for model selection.
type ModelPreferences struct {
	CostPriority         *float64    `json:"costPriority,omitempty"`         // Priority for cost (0-1)
	Hints                []ModelHint `json:"hints,omitempty"`                // Optional model selection hints
	IntelligencePriority *float64    `json:"intelligencePriority,omitempty"` // Priority for capabilities (0-1)
	SpeedPriority        *float64    `json:"speedPriority,omitempty"`        // Priority for speed (0-1)
}
