package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gate4ai/mcp/shared"
	"github.com/gate4ai/mcp/shared/mcp/2024/schema"
	"go.uber.org/zap"
)

// gw_resources_read handles the "resources/read" request from the client.
func (c *GatewayCapability) gw_resources_read(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()), zap.String("method", "resources/read"))
	logger.Debug("Processing request")

	if inputMsg.Params == nil {
		return nil, fmt.Errorf("missing parameters")
	}
	var params schema.ReadResourceRequestParams
	if err := json.Unmarshal(*inputMsg.Params, &params); err != nil {
		logger.Error("Failed to unmarshal parameters", zap.Error(err))
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.URI == "" {
		logger.Warn("Resource URI is required")
		return nil, fmt.Errorf("resource URI is required")
	}
	logger = logger.With(zap.String("uri", params.URI))

	// Get combined list of resources (handles fetching, conflict resolution, caching)
	// This ensures we know which backend owns the potentially modified URI.
	resources, err := c.GetResources(inputMsg, logger)
	if err != nil {
		// Error already logged by GetResources
		return nil, fmt.Errorf("failed to get resource list: %w", err)
	}

	// Find the target resource by its (potentially modified) URI
	var targetResource *resourceWithServerInfo
	for _, res := range resources {
		if res != nil && res.URI == params.URI { // Add nil check
			targetResource = res
			break
		}
	}

	if targetResource == nil {
		logger.Error("Resource not found in any backend")
		return nil, fmt.Errorf("resource not found: %s", params.URI)
	}

	logger.Debug("Found resource, forwarding read request to backend",
		zap.String("backendServerID", targetResource.serverID),
		zap.String("originalURI", targetResource.originalURI))

	// Get the backend session for the server that owns this resource
	backendSession, err := c.getBackendSession(inputMsg.Session, targetResource.serverID)
	if err != nil {
		// Error logged by getBackendSession
		return nil, err
	}
	if backendSession == nil {
		logger.Error("Backend session is nil after successful retrieval", zap.String("serverID", targetResource.serverID))
		return nil, fmt.Errorf("internal error: failed to get valid backend session for server %s", targetResource.serverID)
	}

	// Use a timeout context for the backend call
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Timeout for the backend read operation
	defer cancel()

	// Forward the request to the backend using the ORIGINAL resource URI
	resultChan := backendSession.ReadResource(ctx, targetResource.originalURI)
	result := <-resultChan // Wait for the result from the backend

	if result.Err != nil {
		logger.Error("Failed to read resource from backend server",
			zap.String("server", targetResource.serverID),
			zap.String("originalURI", targetResource.originalURI),
			zap.Error(result.Err))
		// Return the error received from the backend
		return nil, fmt.Errorf("backend error reading resource '%s': %w", targetResource.originalURI, result.Err)
	}

	if result.Result == nil {
		// Should not happen if Err is nil, but check defensively
		err := fmt.Errorf("nil result received from backend %s for resource %s",
			targetResource.serverID, targetResource.originalURI)
		logger.Error(err.Error())
		return nil, err
	}

	// Return the contents obtained from the backend (already in 2025 format)
	logger.Debug("Successfully read resource from backend")
	return result.Result, nil
}
