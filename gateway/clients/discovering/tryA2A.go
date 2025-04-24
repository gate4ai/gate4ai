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

// tryA2ADiscovery attempts A2A discovery by checking /.well-known/agent.json.
// Now accepts discoveryHeaders map.
func tryA2ADiscovery(ctx context.Context, targetURL string, httpClient *http.Client, discoveryHeaders map[string]string, logger *zap.Logger) (*DiscoveryResult, error) {
	logger.Debug("Attempting A2A discovery", zap.String("url", targetURL), zap.Int("headerCount", len(discoveryHeaders)))

	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}
	wellKnownURL := fmt.Sprintf("%s://%s/.well-known/agent.json", parsedURL.Scheme, parsedURL.Host)

	req, err := http.NewRequestWithContext(ctx, "GET", wellKnownURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create A2A discovery request: %w", err)
	}

	// Set discovery headers
	req.Header.Set("Accept", "application/json")
	for key, value := range discoveryHeaders {
		req.Header.Set(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("A2A discovery request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read A2A agent card: %w", readErr)
		}

		var agentCard a2aSchema.AgentCard
		if jsonErr := json.Unmarshal(body, &agentCard); jsonErr != nil {
			// Don't return error immediately, could be a non-A2A server
			logger.Debug("Failed to parse A2A agent card", zap.Error(jsonErr))
			return nil, fmt.Errorf("failed parsing agent card: %w", jsonErr) // Indicate parsing failure
		}

		// Successfully parsed agent card
		result := &DiscoveryResult{
			ServerInfo: clients.ServerInfo{
				URL:             agentCard.URL, // Use URL from card
				Name:            agentCard.Name,
				Version:         agentCard.Version,
				Description:     shared.StringPtrToString(agentCard.Description),
				Website:         getWebsiteFromProvider(agentCard.Provider),
				Protocol:        clients.ServerTypeA2A,
				ProtocolVersion: "2025-draft", // Hardcoded for now
			},
			A2ASkills: agentCard.Skills,
		}
		logger.Info("A2A detected via /.well-known/agent.json", zap.String("url", wellKnownURL))
		return result, nil
	}

	return nil, fmt.Errorf("A2A discovery failed: status code %d for %s", resp.StatusCode, wellKnownURL)
}

// getWebsiteFromProvider remains the same.
func getWebsiteFromProvider(provider *a2aSchema.AgentProvider) *string {
	if provider == nil {
		return nil
	}
	return provider.URL
}
