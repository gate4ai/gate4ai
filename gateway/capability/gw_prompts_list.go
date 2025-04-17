package capability

import (
	"github.com/gate4ai/gate4ai/shared"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// gw_prompts_list handles listing prompts (added missing handler)
func (c *GatewayCapability) gw_prompts_list(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()))
	logger.Debug("Processing prompts/list request")

	allPrompts, err := c.GetPrompts(inputMsg, logger)
	if err != nil {
		return nil, err // Error already logged in GetPrompts
	}

	// Convert []*prompt to []schema.Prompt for the result
	schemaPrompts := make([]schema.Prompt, len(allPrompts))
	for i, p := range allPrompts {
		if p != nil { // Add nil check
			schemaPrompts[i] = p.Prompt
		}
	}

	return schema.ListPromptsResult{
		Prompts: schemaPrompts,
		// Pagination not implemented in fetchAndCombineFromBackends yet
	}, nil
}
