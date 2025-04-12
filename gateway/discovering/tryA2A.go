package discovering

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"go.uber.org/zap"
)

// tryA2ADiscovery attempts to discover if the target URL hosts an A2A server
// by checking for the /.well-known/agent.json endpoint.
func tryA2ADiscovery(ctx context.Context, targetURL string, httpClient *http.Client, logger *zap.Logger) (*A2AInfo, error) {
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
		// Optionally, you could try to read and parse the agent.json here
		// to extract more information if needed.
		_, readErr := io.ReadAll(resp.Body) // Read body to check validity, but discard for now
		if readErr != nil {
			logger.Warn("Found agent.json but failed to read body", zap.String("url", wellKnownURL), zap.Error(readErr))
			// Proceed considering it A2A, but log the read error
		}
		logger.Info("A2A detected via /.well-known/agent.json", zap.String("url", wellKnownURL))
		return &A2AInfo{
			AgentJsonUrl: wellKnownURL,
		}, nil
	}

	// Endpoint doesn't exist or returned an error status
	return nil, fmt.Errorf("A2A discovery failed: status code %d for %s", resp.StatusCode, wellKnownURL)
}
