package discovering

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/gateway/client"
	"github.com/gate4ai/mcp/shared"
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// tryMCPDiscovery attempts to discover if the target URL hosts an MCP server.
func tryMCPDiscovery(ctx context.Context, targetURL string, authBearer string, logger *zap.Logger) (*MCPInfo, error) {
	logger.Debug("Attempting MCP discovery", zap.String("url", targetURL))

	// Create a new MCP client for the target URL
	mcpClient, err := client.New(shared.RandomID(), targetURL, logger.Named("mcp-discover-client"))
	if err != nil {
		return nil, fmt.Errorf("failed to create MCP client for discovery: %w", err)
	}

	// Use a default HTTP client with a reasonable timeout for discovery
	httpClient := &http.Client{
		Timeout: 5 * time.Second, // Shorter timeout for discovery handshake
	}

	// Create a new session with a derived context that includes the overall timeout
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel() // Ensure session context is cancelled when this function returns

	mcpSession := mcpClient.NewSession(sessionCtx, httpClient, authBearer)
	defer mcpSession.Close() // Ensure session resources are cleaned up

	// Attempt to get server info. This implicitly calls Open() and performs the handshake.
	// Use the parent context (ctx) for the GetServerInfo call itself.
	serverInfoResultChan := mcpSession.GetServerInfo(ctx)

	// Wait for the result or timeout/cancellation from the parent context
	select {
	case serverInfoResult := <-serverInfoResultChan:
		if serverInfoResult.Err != nil {
			// Handshake failed (e.g., invalid URL, auth error, non-MCP server, timeout)
			return nil, fmt.Errorf("MCP handshake failed: %w", serverInfoResult.Err)
		}
		// MCP Handshake successful!

		// Now, fetch tools (optional, but useful for the dialog)
		toolsResultChan := mcpSession.GetTools(ctx)
		mcpInfo := &MCPInfo{
			ServerInfo: serverInfoResult.ServerInfo,
			Tools:      []schema.Tool{}, // Initialize empty slice
		}

		// Wait for tools result or timeout/cancellation
		select {
		case toolsResult := <-toolsResultChan:
			if toolsResult.Err != nil {
				logger.Warn("MCP server detected, but failed to fetch tools", zap.Error(toolsResult.Err))
				// Return success but without tools
			} else {
				mcpInfo.Tools = toolsResult.Tools
			}
			return mcpInfo, nil // Return success (with or without tools)
		case <-ctx.Done():
			logger.Warn("MCP server detected, but timed out fetching tools", zap.Error(ctx.Err()))
			return mcpInfo, nil // Return success but without tools due to timeout
		}

	case <-ctx.Done():
		// Parent context cancelled/timed out before handshake completed
		return nil, fmt.Errorf("MCP discovery timed out or was cancelled: %w", ctx.Err())
	}
}
