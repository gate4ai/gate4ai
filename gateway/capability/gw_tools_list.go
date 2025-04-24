package capability

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients/mcpClient"
	"github.com/gate4ai/gate4ai/shared"

	// Use 2025 schema
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// tool wrapper to include serverID and originalName
type tool struct {
	schema.Tool  // Embed 2025 schema type
	serverSlug   string
	originalName string // Store original name before potential modification
}

// GetTools fetches tools from all subscribed backends for the user associated with inputMsg.
// It handles combining results, resolving name conflicts, and caching.
func (c *GatewayCapability) GetTools(inputMsg *shared.Message, logger *zap.Logger) ([]*tool, error) {
	// Use a timeout for the overall operation
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second) // Adjusted timeout
	defer cancel()

	sessionParams := inputMsg.Session.GetParams()

	// Check for cached tools first
	if cachedTools, timestamp, ok := GetCachedTools(sessionParams); ok && time.Since(timestamp) < defaultCacheExpiration {
		logger.Debug("Returning cached tools", zap.Int("count", len(cachedTools)), zap.Time("cached_at", timestamp))
		// Filter out nil tools from cache before returning
		validCachedTools := make([]*tool, 0, len(cachedTools))
		for _, t := range cachedTools {
			if t != nil {
				validCachedTools = append(validCachedTools, t)
			}
		}
		return validCachedTools, nil
	}
	logger.Debug("Cache miss or expired, fetching fresh tools")

	// Define the function to fetch tools from a single backend session
	fetchToolsFunc := func(ctx context.Context, session *mcpClient.Session) ([]*tool, error) {
		fetchLogger := logger.With(zap.String("server", session.Backend.Slug))
		fetchLogger.Debug("Getting tools from backend")

		// GetTools now returns a channel GetToolsResult (using 2025 schema type)
		toolsResultChan := session.GetTools(ctx)
		select {
		case result := <-toolsResultChan:
			if result.Err != nil {
				fetchLogger.Error("Failed to get tools from backend", zap.Error(result.Err))
				return nil, result.Err // Propagate error
			}

			results := make([]*tool, 0, len(result.Tools))
			for _, t := range result.Tools {
				tCopy := t // Create a copy of the tool struct
				results = append(results, &tool{
					Tool:         tCopy,
					serverSlug:   session.Backend.Slug,
					originalName: tCopy.Name, // Store original name
				})
			}
			fetchLogger.Debug("Received tools from backend", zap.Int("count", len(results)))
			return results, nil
		case <-ctx.Done():
			fetchLogger.Warn("Context cancelled while waiting for tools from backend", zap.Error(ctx.Err()))
			return nil, ctx.Err()
		}
	}

	// Define the function to get the key (name) from a tool
	getToolKeyFunc := func(t *tool) string {
		return t.Name // Use the current name (which might be modified later)
	}

	// Define the function to modify the tool name in case of duplicates
	modifyToolKeyFunc := func(t *tool, serverSlug string) *tool {
		// Use serverID passed to the function for prefixing
		newName := fmt.Sprintf("%s:%s", serverSlug, t.originalName) // Use originalName for prefixing
		logger.Debug("Modifying duplicate tool name",
			zap.String("original", t.originalName),
			zap.String("server", serverSlug),
			zap.String("modified", newName),
		)
		t.Name = newName // Update the Name field
		return t
	}

	// Use the generic function to fetch and combine tools
	allTools, err := fetchAndCombineFromBackends(c, ctx, inputMsg.Session, fetchToolsFunc, getToolKeyFunc, modifyToolKeyFunc)
	if err != nil {
		logger.Error("Failed to fetch and combine tools", zap.Error(err))
		return nil, fmt.Errorf("failed to get tools: %w", err)
	}

	logger.Debug("Collected all tools", zap.Int("count", len(allTools)))

	// Cache the combined and potentially modified tools
	SaveCachedTools(sessionParams, allTools)

	return allTools, nil
}

// gw_tools_list handles the "tools/list" request from the client.
func (c *GatewayCapability) gw_tools_list(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()), zap.String("method", "tools/list"))
	logger.Debug("Processing request")

	// Get combined list of tools (handles fetching, conflict resolution, caching)
	tools, err := c.GetTools(inputMsg, logger)
	if err != nil {
		// Error already logged by GetTools
		return nil, err
	}

	// Convert []*tool to schema.ListToolsResult
	return toListToolsResult(tools), nil
}

// toListToolsResult converts the internal representation to the 2025 schema result type.
func toListToolsResult(tools []*tool) schema.ListToolsResult {
	result := schema.ListToolsResult{
		Tools: make([]schema.Tool, 0, len(tools)),
	}

	for _, t := range tools {
		if t != nil { // Add nil check
			result.Tools = append(result.Tools, t.Tool) // Append the embedded schema.Tool struct
		}
	}
	return result
}

// --- Caching Logic ---

const cachedToolsKey = "gw_cached_tools"

// SavedValue defined in sessionParams.go or gateway/capability.go

// SaveCachedTools stores tools with timestamp in session parameters.
func SaveCachedTools(sessionParams *sync.Map, tools []*tool) {
	sessionParams.Store(cachedToolsKey, &SavedValue{
		Value:     tools,
		Timestamp: time.Now(),
	})
}

// GetCachedTools retrieves tools from cache if present and not expired.
// Returns the cached tools, timestamp, and a boolean indicating success.
func GetCachedTools(sessionParams *sync.Map) ([]*tool, time.Time, bool) {
	cachedValue, ok := sessionParams.Load(cachedToolsKey)
	if !ok {
		return nil, time.Time{}, false // Not found
	}

	cached, ok := cachedValue.(*SavedValue)
	if !ok {
		return nil, time.Time{}, false // Invalid cache type
	}

	// Type assert the cached value
	tools, ok := cached.Value.([]*tool)
	if !ok {
		return nil, time.Time{}, false // Invalid data type in cache
	}

	return tools, cached.Timestamp, true
}
