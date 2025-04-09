package extra

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/gateway/client"
	"github.com/gate4ai/mcp/shared"

	// Use 2025 schema
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// InfoResponse represents the response structure for the info endpoint (using 2025 types)
type InfoResponse struct {
	ServerInfo *schema.Implementation `json:"serverInfo,omitempty"`
	Tools      []schema.Tool          `json:"tools,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

// InfoHandler creates an HTTP handler for retrieving server info and tools
func InfoHandler(logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handlerLogger := logger.With(zap.String("handler", "InfoHandler"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*") // Consider restricting this in production

		// Extract query parameters
		sseURL := r.URL.Query().Get("url") // Renamed for clarity, expecting SSE URL
		authBearer := r.URL.Query().Get("authorizationBearer")

		var response InfoResponse

		if sseURL == "" {
			handlerLogger.Warn("Missing 'url' query parameter")
			response.Error = "'url' query parameter (SSE endpoint) is required"
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(response)
			return
		}
		handlerLogger = handlerLogger.With(zap.String("targetURL", sseURL))

		// Create a context with timeout for the entire operation
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second) // Overall timeout
		defer cancel()

		// Create new backend client
		handlerLogger.Debug("Creating backend client")
		mcpClient, err := client.New(shared.RandomID(), sseURL, handlerLogger)
		if err != nil {
			handlerLogger.Error("Failed to create backend client", zap.Error(err))
			response.Error = "Failed to initialize connection: " + err.Error()
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(response)
			return
		}

		// Create HTTP client with appropriate timeouts for POST requests made by the session
		httpClient := &http.Client{
			Timeout: 30 * time.Second, // Timeout for individual POST requests
		}

		// Create a new session (use derived context for session itself?)
		handlerLogger.Debug("Creating new backend session")
		mcpClientSession := mcpClient.NewSession(ctx, httpClient, authBearer)
		// Open() is called implicitly by GetServerInfo/GetTools if needed, no need to call here explicitly
		defer mcpClientSession.Close() // Ensure session is closed eventually

		// Get Server Info (waits for session initialization)
		handlerLogger.Debug("Getting server info...")
		serverInfoResult := <-mcpClientSession.GetServerInfo(ctx) // Use overall context
		if serverInfoResult.Err != nil {
			handlerLogger.Error("Failed to get server info", zap.Error(serverInfoResult.Err))
			response.Error = "Failed to get server info: " + serverInfoResult.Err.Error()
			// Determine appropriate status code based on error (e.g., 401 for auth, 502/504 for connection issues)
			statusCode := http.StatusInternalServerError
			// Add more specific error checking if needed
			w.WriteHeader(statusCode)
			json.NewEncoder(w).Encode(response)
			return
		}
		response.ServerInfo = serverInfoResult.ServerInfo // Assign V2025 type
		handlerLogger.Debug("Successfully retrieved server info")

		// Get Tools (waits for session initialization if not already done)
		handlerLogger.Debug("Getting tools...")
		toolsResult := <-mcpClientSession.GetTools(ctx) // Use overall context

		if toolsResult.Err != nil {
			// Log the error, but return server info if available
			handlerLogger.Error("Failed to get tools", zap.Error(toolsResult.Err))
			response.Error = "Failed to get tools: " + toolsResult.Err.Error()
			// Send a partial success response (207 Multi-Status or 206 Partial Content might be suitable)
			// Or stick with 200 OK and include error in body
			w.WriteHeader(http.StatusOK) // Or http.StatusPartialContent (206)
			json.NewEncoder(w).Encode(response)
			return
		}
		response.Tools = toolsResult.Tools // Assign V2025 type
		handlerLogger.Debug("Successfully retrieved tools", zap.Int("toolCount", len(response.Tools)))

		// Success response
		handlerLogger.Info("Successfully retrieved server info and tools")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}
}
