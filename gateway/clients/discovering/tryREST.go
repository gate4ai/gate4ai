package discovering

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gate4ai/gate4ai/gateway/clients"
	"go.uber.org/zap"
)

// tryRESTDiscovery attempts REST/OpenAPI discovery.
// Now accepts discoveryHeaders map.
func tryRESTDiscovery(ctx context.Context, targetURL string, httpClient *http.Client, discoveryHeaders map[string]string, logger *zap.Logger) (*DiscoveryResult, error) {
	logger.Debug("Attempting REST/OpenAPI discovery", zap.String("url", targetURL), zap.Int("headerCount", len(discoveryHeaders)))

	baseParsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL: %w", err)
	}

	commonPaths := []string{
		"/openapi.json", "/swagger.json", "/swagger/v1/swagger.json",
		"/v3/api-docs", "/docs", "/swagger", "/swagger-ui.html", "/redoc",
	}
	originURL := fmt.Sprintf("%s://%s", baseParsedURL.Scheme, baseParsedURL.Host)

	for _, path := range commonPaths {
		checkURL := originURL + path
		logger.Debug("Checking REST path", zap.String("checkURL", checkURL))

		req, err := http.NewRequestWithContext(ctx, "GET", checkURL, nil)
		if err != nil {
			logger.Warn("Failed to create REST discovery request", zap.Error(err))
			continue
		}

		// Set discovery headers
		req.Header.Set("Accept", "application/json, text/html, */*")
		for key, value := range discoveryHeaders {
			req.Header.Set(key, value)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			logger.Debug("REST discovery request failed for path", zap.Error(err))
			continue
		}

		_, _ = io.CopyN(io.Discard, resp.Body, 1024) // Read max 1KB
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			logger.Info("REST/OpenAPI likely detected", zap.String("path", path), zap.Int("statusCode", resp.StatusCode))
			result := &DiscoveryResult{
				ServerInfo: clients.ServerInfo{
					URL:             targetURL, // Use original target URL
					Name:            getServerNameFromPath(path),
					Protocol:        clients.ServerTypeREST,
					ProtocolVersion: getOpenAPIVersionFromPath(path),
				},
			}
			return result, nil
		}
		logger.Debug("REST check failed for path", zap.String("path", path), zap.Int("statusCode", resp.StatusCode))
	}

	return nil, fmt.Errorf("no common REST/OpenAPI paths found or accessible")
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
