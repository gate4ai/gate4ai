package a2aClient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema" // Use A2A schema
	"go.uber.org/zap"
)

// AgentInfo holds the discovered information about an A2A agent from its AgentCard.
type AgentInfo struct {
	a2aSchema.AgentCard // Embed the AgentCard structure directly
}

// FetchAgentCard retrieves the AgentCard JSON from the standard /.well-known path.
func FetchAgentCard(ctx context.Context, baseURL string, httpClient *http.Client, logger *zap.Logger) (*AgentInfo, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	// Construct the well-known URL
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	// Ensure path is handled correctly, joining with the base URL's path if necessary
	// For /.well-known, it's usually relative to the host root.
	wellKnownURL := fmt.Sprintf("%s://%s/.well-known/agent.json", parsedURL.Scheme, parsedURL.Host)

	logger.Debug("Fetching AgentCard", zap.String("url", wellKnownURL))

	req, err := http.NewRequestWithContext(ctx, "GET", wellKnownURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create AgentCard request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch AgentCard from %s: %w", wellKnownURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch AgentCard from %s: status code %d", wellKnownURL, resp.StatusCode)
	}

	var agentCard a2aSchema.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&agentCard); err != nil {
		return nil, fmt.Errorf("failed to parse AgentCard JSON from %s: %w", wellKnownURL, err)
	}

	// Basic validation
	if agentCard.Name == "" || agentCard.URL == "" || agentCard.Version == "" {
		return nil, fmt.Errorf("invalid AgentCard received: missing required fields (name, url, version)")
	}

	// Ensure URL in card is absolute or resolve relative to base
	cardURLParsed, err := url.Parse(agentCard.URL)
	if err != nil {
		logger.Warn("AgentCard URL is invalid, using provided base URL", zap.String("cardURL", agentCard.URL), zap.String("baseURL", baseURL))
		agentCard.URL = baseURL // Fallback to provided base URL
	} else if !cardURLParsed.IsAbs() {
		resolvedURL := parsedURL.ResolveReference(cardURLParsed)
		logger.Debug("Resolved relative AgentCard URL", zap.String("original", agentCard.URL), zap.String("resolved", resolvedURL.String()))
		agentCard.URL = resolvedURL.String()
	}

	// Set defaults if Modes are empty (as per schema spec)
	if len(agentCard.DefaultInputModes) == 0 {
		agentCard.DefaultInputModes = []string{"text"}
	}
	if len(agentCard.DefaultOutputModes) == 0 {
		agentCard.DefaultOutputModes = []string{"text"}
	}

	logger.Info("Successfully fetched and parsed AgentCard", zap.String("agentName", agentCard.Name), zap.String("agentVersion", agentCard.Version))
	return &AgentInfo{AgentCard: agentCard}, nil
}
