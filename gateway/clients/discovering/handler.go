package discovering

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients" // Assuming MCP info is based on latest schema
	"go.uber.org/zap"

	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	mcpSchema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
)

// DiscoveryResult holds the result of a discovery check.
type DiscoveryResult struct {
	clients.ServerInfo
	MCPTools  []mcpSchema.Tool       `json:"mcpTools,omitempty"`
	A2ASkills []a2aSchema.AgentSkill `json:"a2aSkills,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

// Handler creates an HTTP handler for discovering server type and basic info.
func Handler(logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handlerLogger := logger.With(zap.String("handler", "discovering"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*") // Consider restricting this in production

		// Extract query parameters
		targetURL := r.URL.Query().Get("url")
		authBearer := r.URL.Query().Get("authorizationBearer") // Optional bearer token

		if targetURL == "" {
			var response DiscoveryResult
			handlerLogger.Warn("Missing 'url' query parameter")
			response.Error = "'url' query parameter is required"
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(response)
			return
		}
		handlerLogger = handlerLogger.With(zap.String("targetURL", targetURL))

		// Create a context with timeout for the entire operation
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		// Create a mutex for response and a channel to signal success
		successChan := make(chan *DiscoveryResult)
		errorChan := make(chan struct{})

		checkCount := 1
		// Launch MCP discovery in a goroutine
		go func() {
			response, err := tryMCPDiscovery(ctx, targetURL, authBearer, handlerLogger)
			if err == nil && response != nil {
				select {
				case successChan <- response:
				default:
				}
			} else {
				select {
				case errorChan <- struct{}{}:
				default:
				}
			}
		}()

		checkCount++
		// Launch A2A discovery in a goroutine
		go func() {
			response, err := tryA2ADiscovery(ctx, targetURL, http.DefaultClient, handlerLogger)
			if err == nil && response != nil {
				select {
				case successChan <- response:
				default:
				}
			} else {
				select {
				case errorChan <- struct{}{}:
				default:
				}
			}
		}()

		checkCount++
		// Launch REST discovery in a goroutine
		go func() {
			response, err := tryRESTDiscovery(ctx, targetURL, http.DefaultClient, handlerLogger)
			if err == nil && response != nil {
				select {
				case successChan <- response:
				default:
				}
			} else {
				select {
				case errorChan <- struct{}{}:
				default:
				}
			}
		}()

		errorCounter := 0
		var response *DiscoveryResult
	discoveryLoop:
		for {
			select {
			case response = <-successChan:
				break discoveryLoop
			case <-errorChan:
				errorCounter++
				if errorCounter >= checkCount {
					break discoveryLoop
				}
			case <-ctx.Done():
				break discoveryLoop
			}
		}
		w.WriteHeader(http.StatusOK)
		if response == nil {
			response = &DiscoveryResult{
				Error: "no protocol found",
			}
		}
		json.NewEncoder(w).Encode(response)
	}
}
