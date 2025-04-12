package discovering

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema" // Assuming MCP info is based on latest schema
	"go.uber.org/zap"
)

// A2AInfo holds information specific to A2A discovery results.
type A2AInfo struct {
	AgentJsonUrl string `json:"agentJsonUrl,omitempty"` // URL where agent.json was found
	// TODO: Add more A2A specific info if needed after parsing agent.json
}

// RESTInfo holds information specific to REST (OpenAPI) discovery results.
type RESTInfo struct {
	OpenApiJsonUrl string `json:"openApiJsonUrl,omitempty"` // URL where openapi.json was found
	SwaggerUrl     string `json:"swaggerUrl,omitempty"`     // URL where swagger UI might be found
	// TODO: Add more REST specific info if needed
}

// MCPInfo holds information specific to MCP discovery results.
type MCPInfo struct {
	ServerInfo *schema.Implementation `json:"serverInfo,omitempty"`
	Tools      []schema.Tool          `json:"tools,omitempty"`
	// Add other relevant MCP info if needed
}

// DiscoveryResult holds the result of a discovery check.
type DiscoveryResult struct {
	MCP   *MCPInfo  `json:"mcp,omitempty"`
	A2A   *A2AInfo  `json:"a2a,omitempty"`
	REST  *RESTInfo `json:"rest,omitempty"`
	Error string    `json:"error,omitempty"`
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

		var response DiscoveryResult

		if targetURL == "" {
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
		lock := sync.Mutex{}
		successChan := make(chan struct{})
		errorChan := make(chan struct{})

		checkCount := 1
		// Launch MCP discovery in a goroutine
		go func() {
			mcpInfo, err := tryMCPDiscovery(ctx, targetURL, authBearer, handlerLogger)
			if err == nil && mcpInfo != nil {
				lock.Lock()
				response.MCP = mcpInfo
				lock.Unlock()
				// Signal success to return early
				select {
				case successChan <- struct{}{}:
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
			a2aInfo, err := tryA2ADiscovery(ctx, targetURL, http.DefaultClient, handlerLogger)
			if err == nil && a2aInfo != nil {
				lock.Lock()
				response.A2A = a2aInfo
				lock.Unlock()
				// Signal success to return early
				select {
				case successChan <- struct{}{}:
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
			restInfo, err := tryRESTDiscovery(ctx, targetURL, http.DefaultClient, handlerLogger)
			if err == nil && restInfo != nil {
				lock.Lock()
				response.REST = restInfo
				lock.Unlock()
				// Signal success to return early
				select {
				case successChan <- struct{}{}:
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
	discoveryLoop:
		for {
			select {
			case <-successChan:
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
		if response.A2A == nil && response.REST == nil && response.MCP == nil {
			response.Error = "no protocol found"
		}
		json.NewEncoder(w).Encode(response)
	}
}
