package discovering

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients"
	"go.uber.org/zap"
)

// tryRESTDiscovery attempts REST/OpenAPI discovery by checking common paths.
// It accepts a unique stepID to correlate log entries for the overall REST attempt.
// Sends log updates via logChan.
func tryRESTDiscovery(
	ctx context.Context,
	stepID string, // Unique ID for this overall REST discovery attempt
	targetURL string,
	httpClient *http.Client,
	discoveryHeaders map[string]string,
	logChan chan<- DiscoveryLogEntry,
	logger *zap.Logger,
) (*DiscoveryResult, error) {
	protocol := "REST"
	logger = logger.With(zap.String("stepId", stepID)) // Add stepId to logger
	logger.Debug("Attempting REST/OpenAPI discovery", zap.String("url", targetURL))

	// Send overall attempt log entry
	sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
		StepID:    stepID,
		Timestamp: time.Now(),
		Protocol:  protocol,
		Method:    "GET",
		Step:      "Overall Check",
		URL:       targetURL,
		Status:    "attempting",
	})

	baseParsedURL, err := url.Parse(targetURL)
	if err != nil {
		finalErr := fmt.Errorf("invalid target URL: %w", err)
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID, // Use overall stepID for the final error
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "ParseURL",
			Step:      "Initial Parse",
			Status:    "error",
			Details:   &LogDetails{Type: "Configuration", Message: finalErr.Error()},
		})
		return nil, finalErr
	}

	// Removed paths that are less likely to indicate a machine-readable API endpoint
	commonPaths := []string{
		"/openapi.json", "/swagger.json", "/swagger/v1/swagger.json", "/v3/api-docs",
	}
	originURL := fmt.Sprintf("%s://%s", baseParsedURL.Scheme, baseParsedURL.Host)

	var firstSuccessfulResult *DiscoveryResult = nil
	var lastError error = nil

	for _, path := range commonPaths {
		// Generate a unique ID for *this specific path check* within the overall REST attempt
		pathStepID := fmt.Sprintf("%s-%s", stepID, strings.ReplaceAll(strings.Trim(path, "/"), "/", "-"))
		pathStep := fmt.Sprintf("GET %s", path)
		checkURL := originURL + path
		pathLogger := logger.With(zap.String("pathStepId", pathStepID), zap.String("checkURL", checkURL))
		pathLogger.Debug("Checking REST path")

		// Log attempt for this specific path
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    pathStepID,
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      pathStep,
			URL:       checkURL,
			Status:    "attempting",
		})

		req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to create REST discovery request for %s: %v", path, err)
			pathLogger.Warn(errMsg)
			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    pathStepID,
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "GET",
				Step:      pathStep,
				URL:       checkURL,
				Status:    "error",
				Details:   &LogDetails{Type: "RequestCreation", Message: errMsg},
			})
			lastError = errors.New(errMsg) // Keep track of the last error
			continue                        // Try next path
		}

		req.Header.Set("Accept", "application/json, */*") // Primarily look for JSON
		for key, value := range discoveryHeaders {
			req.Header.Set(key, value)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			errMsg := fmt.Sprintf("REST discovery request failed for %s: %v", path, err)
			pathLogger.Debug(errMsg)
			details := &LogDetails{Message: errMsg}
			if errors.Is(err, context.DeadlineExceeded) {
				details.Type = "Timeout"
			} else if urlErr, ok := err.(*url.Error); ok && urlErr.Timeout() {
				details.Type = "Timeout"
			} else {
				details.Type = "Connection" // Includes DNS errors
			}
			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    pathStepID,
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "GET",
				Step:      pathStep,
				URL:       checkURL,
				Status:    "error",
				Details:   details,
			})
			lastError = errors.New(errMsg)
			continue // Try next path
		}

		// Read limited body for preview in case of non-2xx status
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*10)) // Limit read to 10KB
		resp.Body.Close()                                              // Close body immediately after read

		if readErr != nil {
			errMsg := fmt.Sprintf("Failed to read response body for %s: %v", path, readErr)
			pathLogger.Warn(errMsg)
			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    pathStepID,
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "GET",
				Step:      pathStep,
				URL:       checkURL,
				Status:    "error",
				Details:   &LogDetails{Type: "ReadBody", StatusCode: &resp.StatusCode, Message: errMsg},
			})
			lastError = errors.New(errMsg)
			continue // Try next path
		}

		preview := string(body)
		if len(preview) > 1000 {
			preview = preview[:1000] + "..."
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success! Found a potential REST definition
			successMsg := fmt.Sprintf("Found likely REST/OpenAPI definition at %s (Status: %d)", path, resp.StatusCode)
			pathLogger.Info("REST/OpenAPI likely detected", zap.String("path", path), zap.Int("statusCode", resp.StatusCode))
			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    pathStepID,
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "GET",
				Step:      pathStep,
				URL:       checkURL,
				Status:    "success",
				Details:   &LogDetails{Message: successMsg, StatusCode: &resp.StatusCode},
			})

			firstSuccessfulResult = &DiscoveryResult{
				ServerInfo: clients.ServerInfo{
					URL:             targetURL, // Use original target URL provided by user
					Name:            getServerNameFromPath(path),
					Protocol:        clients.ServerTypeREST,
					ProtocolVersion: getOpenAPIVersionFromPath(path),
				},
			}
			// Don't break here, let other path checks complete to send their logs.
			// We'll prioritize this result later.
		} else {
			// Log non-success HTTP status as an error for this path attempt
			errMsg := fmt.Sprintf("REST check failed for %s: status code %d", path, resp.StatusCode)
			pathLogger.Debug(errMsg)
			sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
				StepID:    pathStepID,
				Timestamp: time.Now(),
				Protocol:  protocol,
				Method:    "GET",
				Step:      pathStep,
				URL:       checkURL,
				Status:    "error",
				Details:   &LogDetails{Type: "HTTP", StatusCode: &resp.StatusCode, Message: fmt.Sprintf("Received status %d", resp.StatusCode), ResponseBodyPreview: preview},
			})
			lastError = errors.New(errMsg) // Track last error
			// Continue to the next path
		}
	} // End of path loop

	// After checking all paths, determine the final outcome for the overall REST step
	if firstSuccessfulResult != nil {
		// Log overall success for the REST check
		sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
			StepID:    stepID, // Use overall stepID
			Timestamp: time.Now(),
			Protocol:  protocol,
			Method:    "GET",
			Step:      "Overall Check Result",
			URL:       targetURL,
			Status:    "success",
			Details:   &LogDetails{Message: "Found a potential REST endpoint."},
		})
		return firstSuccessfulResult, nil // Return the first success found
	}

	// If loop completes without success
	finalErrMsg := "no common REST/OpenAPI paths found or accessible"
	sendDiscoveryLog(logChan, logger, DiscoveryLogEntry{
		StepID:    stepID, // Use overall stepID
		Timestamp: time.Now(),
		Protocol:  protocol,
		Method:    "GET",
		Step:      "Overall Check Result",
		URL:       targetURL,
		Status:    "error",
		Details:   &LogDetails{Type: "NotFound", Message: finalErrMsg},
	})
	if lastError != nil {
		// Return the last specific error encountered if available
		return nil, lastError
	}
	return nil, errors.New(finalErrMsg)
}

// Helper functions getServerNameFromPath, getOpenAPIVersionFromPath remain the same.
func getServerNameFromPath(path string) string {
	if strings.Contains(path, "swagger") {
		return "Swagger API"
	}
	if strings.Contains(path, "openapi") {
		return "OpenAPI Service"
	}
	if strings.Contains(path, "redoc") {
		return "ReDoc API"
	}
	if strings.Contains(path, "api-docs") {
		return "API Documentation Service"
	}
	return "REST API Service"
}
func getOpenAPIVersionFromPath(path string) string {
	if strings.Contains(path, "v3") {
		return "OpenAPI 3.0"
	}
	if strings.Contains(path, "v2") {
		return "OpenAPI/Swagger 2.0"
	}
	return "REST/OpenAPI"
}