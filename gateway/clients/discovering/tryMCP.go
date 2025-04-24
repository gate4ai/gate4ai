package discovering

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients"
	"github.com/gate4ai/gate4ai/gateway/clients/mcpClient"
	"github.com/gate4ai/gate4ai/shared"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// tryMCPDiscovery attempts to discover if the target URL hosts an MCP server.
// Now accepts discoveryHeaders map. authBearer is extracted from headers if present.
func tryMCPDiscovery(ctx context.Context, targetURL string, discoveryHeaders map[string]string, logger *zap.Logger) (*DiscoveryResult, error) {
	logger.Debug("Attempting MCP discovery", zap.String("url", targetURL), zap.Int("headerCount", len(discoveryHeaders)))

	mcpClientInstance, err := mcpClient.New(shared.RandomID(), targetURL, logger.Named("mcp-discover-client"))
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client for discovery: %w", err)
	}

	httpClient := &http.Client{ // Use default client, timeout handled by context
		// Timeout: 5 * time.Second, // Timeout handled by context passed to NewSession/GetServerInfo
	}

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Pass the discoveryHeaders (which might include Authorization) to NewSession
	mcpSession := mcpClientInstance.NewSession(sessionCtx,
		mcpClient.WithHTTPClient(httpClient),
		mcpClient.WithHeaders(discoveryHeaders))
	defer mcpSession.Close()

	// GetServerInfo implicitly calls Open() and uses headers set during NewSession
	serverInfoResultChan := mcpSession.GetServerInfo(ctx) // Use parent context for the call itself

	select {
	case serverInfoResult := <-serverInfoResultChan:
		if serverInfoResult.Err != nil {
			logger.Debug("MCP handshake failed", zap.Error(serverInfoResult.Err))
			return nil, fmt.Errorf("MCP handshake failed: %w", serverInfoResult.Err)
		}
		logger.Info("MCP detected via successful handshake", zap.String("url", targetURL))

		// Fetch tools
		toolsCtx, toolsCancel := context.WithTimeout(ctx, 500*time.Second) // Separate short timeout for tools fetch
		defer toolsCancel()
		toolsResultChan := mcpSession.GetTools(toolsCtx)
		result := &DiscoveryResult{
			ServerInfo: clients.ServerInfo{
				URL:             targetURL, // Use original target URL
				Name:            serverInfoResult.ServerInfo.Name,
				Version:         serverInfoResult.ServerInfo.Version,
				Protocol:        clients.ServerTypeMCP,
				ProtocolVersion: schema.PROTOCOL_VERSION, // Assuming latest if handshake succeeds
			},
		}

		select {
		case toolsResult := <-toolsResultChan:
			if toolsResult.Err != nil {
				logger.Warn("MCP server detected, but failed to fetch tools", zap.Error(toolsResult.Err))
			} else {
				result.MCPTools = toolsResult.Tools
				logger.Debug("MCP tools fetched successfully", zap.Int("count", len(result.MCPTools)))
			}
		case <-toolsCtx.Done():
			logger.Warn("MCP server detected, but timed out fetching tools", zap.Error(toolsCtx.Err()))
		}
		return result, nil

	case <-ctx.Done():
		return nil, fmt.Errorf("MCP discovery timed out or cancelled: %w", ctx.Err())
	}
}
