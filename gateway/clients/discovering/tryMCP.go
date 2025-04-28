package discovering

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients"
	"github.com/gate4ai/gate4ai/gateway/clients/mcpClient"
	"github.com/gate4ai/gate4ai/shared"
	schema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// tryMCPDiscovery attempts to discover if the target URL hosts an MCP server.
// It accepts a unique stepID to correlate log entries.
// Sends log updates via logChan.
func tryMCPDiscovery(
	ctx context.Context,
	stepID string, // Unique ID for this discovery attempt
	targetURL string,
	discoveryHeaders map[string]string,
	logChan chan<- DiscoveryLogEntry,
	logger *zap.Logger,
) (*DiscoveryResult, error) {
	protocol := "MCP"
	step := "Handshake"
	logger = logger.With(zap.String("stepId", stepID)) // Add stepId to logger
	logger.Debug("Attempting MCP discovery", zap.String("url", targetURL))

	// Send initial attempt log entry
	sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
		StepID:    stepID,
		Timestamp: time.Now(),
		Protocol:  protocol,
		Method:    "SSE/POST",
		Step:      step,
		URL:       targetURL,
		Status:    "attempting",
	})

	mcpClientInstance, err := mcpClient.New(shared.RandomID(), targetURL, logger.Named("mcp-discover-client"))
	if err != nil {
		finalErr := fmt.Errorf("failed to create MCP client for discovery: %w", err)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "Internal",
			Step:      "Client Creation",
			Status:    "error",
			Details:   &LogDetails{Type: "Configuration", Message: finalErr.Error()},
		})
		return nil, finalErr
	}

	httpClient := &http.Client{} // Context handled by session

	// Create a context specifically for the session handshake attempt, respecting the overall context
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel() // Ensure cancellation propagates

	// Pass the discoveryHeaders (which might include Authorization) to NewSession
	mcpSession := mcpClientInstance.NewSession(sessionCtx,
		mcpClient.WithHTTPClient(httpClient),
		mcpClient.WithHeaders(discoveryHeaders))
	defer mcpSession.Close() // Ensure session resources are cleaned up

	// GetServerInfo implicitly calls Open() which performs the handshake
	serverInfoResultChan := mcpSession.GetServerInfo(sessionCtx) // Use sessionCtx

	var finalErr error
	var finalResult *DiscoveryResult = nil

	select {
	case serverInfoResult := <-serverInfoResultChan:
		if serverInfoResult.Err != nil {
			finalErr = fmt.Errorf("MCP handshake failed: %w", serverInfoResult.Err)
			details := &LogDetails{Message: finalErr.Error()}
			// Try to determine error type based on message
			if errors.Is(serverInfoResult.Err, context.Canceled) || errors.Is(serverInfoResult.Err, context.DeadlineExceeded) {
				details.Type = "Timeout" // or Canceled
			} else if strings.Contains(strings.ToLower(serverInfoResult.Err.Error()), "unauthorized") || strings.Contains(strings.ToLower(serverInfoResult.Err.Error()), "status code 401") {
				details.Type = "HTTP"
				statusCode := 401
				details.StatusCode = &statusCode
			} else {
				// Could be connection refused, DNS error (no such host), protocol mismatch etc.
				details.Type = "Connection/Protocol"
			}
			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    stepID,
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "SSE/POST",
				Step:      step,
				URL:       targetURL,
				Status:    "error",
				Details:   details,
			})
			// finalErr is set, will return nil result and the error
		} else {
			// Handshake successful
			serverInfo := serverInfoResult.ServerInfo
			successMsg := fmt.Sprintf("Handshake OK. Name: %s, Version: %s", serverInfo.Name, serverInfo.Version)
			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    stepID,
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "SSE/POST",
				Step:      step,
				URL:       targetURL,
				Status:    "success",
				Details:   &LogDetails{Message: successMsg},
			})
			logger.Info("MCP detected via successful handshake", zap.String("url", targetURL))

			// Prepare final result structure immediately after handshake success
			finalResult = &DiscoveryResult{
				ServerInfo: clients.ServerInfo{
					URL:             targetURL, // Use original target URL
					Name:            serverInfo.Name,
					Version:         serverInfo.Version,
					Protocol:        clients.ServerTypeMCP,
					ProtocolVersion: schema.PROTOCOL_VERSION, // Assuming latest if handshake succeeds
				},
			}

			// Fetch tools (optional, best effort) - This needs its own step tracking
			toolsStepID := fmt.Sprintf("%s-tools", stepID)
			toolsStep := "Get Tools"
			toolsCtx, toolsCancel := context.WithTimeout(ctx, 5*time.Second) // Short timeout for tools
			defer toolsCancel()

			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    toolsStepID, // New ID for this sub-step
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "Request",
				Step:      toolsStep,
				URL:       targetURL,
				Status:    "attempting",
			})

			toolsResultChan := mcpSession.GetTools(toolsCtx)
			select {
			case toolsResult := <-toolsResultChan:
				if toolsResult.Err != nil {
					errMsg := fmt.Sprintf("MCP server detected, but failed to fetch tools: %v", toolsResult.Err)
					logger.Warn(errMsg)
					sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
						StepID:    toolsStepID,
						Timestamp: time.Now(),
						Protocol:  protocol,
						Method:    "Request",
						Step:      toolsStep,
						URL:       targetURL,
						Status:    "error",
						Details:   &LogDetails{Type: "MCP Request", Message: errMsg},
					})
					// Continue without tools, finalResult is already prepared
				} else {
					finalResult.MCPTools = toolsResult.Tools // Add tools to the prepared result
					logMsg := fmt.Sprintf("Fetched %d tools", len(finalResult.MCPTools))
					logger.Debug("MCP tools fetched successfully", zap.Int("count", len(finalResult.MCPTools)))
					sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
						StepID:    toolsStepID,
						Timestamp: time.Now(),
						Protocol:  protocol,
						Method:    "Request",
						Step:      toolsStep,
						URL:       targetURL,
						Status:    "success",
						Details:   &LogDetails{Message: logMsg},
					})
				}
			case <-toolsCtx.Done():
				errMsg := fmt.Sprintf("MCP server detected, but timed out fetching tools: %v", toolsCtx.Err())
				logger.Warn(errMsg)
				sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
					StepID:    toolsStepID,
					Timestamp: time.Now(),
					Protocol:  protocol,
					Method:    "Request",
					Step:      toolsStep,
					URL:       targetURL,
					Status:    "error",
					Details:   &LogDetails{Type: "Timeout", Message: errMsg},
				})
				// Continue without tools
			}
			// Handshake was successful, so no error overall for MCP detection
			finalErr = nil
		}

	case <-ctx.Done(): // Overall context timeout/cancel
		finalErr = fmt.Errorf("MCP discovery timed out or cancelled: %w", ctx.Err())
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "SSE/POST",
			Step:      step,
			URL:       targetURL,
			Status:    "error",
			Details:   &LogDetails{Type: "Timeout", Message: finalErr.Error()},
		})
	}

	return finalResult, finalErr
}