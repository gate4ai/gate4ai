package capability

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/mcp/gateway/clients/mcpClient"
	"github.com/gate4ai/mcp/shared"

	// Use 2025 schema
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// resourceWithServerInfo extends the 2025 schema.Resource with server information
type resourceWithServerInfo struct {
	schema.Resource        // Embed 2025 schema type
	originalURI     string // Store original URI before potential modification
	serverID        string
}

// GetResources fetches resources from all subscribed backends for the user associated with inputMsg.
// It handles combining results, resolving URI conflicts, and caching.
func (c *GatewayCapability) GetResources(inputMsg *shared.Message, logger *zap.Logger) ([]*resourceWithServerInfo, error) {
	// Use a timeout for the overall operation
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Adjusted timeout
	defer cancel()

	sessionParams := inputMsg.Session.GetParams()

	// Check for cached resources first
	if cachedResources, timestamp, ok := GetSavedResources(sessionParams); ok && time.Since(timestamp) < defaultCacheExpiration {
		logger.Debug("Returning cached resources", zap.Int("count", len(cachedResources)), zap.Time("cached_at", timestamp))
		return cachedResources, nil
	}
	logger.Debug("Cache miss or expired, fetching fresh resources")

	// Define the function to fetch resources from a single backend session
	fetchResourcesFunc := func(ctx context.Context, session *mcpClient.Session) ([]*resourceWithServerInfo, error) {
		fetchLogger := logger.With(zap.String("server", session.Backend.Slug))
		fetchLogger.Debug("Getting resources from backend")

		// GetResources now returns a channel GetResourcesResult (using 2025 schema type)
		resourcesResultChan := session.GetResources(ctx)
		select {
		case result := <-resourcesResultChan:
			if result.Err != nil {
				fetchLogger.Error("Failed to get resources from backend", zap.Error(result.Err))
				return nil, result.Err // Propagate error
			}

			backendResources := result.Resources // Slice of schema.Resource (V2025)
			results := make([]*resourceWithServerInfo, 0, len(backendResources))
			for _, r := range backendResources {
				rCopy := r // Create copy
				results = append(results, &resourceWithServerInfo{
					Resource:    rCopy,
					originalURI: rCopy.URI, // Store original URI
					serverID:    session.Backend.Slug,
				})
			}
			fetchLogger.Debug("Received resources from backend", zap.Int("count", len(results)))
			return results, nil
		case <-ctx.Done():
			fetchLogger.Warn("Context cancelled while waiting for resources from backend", zap.Error(ctx.Err()))
			return nil, ctx.Err()
		}
	}

	// Define the function to get the key (URI) from a resource
	getResourceKeyFunc := func(r *resourceWithServerInfo) string {
		return r.URI // Use the current URI (which might be modified later)
	}

	// Define the function to modify the resource URI in case of duplicates
	modifyResourceKeyFunc := func(r *resourceWithServerInfo, serverID string) *resourceWithServerInfo {
		// Use serverID passed to the function for prefixing
		newURI := fmt.Sprintf("%s:%s", serverID, r.originalURI) // Use originalURI for prefixing
		logger.Debug("Modifying duplicate resource URI",
			zap.String("original", r.originalURI),
			zap.String("server", serverID),
			zap.String("modified", newURI),
		)
		r.URI = newURI // Update the URI field
		return r
	}

	// Use the generic function to fetch and combine resources
	allResources, err := fetchAndCombineFromBackends(c, ctx, inputMsg.Session, fetchResourcesFunc, getResourceKeyFunc, modifyResourceKeyFunc)
	if err != nil {
		logger.Error("Failed to fetch and combine resources", zap.Error(err))
		return nil, fmt.Errorf("failed to get resources: %w", err)
	}

	logger.Debug("Collected all resources", zap.Int("count", len(allResources)))

	// Cache the combined and potentially modified resources
	SaveCachedResources(sessionParams, allResources)
	return allResources, nil
}

// gw_resources_list handles the "resources/list" request from the client.
func (c *GatewayCapability) gw_resources_list(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()), zap.String("method", "resources/list"))
	logger.Debug("Processing request")

	// Get combined list of resources (handles fetching, conflict resolution, caching)
	allResources, err := c.GetResources(inputMsg, logger)
	if err != nil {
		// Error already logged by GetResources
		return nil, err
	}

	// Convert []*resourceWithServerInfo to []schema.Resource for the result
	return toListResourcesResult(allResources), nil
}

// toListResourcesResult converts the internal representation to the schema result type.
func toListResourcesResult(resources []*resourceWithServerInfo) schema.ListResourcesResult {
	schemaResources := make([]schema.Resource, 0, len(resources))
	for _, r := range resources {
		if r != nil { // Add nil check
			schemaResources = append(schemaResources, r.Resource) // Add the embedded schema.Resource
		}
	}
	return schema.ListResourcesResult{
		Resources: schemaResources,
		// Pagination not implemented in fetchAndCombineFromBackends yet
		PaginatedResult: schema.PaginatedResult{NextCursor: nil},
	}
}

// --- Caching Logic ---

const cachedResourcesKey = "gw_cached_resources"

// SaveCachedResources stores resources with timestamp in session parameters.
func SaveCachedResources(sessionParams *sync.Map, resources []*resourceWithServerInfo) {
	sessionParams.Store(cachedResourcesKey, &SavedValue{
		Value:     resources,
		Timestamp: time.Now(),
	})
}

// GetSavedResources retrieves resources from cache if present and not expired.
// Returns the cached resources, timestamp, and a boolean indicating success.
func GetSavedResources(sessionParams *sync.Map) ([]*resourceWithServerInfo, time.Time, bool) {
	cachedValue, ok := sessionParams.Load(cachedResourcesKey)
	if !ok {
		return nil, time.Time{}, false // Not found in cache
	}

	cached, ok := cachedValue.(*SavedValue)
	if !ok {
		// Invalid cache entry type
		return nil, time.Time{}, false
	}

	// Type assert the cached value
	resources, ok := cached.Value.([]*resourceWithServerInfo)
	if !ok {
		// Invalid data type in cache
		return nil, time.Time{}, false
	}

	return resources, cached.Timestamp, true
}
