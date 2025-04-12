package discovering

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.uber.org/zap"
)

// tryRESTDiscovery attempts to discover if the target URL hosts a REST/OpenAPI server
// by checking for common OpenAPI definition file paths (/openapi.json, /docs, etc.).
func tryRESTDiscovery(ctx context.Context, targetURL string, httpClient *http.Client, logger *zap.Logger) (*RESTInfo, error) {
	logger.Debug("Attempting REST/OpenAPI discovery", zap.String("url", targetURL))

	baseParsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	// Common paths to check for OpenAPI definitions or Swagger UI
	commonPaths := []string{
		"/openapi.json",
		"/swagger.json",
		"/swagger/v1/swagger.json", // Common ASP.NET Core pattern
		"/v3/api-docs",             // Common Spring Boot pattern
		"/docs",                    // Often redirects to Swagger UI
		"/swagger",                 // Common base for Swagger UI
		"/swagger-ui.html",         // Specific Swagger UI file
		"/redoc",                   // Common path for ReDoc UI
	}

	// Try fetching each common path relative to the *origin* of the target URL
	originURL := fmt.Sprintf("%s://%s", baseParsedURL.Scheme, baseParsedURL.Host)

	for _, path := range commonPaths {
		checkURL := originURL + path
		logger.Debug("Checking REST path", zap.String("checkURL", checkURL))

		req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
		if err != nil {
			logger.Warn("Failed to create REST discovery request", zap.String("checkURL", checkURL), zap.Error(err))
			continue // Try next path
		}
		// Set Accept header, prioritize JSON but allow HTML for UI pages
		req.Header.Set("Accept", "application/json, text/html, */*")

		resp, err := httpClient.Do(req)
		if err != nil {
			// Network error or timeout for this specific path, try next
			logger.Debug("REST discovery request failed for path", zap.String("path", path), zap.Error(err))
			continue
		}

		// Read a small part of the body to check content type later if needed, but close it
		_, _ = io.CopyN(io.Discard, resp.Body, 1024) // Read max 1KB
		resp.Body.Close()

		// Check for success status (2xx or 3xx redirects which might lead to UI)
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			logger.Info("REST/OpenAPI likely detected", zap.String("path", path), zap.Int("statusCode", resp.StatusCode))
			restInfo := &RESTInfo{}
			// Distinguish between JSON file and potential UI based on path/content-type (optional)
			if strings.HasSuffix(path, ".json") {
				restInfo.OpenApiJsonUrl = checkURL
			} else {
				// Assume it might be a Swagger/ReDoc UI path
				restInfo.SwaggerUrl = checkURL
			}
			return restInfo, nil
		}
		logger.Debug("REST check failed for path", zap.String("path", path), zap.Int("statusCode", resp.StatusCode))
	}

	// If none of the common paths returned success
	return nil, fmt.Errorf("no common REST/OpenAPI paths found or accessible")
}
