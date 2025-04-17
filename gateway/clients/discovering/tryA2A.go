package discovering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"go.uber.org/zap"

	"github.com/gate4ai/gate4ai/gateway/clients"
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
)

// tryA2ADiscovery attempts to discover if the target URL hosts an A2A server
// by checking for the /.well-known/agent.json endpoint.
func tryA2ADiscovery(ctx context.Context, targetURL string, httpClient *http.Client, logger *zap.Logger) (*DiscoveryResult, error) {
	logger.Debug("Attempting A2A discovery", zap.String("url", targetURL))

	// Construct the well-known URL
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	// Build the .well-known URL based on the *origin* of the target URL
	// Example: if targetURL is https://api.example.com/some/path
	// wellKnownURL should be https://api.example.com/.well-known/agent.json
	wellKnownURL := fmt.Sprintf("%s://%s/.well-known/agent.json", parsedURL.Scheme, parsedURL.Host)

	req, err := http.NewRequestWithContext(ctx, "GET", wellKnownURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create A2A discovery request: %w", err)
	}
	req.Header.Set("Accept", "application/json") // Set appropriate accept header

	resp, err := httpClient.Do(req)
	if err != nil {
		// Network error or timeout
		return nil, fmt.Errorf("A2A discovery request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check if the endpoint exists and returns success (e.g., 200 OK)
	if resp.StatusCode == http.StatusOK {
		// Try to read and parse the agent.json to extract information
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read A2A agent card: %w", readErr)
		}

		// Try to parse the agent card
		var agentCard a2aSchema.AgentCard
		if jsonErr := json.Unmarshal(body, &agentCard); jsonErr != nil {
			return nil, fmt.Errorf("failed to parse A2A agent card: %w", jsonErr)
		}

		// Successfully parsed agent card - create a result with full information
		result := &DiscoveryResult{
			ServerInfo: clients.ServerInfo{
				URL:             agentCard.URL,                                   // Use URL from agent card as specified in ServerInfo comments
				Name:            agentCard.Name,                                  // Map from AgentCard.Name
				Version:         agentCard.Version,                               // Map from AgentCard.Version
				Description:     shared.StringPtrToString(agentCard.Description), // Map from AgentCard.Description
				Website:         getWebsiteFromProvider(agentCard.Provider),      // Map from AgentCard.Provider.URL
				Protocol:        clients.ServerTypeA2A,
				ProtocolVersion: "2025-draft",
			},
			A2ASkills: agentCard.Skills, // Include skills in the discovery result
		}

		logger.Info("A2A detected via /.well-known/agent.json", zap.String("url", wellKnownURL))
		return result, nil
	}

	// Endpoint doesn't exist or returned an error status
	return nil, fmt.Errorf("A2A discovery failed: status code %d for %s", resp.StatusCode, wellKnownURL)
}

// Helper function to extract website from provider
func getWebsiteFromProvider(provider *a2aSchema.AgentProvider) *string {
	if provider == nil {
		return nil
	}
	return provider.URL
}
