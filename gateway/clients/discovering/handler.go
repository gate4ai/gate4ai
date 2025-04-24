package discovering

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients"
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

// DiscoveryRequest defines the structure for POST requests
type DiscoveryRequest struct {
	TargetURL string            `json:"targetUrl"`
	Headers   map[string]string `json:"headers"` // Headers to use for discovery probes
}

// Handler creates an HTTP handler for discovering server type and basic info.
// Now supports GET (legacy/simple) and POST (with headers).
func Handler(logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handlerLogger := logger.With(zap.String("handler", "discovering"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*") // Allow requests from portal origin
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization") // Add other headers if needed

		// Handle OPTIONS preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var targetURL string
		var discoveryHeaders map[string]string

		if r.Method == http.MethodGet {
			targetURL = r.URL.Query().Get("url")
			discoveryHeaders = make(map[string]string)
			handlerLogger.Debug("Handling discovery GET request")
		} else if r.Method == http.MethodPost {
			handlerLogger.Debug("Handling discovery POST request")
			var reqPayload DiscoveryRequest
			if err := json.NewDecoder(r.Body).Decode(&reqPayload); err != nil {
				handlerLogger.Error("Failed to decode discovery POST body", zap.Error(err))
				http.Error(w, `{"error": "Invalid request body format"}`, http.StatusBadRequest)
				return
			}
			defer r.Body.Close()

			targetURL = reqPayload.TargetURL
			discoveryHeaders = reqPayload.Headers
		} else {
			http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		if targetURL == "" {
			handlerLogger.Warn("Missing target URL")
			http.Error(w, `{"error": "Target URL is required"}`, http.StatusBadRequest)
			return
		}
		handlerLogger = handlerLogger.With(zap.String("targetURL", targetURL))

		// Perform discovery attempts concurrently
		var responseMCP, responseA2A, responseREST *DiscoveryResult
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second) // Increased timeout slightly
		defer cancel()

		// Create an HTTP client that respects the context timeout for *all* discovery attempts
		discoveryHTTPClient := &http.Client{
			// Timeout is handled by the context now
			// Transport settings could be added here (e.g., TLS skip verify for testing, but avoid in prod)
		}

		waitGroup := sync.WaitGroup{}
		waitGroup.Add(3)

		// Launch MCP discovery with headers
		go func() {
			defer waitGroup.Done()
			// Pass discovery headers and specific bearer token
			responseMCP, _ = tryMCPDiscovery(ctx, targetURL, discoveryHeaders, handlerLogger)
		}()

		// Launch A2A discovery with headers
		go func() {
			defer waitGroup.Done()
			responseA2A, _ = tryA2ADiscovery(ctx, targetURL, discoveryHTTPClient, discoveryHeaders, handlerLogger)
		}()

		// Launch REST discovery with headers
		go func() {
			defer waitGroup.Done()
			responseREST, _ = tryRESTDiscovery(ctx, targetURL, discoveryHTTPClient, discoveryHeaders, handlerLogger)
		}()

		waitGroup.Wait()

		// Prioritize MCP > A2A > REST
		var finalResponse *DiscoveryResult
		if responseMCP != nil {
			finalResponse = responseMCP
		} else if responseA2A != nil {
			finalResponse = responseA2A
		} else if responseREST != nil {
			finalResponse = responseREST
		} else {
			finalResponse = &DiscoveryResult{Error: "no protocol found"}
		}

		w.WriteHeader(http.StatusOK) // Send 200 OK even if protocol not found
		json.NewEncoder(w).Encode(finalResponse)
	}
}
