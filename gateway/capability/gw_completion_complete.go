package capability

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gate4ai/mcp/shared"
	"go.uber.org/zap"
)

func (c *GatewayCapability) gw_completion_complete(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()))
	logger.Debug("Processing completion/complete request")

	var params struct {
		Argument struct {
			Text string `json:"text"`
			Ref  struct {
				Type string `json:"type"`
				ID   string `json:"id,omitempty"`  // For prompt completions
				URI  string `json:"uri,omitempty"` // For resource completions
			} `json:"ref"`
		} `json:"argument"`
	}

	if err := json.Unmarshal(*inputMsg.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	// Determine which server to use based on the reference type
	var serverID string
	var originalID string
	var originalURI string

	if params.Argument.Ref.Type == "prompt" {
		// For prompt completions, get the prompt from all servers
		prompts, err := c.GetPrompts(inputMsg, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to get prompts: %w", err)
		}

		// Find the target prompt
		for _, p := range prompts {
			if p.Name == params.Argument.Ref.ID {
				serverID = p.serverID
				originalID = p.Name
				break
			}
		}

		if serverID == "" {
			return nil, fmt.Errorf("prompt not found: %s", params.Argument.Ref.ID)
		}
	} else if params.Argument.Ref.Type == "resource" {
		// For resource completions, get the resource
		resources, err := c.GetResources(inputMsg, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to get resources: %w", err)
		}

		// Find the target resource
		for _, res := range resources {
			if res.URI == params.Argument.Ref.URI {
				serverID = res.serverID
				originalURI = res.originalURI
				break
			}
		}

		if serverID == "" {
			return nil, fmt.Errorf("resource not found: %s", params.Argument.Ref.URI)
		}
	} else {
		return nil, fmt.Errorf("unsupported reference type: %s", params.Argument.Ref.Type)
	}

	// In a real implementation, we would get the backend session for the server
	// and forward the request to it. Since we don't have the API for this in the client,
	// we'll simply log the server we would use.
	logger.Info("Would forward completion request to server",
		zap.String("server", serverID),
		zap.String("refType", params.Argument.Ref.Type),
		zap.String("text", params.Argument.Text))

	// For prompt completions
	if params.Argument.Ref.Type == "prompt" {
		logger.Info("Mock completion for prompt",
			zap.String("id", params.Argument.Ref.ID),
			zap.String("original", originalID))

		// Create a simple mock result based on the text
		return map[string]interface{}{
			"completion": map[string]interface{}{
				"hasMore": false,
				"total":   5,
				"values": []string{
					strings.ToUpper(params.Argument.Text),
					strings.ToUpper(params.Argument.Text),
					params.Argument.Text + "...",
					params.Argument.Text + "?",
					params.Argument.Text + "!",
				},
			},
		}, nil
	}

	// For resource completions
	logger.Info("Mock completion for resource",
		zap.String("uri", params.Argument.Ref.URI),
		zap.String("original", originalURI))

	// Create a simple mock result based on the text
	return map[string]interface{}{
		"completion": map[string]interface{}{
			"hasMore": false,
			"total":   3,
			"values": []string{
				params.Argument.Text + ".resource",
				params.Argument.Text + ".data",
				params.Argument.Text + ".content",
			},
		},
	}, nil
}

// Create a simple mock result based on the text
