package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"time" // Import time

	"github.com/gate4ai/gate4ai/shared"
	// Use 2025 schema for parsing notifications
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// subscribeParams struct removed, use findBackendSessionForResourceURI which parses URI

// gw_resources_subscribe handles the "resources/subscribe" request from the client.
func (c *GatewayCapability) gw_resources_subscribe(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()), zap.String("method", "resources/subscribe"))
	logger.Debug("Processing request")

	// findBackendSessionForResourceURI also parses the URI from params
	backendSession, targetResource, err := c.findBackendSessionForResourceURI(inputMsg, logger)
	if err != nil {
		// Error logged by findBackendSessionForResourceURI
		return nil, err // Return error finding session/resource
	}

	logger.Debug("Found resource, forwarding subscribe request to backend",
		zap.String("backendServerID", targetResource.serverID),
		zap.String("originalURI", targetResource.originalURI),
		zap.String("gatewayURI", targetResource.URI))

	// Create a context with timeout for the backend subscribe call
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Short timeout for subscribe/unsubscribe
	defer cancel()

	// Pass the ORIGINAL URI to the backend's SubscribeResource method
	err = backendSession.SubscribeResource(ctx, targetResource.originalURI)
	if err != nil {
		logger.Error("Failed to subscribe to resource on backend server",
			zap.String("server", targetResource.serverID),
			zap.String("originalURI", targetResource.originalURI),
			zap.Error(err))
		return nil, fmt.Errorf("failed to subscribe to resource '%s' on backend: %w", targetResource.originalURI, err)
	}

	logger.Info("Successfully subscribed to resource via backend",
		zap.String("gatewayURI", targetResource.URI),
		zap.String("backendServerID", targetResource.serverID),
		zap.String("originalURI", targetResource.originalURI))

	// Return success response to the gateway client
	return map[string]interface{}{
		"status": "subscribed",
		"uri":    targetResource.URI, // Return the potentially modified URI to the client
	}, nil
}

// gw_resources_unsubscribe handles the "resources/unsubscribe" request from the client.
func (c *GatewayCapability) gw_resources_unsubscribe(inputMsg *shared.Message) (interface{}, error) {
	logger := c.logger.With(zap.String("msgID", inputMsg.ID.String()), zap.String("method", "resources/unsubscribe"))
	logger.Debug("Processing request")

	// findBackendSessionForResourceURI also parses the URI from params
	backendSession, targetResource, err := c.findBackendSessionForResourceURI(inputMsg, logger)
	if err != nil {
		// Error logged by findBackendSessionForResourceURI
		return nil, err // Return error finding session/resource
	}

	logger.Debug("Found resource, forwarding unsubscribe request to backend",
		zap.String("backendServerID", targetResource.serverID),
		zap.String("originalURI", targetResource.originalURI),
		zap.String("gatewayURI", targetResource.URI))

	// Create a context with timeout for the backend unsubscribe call
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Short timeout
	defer cancel()

	// Pass the ORIGINAL URI to the backend's UnsubscribeResource method
	err = backendSession.UnsubscribeResource(ctx, targetResource.originalURI)
	if err != nil {
		logger.Error("Failed to unsubscribe from resource on backend server",
			zap.String("server", targetResource.serverID),
			zap.String("originalURI", targetResource.originalURI),
			zap.Error(err))
		return nil, fmt.Errorf("failed to unsubscribe from resource '%s' on backend: %w", targetResource.originalURI, err)
	}

	logger.Info("Successfully unsubscribed from resource via backend",
		zap.String("gatewayURI", targetResource.URI),
		zap.String("backendServerID", targetResource.serverID),
		zap.String("originalURI", targetResource.originalURI))

	// Return success response to the gateway client
	return map[string]interface{}{
		"status": "unsubscribed",
		"uri":    targetResource.URI, // Return the potentially modified URI
	}, nil
}

// gw_resources_notification_updated is the callback invoked by a backend client.Session when it receives a resource update notification.
func (c *GatewayCapability) gw_resources_notification_updated(backendMsg *shared.Message) {
	logger := c.logger.With(zap.String("method", "notifications/resources/updated_callback"))
	logger.Debug("Processing resource update notification from backend")

	if backendMsg == nil || backendMsg.Params == nil {
		logger.Error("Received nil message or params in resource update callback")
		return
	}

	// Parse the parameters from the backend notification using V2025 schema
	var backendParams schema.ResourceUpdatedNotificationParams
	if err := json.Unmarshal(*backendMsg.Params, &backendParams); err != nil {
		logger.Error("Failed to unmarshal backend resource updated notification params", zap.Error(err), zap.ByteString("params", *backendMsg.Params))
		return
	}
	originalURI := backendParams.URI // This is the URI known by the backend

	// Get the originating backend session's parameters to find the server ID and the target gateway client session
	backendSessionParams := backendMsg.Session.GetParams()

	serverID, _, okServer := GetServerSlug(backendSessionParams)
	if !okServer || serverID == "" {
		logger.Error("Could not determine server ID from backend session receiving the update")
		// backendMsg.Session.Close() // Consider closing inconsistent backend session
		return
	}

	clientSession, _, okClient := GetClientSession(backendSessionParams)
	if !okClient || clientSession == nil {
		logger.Error("Could not retrieve gateway client session associated with the backend session")
		// backendMsg.Session.Close() // Consider closing inconsistent backend session
		return
	}
	clientSessionLogger := logger.With(zap.String("clientSessionID", clientSession.GetID()))

	// Find the corresponding gateway-facing URI using the cached resources for the client session
	// We need the cache to map originalURI@serverID back to the gateway URI
	cachedResources, _, okCache := GetSavedResources(clientSession.GetParams())
	if !okCache {
		clientSessionLogger.Warn("No cached resources found for client session, cannot map updated URI", zap.String("originalURI", originalURI), zap.String("serverID", serverID))
		// Option 1: Force refresh cache?
		// Option 2: Send notification with original URI (client might not recognize it)?
		// Option 3: Send notification with prefixed URI (serverID:originalURI)?
		// Let's try Option 3 as a fallback.
		gatewayURI := fmt.Sprintf("%s:%s", serverID, originalURI)
		clientSessionLogger.Warn("Sending notification with prefixed URI as fallback", zap.String("gatewayURI", gatewayURI))
		clientSession.SendNotification("notifications/resources/updated", map[string]interface{}{
			"uri": gatewayURI,
		})
		return
	}

	gatewayURI := ""
	for _, res := range cachedResources {
		if res != nil && res.originalURI == originalURI && res.serverID == serverID {
			gatewayURI = res.URI
			break
		}
	}

	if gatewayURI == "" {
		clientSessionLogger.Error("Could not find gateway URI corresponding to updated resource", zap.String("originalURI", originalURI), zap.String("serverID", serverID))
		// Fallback like above, or maybe don't send notification if mapping fails?
		// Let's try sending prefixed URI again.
		gatewayURI = fmt.Sprintf("%s:%s", serverID, originalURI)
		clientSessionLogger.Warn("Sending notification with prefixed URI as fallback (mapping failed)", zap.String("gatewayURI", gatewayURI))
	} else {
		clientSessionLogger.Debug("Mapped backend update to gateway URI", zap.String("originalURI", originalURI), zap.String("serverID", serverID), zap.String("gatewayURI", gatewayURI))
	}

	// Send notification to the gateway client using the mapped (or fallback prefixed) URI
	clientSession.SendNotification("notifications/resources/updated", map[string]interface{}{
		"uri": gatewayURI,
	})
	clientSessionLogger.Info("Forwarded resource update notification to client", zap.String("gatewayURI", gatewayURI))
}
