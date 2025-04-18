package a2a

import (
	"fmt"

	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/gate4ai/shared/config"
)

// CreateAgentCard constructs the AgentCard using base info from config and dynamically determined fields.
func CreateAgentCard(cfg config.IConfig, agentURL string) (a2aSchema.AgentCard, error) {
	baseInfo, err := cfg.GetA2ACardBaseInfo(agentURL)
	if err != nil {
		return a2aSchema.AgentCard{}, fmt.Errorf("failed to get A2A base info from config: %w", err)
	}

	// Define the skills this specific agent implementation offers
	skills := []a2aSchema.AgentSkill{
		{
			ID:          "scenario_runner",
			Name:        "A2A Scenario Runner",
			Description: shared.PointerTo("Runs different A2A test scenarios based on input text ('error_test', 'input_test', 'cancel_test', 'stream_test')."),
			Tags:        []string{"testing", "a2a", "scenarios"},
			Examples: []string{
				"Run the simple success case.",
				"error_test",
				"input_test",
				"cancel_test",
				"stream_test",
				"Please provide the 'secret_code=123' to continue.",
			},
			// Inherits default input/output modes
		},
		// Add other skills here if needed
	}

	// Define capabilities based on implementation
	capabilities := a2aSchema.AgentCapabilities{
		Streaming:              true,  // We implement tasks/sendSubscribe
		PushNotifications:      false, // Not implemented
		StateTransitionHistory: false, // Not implemented
	}

	// Construct the final card
	card := a2aSchema.AgentCard{
		Name:               baseInfo.Name,
		Description:        baseInfo.Description,
		URL:                baseInfo.AgentURL, // Use the URL passed in
		Provider:           baseInfo.Provider,
		Version:            baseInfo.Version,
		DocumentationURL:   baseInfo.DocumentationURL,
		Capabilities:       capabilities,
		Authentication:     baseInfo.Authentication,
		DefaultInputModes:  baseInfo.DefaultInputModes,
		DefaultOutputModes: baseInfo.DefaultOutputModes,
		Skills:             skills,
	}

	// Set defaults if empty from config
	if len(card.DefaultInputModes) == 0 {
		card.DefaultInputModes = []string{"text"}
	}
	if len(card.DefaultOutputModes) == 0 {
		card.DefaultOutputModes = []string{"text", "file"} // Example agent produces text/file artifacts
	}

	return card, nil
}
