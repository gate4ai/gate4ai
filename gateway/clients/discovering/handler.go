package discovering

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
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

		w.WriteHeader(http.StatusOK)

		var responseMCP, responseA2A, responseREST *DiscoveryResult
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		waitGroup := sync.WaitGroup{}
		waitGroup.Add(3)

		// Launch MCP discovery
		go func() {
			defer waitGroup.Done()
			responseMCP, _ = tryMCPDiscovery(ctx, targetURL, authBearer, handlerLogger)
		}()

		// Launch A2A discovery
		go func() {
			defer waitGroup.Done()
			responseA2A, _ = tryA2ADiscovery(ctx, targetURL, http.DefaultClient, handlerLogger)
		}()

		// Launch REST discovery
		go func() {
			defer waitGroup.Done()
			responseREST, _ = tryRESTDiscovery(ctx, targetURL, http.DefaultClient, handlerLogger)
		}()

		waitGroup.Wait()

		if responseMCP != nil {
			json.NewEncoder(w).Encode(responseMCP)
			return
		}
		if responseA2A != nil {
			json.NewEncoder(w).Encode(responseA2A)
			return
		}
		if responseREST != nil {
			json.NewEncoder(w).Encode(responseREST)
			return
		}

		json.NewEncoder(w).Encode(&DiscoveryResult{
			Error: "no protocol found",
		})
	}
}
