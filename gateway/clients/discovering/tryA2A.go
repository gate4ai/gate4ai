package discovering

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/zap"

	"github.com/gate4ai/gate4ai/gateway/clients"
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
)

// tryA2ADiscovery attempts A2A discovery by checking /.well-known/agent.json.
// It accepts a unique stepID to correlate log entries.
// Sends log updates via logChan.
func tryA2ADiscovery(
	ctx context.Context,
	stepID string, // Unique ID for this discovery attempt
	targetURL string,
	httpClient *http.Client,
	discoveryHeaders map[string]string,
	logChan chan<- DiscoveryLogEntry,
	logger *zap.Logger,
) (*DiscoveryResult, error) {
	protocol := "A2A"
	step := "GET /.well-known/agent.json"
	logger = logger.With(zap.String("stepId", stepID)) // Add stepId to logger
	logger.Debug("Attempting A2A discovery", zap.String("url", targetURL))

	// Send initial attempt log entry
	sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
		StepID:    stepID,
		Timestamp: time.Now(),
		Protocol:  protocol,
		Method:    "GET",
		Step:      step,
		Status:    "attempting",
	})

	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		finalErr := fmt.Errorf("invalid target URL: %w", err)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "ParseURL",
			Step:      "Initial Parse",
			Status:    "error",
			Details:   &LogDetails{Type: "Configuration", Message: finalErr.Error()},
		})
		return nil, finalErr
	}
	wellKnownURL := fmt.Sprintf("%s://%s/.well-known/agent.json", parsedURL.Scheme, parsedURL.Host)

	req, err := http.NewRequestWithContext(ctx, "GET", wellKnownURL, nil)
	if err != nil {
		finalErr := fmt.Errorf("failed to create A2A discovery request: %w", err)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      step,
			URL:       wellKnownURL,
			Status:    "error",
			Details:   &LogDetails{Type: "RequestCreation", Message: finalErr.Error()},
		})
		return nil, finalErr
	}

	req.Header.Set("Accept", "application/json")
	for key, value := range discoveryHeaders {
		req.Header.Set(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		details := &LogDetails{Message: err.Error()}
		if errors.Is(err, context.DeadlineExceeded) {
			details.Type = "Timeout"
		} else if urlErr, ok := err.(*url.Error); ok && urlErr.Timeout() {
			details.Type = "Timeout"
		} else {
			details.Type = "Connection" // General connection error (includes DNS errors like "no such host")
		}
		finalErr := fmt.Errorf("A2A discovery request failed: %w", err)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      step,
			URL:       wellKnownURL,
			Status:    "error",
			Details:   details,
		})
		return nil, finalErr
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*10)) // Limit read size
	if readErr != nil {
		finalErr := fmt.Errorf("failed to read A2A agent card response body: %w", readErr)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      step,
			URL:       wellKnownURL,
			Status:    "error",
			Details:   &LogDetails{Type: "ReadBody", StatusCode: &resp.StatusCode, Message: finalErr.Error()},
		})
		return nil, finalErr
	}

	if resp.StatusCode != http.StatusOK {
		preview := string(body)
		if len(preview) > 1000 {
			preview = preview[:1000] + "..."
		}
		finalErr := fmt.Errorf("A2A discovery failed: status code %d for %s", resp.StatusCode, wellKnownURL)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      step,
			URL:       wellKnownURL,
			Status:    "error",
			Details:   &LogDetails{Type: "HTTP", StatusCode: &resp.StatusCode, Message: fmt.Sprintf("Received status %d", resp.StatusCode), ResponseBodyPreview: preview},
		})
		return nil, finalErr
	}

	// Attempt to parse the successful response
	var agentCard a2aSchema.AgentCard
	if jsonErr := json.Unmarshal(body, &agentCard); jsonErr != nil {
		preview := string(body)
		if len(preview) > 1000 {
			preview = preview[:1000] + "..."
		}
		finalErr := fmt.Errorf("failed parsing agent card JSON from %s: %w", wellKnownURL, jsonErr)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      step,
			URL:       wellKnownURL,
			Status:    "error",
			Details:   &LogDetails{Type: "Parse", StatusCode: &resp.StatusCode, Message: finalErr.Error(), ResponseBodyPreview: preview},
		})
		return nil, finalErr // Indicate parsing failure
	}

	// Basic validation after successful parsing
	if agentCard.Name == "" || agentCard.URL == "" || agentCard.Version == "" {
		finalErr := errors.New("invalid AgentCard received: missing required fields (name, url, version)")
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      step,
			URL:       wellKnownURL,
			Status:    "error",
			Details:   &LogDetails{Type: "Validation", StatusCode: &resp.StatusCode, Message: finalErr.Error()},
		})
		return nil, finalErr
	}

	// Log success
	successMsg := fmt.Sprintf("Found Agent: %s v%s", agentCard.Name, agentCard.Version)
	sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
		StepID:    stepID,
		Timestamp: time.Now(),
		Protocol:  protocol,
		Method:    "GET",
		Step:      step,
		URL:       wellKnownURL,
		Status:    "success",
		Details:   &LogDetails{Message: successMsg},
	})
	logger.Info("A2A detected via /.well-known/agent.json", zap.String("url", wellKnownURL))

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
	return result, nil
}

// getWebsiteFromProvider remains the same.
func getWebsiteFromProvider(provider *a2aSchema.AgentProvider) *string {
	if provider == nil {
		return nil
	}
	return provider.URL
}

// sendDiscoveryLog is a helper to safely send log entries on the channel.
func sendDiscoveryLog(logChan chan<- DiscoveryLogEntry, logger *zap.Logger, entry DiscoveryLogEntry) {
	// Ensure StepID is set, although it should be passed in
	if entry.StepID == "" {
		logger.Error("Attempted to send log entry without StepID", zap.Any("entry", entry))
		return
	}
	select {
	case logChan <- entry:
		// Log sent
	default:
		// Log channel is full or closed, log this issue
		logger.Warn("Log channel full or closed, could not send discovery log entry", zap.String("stepId", entry.StepID), zap.String("status", entry.Status))
	}
}