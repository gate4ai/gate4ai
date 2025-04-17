package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	client "github.com/gate4ai/gate4ai/gateway/clients/mcpClient"
	"github.com/gate4ai/gate4ai/shared"

	// Use 2025 schema
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// prompt wrapper to include serverID
type prompt struct {
	schema.Prompt // Embed 2025 schema type
	serverSlug    string
	originalName  string // Store original name before potential modification
}

// GetPrompts fetches prompts from all subscribed backends for the user associated with inputMsg.
// It handles combining results and resolving name conflicts.
func (c *GatewayCapability) GetPrompts(inputMsg *shared.Message, logger *zap.Logger) ([]*prompt, error) {
	// Use a timeout for the overall operation
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Increased timeout
	defer cancel()

	// TODO: Implement caching similar to GetResources
	//  sessionParams := inputMsg.Session.GetParams()
	// if cachedPrompts, timestamp := GetSavedPrompts(sessionParams); cachedPrompts != nil { ... }

	// Define the function to fetch prompts from a single backend session
	fetchPromptsFunc := func(ctx context.Context, session *client.Session) ([]*prompt, error) {
		fetchLogger := logger.With(zap.String("server", session.Backend.Slug))
		fetchLogger.Debug("Getting prompts from backend")

		// GetPrompts now returns a channel of results
		promptsResultChan := session.GetPrompts(ctx)

		// Wait for result from the channel
		promptsResult := <-promptsResultChan

		if promptsResult.Error != nil {
			fetchLogger.Error("Failed to get prompts from backend", zap.Error(promptsResult.Error))
			return nil, promptsResult.Error
		}

		results := make([]*prompt, 0, len(promptsResult.Prompts))
		for _, p := range promptsResult.Prompts {
			pCopy := p // Create a copy to avoid modifying the cache
			results = append(results, &prompt{
				Prompt:       pCopy,
				serverSlug:   session.Backend.Slug,
				originalName: pCopy.Name, // Store original name
			})
		}
		fetchLogger.Debug("Received prompts from backend", zap.Int("count", len(results)))
		return results, nil
	}

	// Define the function to get the key (name) from a prompt
	getPromptKeyFunc := func(p *prompt) string {
		return p.Name // Use the current name (which might be modified later)
	}

	// Define the function to modify the prompt name in case of duplicates
	modifyPromptKeyFunc := func(p *prompt, serverID string) *prompt {
		// Use serverID passed to the function for prefixing
		newName := fmt.Sprintf("%s:%s", serverID, p.originalName) // Use originalName for prefixing
		logger.Debug("Modifying duplicate prompt name",
			zap.String("original", p.originalName),
			zap.String("server", serverID),
			zap.String("modified", newName),
		)
		p.Name = newName // Update the Name field
		return p
	}

	// Use the generic function to fetch and combine prompts
	allPrompts, err := fetchAndCombineFromBackends(c, ctx, inputMsg.Session, fetchPromptsFunc, getPromptKeyFunc, modifyPromptKeyFunc)
	if err != nil {
		logger.Error("Failed to fetch and combine prompts", zap.Error(err))
		return nil, fmt.Errorf("failed to get prompts: %w", err)
	}

	logger.Debug("Collected all prompts", zap.Int("count", len(allPrompts)))

	// TODO: Cache the results if caching is implemented
	// SaveCachedPrompts(sessionParams, allPrompts)

	return allPrompts, nil
}

// gw_prompts_get handles the "prompts/get" request from the client.
func (c *GatewayCapability) gw_prompts_get(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()), zap.String("method", "prompts/get"))
	logger.Debug("Processing request")

	// Parse parameters using 2025 schema type
	var params schema.GetPromptRequestParams
	if inputMsg.Params == nil {
		return nil, fmt.Errorf("missing parameters")
	}
	if err := json.Unmarshal(*inputMsg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal parameters", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Name == "" {
		logger.Warn("Prompt name is required")
		return nil, fmt.Errorf("prompt name is required")
	}
	logger = logger.With(zap.String("promptName", params.Name))

	// Get combined list of prompts (handles fetching and conflict resolution)
	allPrompts, err := c.GetPrompts(inputMsg, logger)
	if err != nil {
		// Error already logged by GetPrompts
		return nil, err // Return the error from GetPrompts
	}

	// Find the prompt by its potentially modified name
	var foundPrompt *prompt
	for _, p := range allPrompts {
		if p != nil && p.Name == params.Name { // Add nil check
			foundPrompt = p
			break
		}
	}

	if foundPrompt == nil {
		logger.Warn("Prompt not found in any backend")
		// Return an empty result or an error? Spec implies empty result might be acceptable.
		// Let's return an error for clarity.
		return nil, fmt.Errorf("prompt not found: %s", params.Name)
		// Alternative: return schema.GetPromptResult{Messages: []schema.PromptMessage{}}, nil
	}

	logger.Debug("Found prompt, forwarding to backend",
		zap.String("backendServerID", foundPrompt.serverSlug),
		zap.String("originalName", foundPrompt.originalName))

	// Get the backend session for the server that owns this prompt
	backendSession, err := c.getBackendSession(inputMsg.Session, foundPrompt.serverSlug)
	if err != nil {
		// Error logged by getBackendSession
		return nil, err // Return error from getting session
	}
	if backendSession == nil {
		// Should not happen if getBackendSession returns nil error, but check defensively
		logger.Error("Backend session is nil after successful retrieval", zap.String("serverID", foundPrompt.serverSlug))
		return nil, fmt.Errorf("internal error: failed to get valid backend session for server %s", foundPrompt.serverSlug)
	}

	// Use a timeout context for the backend call
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Timeout for the backend call
	defer cancel()

	// Forward the request to the backend using the ORIGINAL prompt name and arguments
	// The backend doesn't know about the gateway's prefixed names.
	resultChan := backendSession.GetPrompt(ctx, foundPrompt.originalName, params.Arguments)
	asyncResult := <-resultChan // Wait for the result from the backend

	if asyncResult.Error != nil {
		logger.Error("Failed to get prompt from backend server",
			zap.String("server", foundPrompt.serverSlug),
			zap.String("originalName", foundPrompt.originalName),
			zap.Error(asyncResult.Error))
		// Return the error received from the backend
		return nil, fmt.Errorf("backend error getting prompt '%s': %w", foundPrompt.originalName, asyncResult.Error)
	}

	// Return the result obtained from the backend (already in 2025 format)
	logger.Debug("Successfully retrieved prompt from backend")
	return asyncResult.Result, nil
}
