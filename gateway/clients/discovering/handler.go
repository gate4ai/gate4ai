package discovering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/gateway/clients"
	"github.com/gate4ai/gate4ai/shared" // For shared.FlushIfNotDone and RandomID
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	mcpSchema "github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
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

// writeLogEntry sends a DiscoveryLogEntry as an SSE event.
func writeLogEntry(w http.ResponseWriter, r *http.Request, entry DiscoveryLogEntry, logger *zap.Logger) error {
	jsonData, err := json.Marshal(entry)
	if err != nil {
		logger.Error("Failed to marshal log entry", zap.Error(err), zap.Any("entry", entry))
		// Don't send malformed data to client
		return fmt.Errorf("failed to marshal log entry: %w", err)
	}

	// Use shared helper for robust writing/flushing
	return shared.FlushIfNotDone(logger, r, w, "event: log_entry\ndata: %s\n\n", jsonData)
}

// writeFinalResult sends the final DiscoveryResult as an SSE event.
func writeFinalResult(w http.ResponseWriter, r *http.Request, result DiscoveryResult, logger *zap.Logger) error {
	jsonData, err := json.Marshal(result)
	if err != nil {
		logger.Error("Failed to marshal final result", zap.Error(err), zap.Any("result", result))
		// Try to send a generic error event instead
		errData, _ := json.Marshal(map[string]string{"error": "Internal error finalizing result"})
		_ = shared.FlushIfNotDone(logger, r, w, "event: final_result\ndata: %s\n\n", errData)
		return fmt.Errorf("failed to marshal final result: %w", err)
	}
	return shared.FlushIfNotDone(logger, r, w, "event: final_result\ndata: %s\n\n", jsonData)
}

// Handler creates an HTTP handler for discovering server type and basic info.
// Supports POST for sync JSON response and POST with "Accept: text/event-stream" for SSE logs.
func Handler(logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handlerLogger := logger.With(zap.String("handler", "discovering"))
		isSSE := strings.Contains(r.Header.Get("Accept"), "text/event-stream")

		// Set common headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS") // Only allow POST now
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept")

		// Handle OPTIONS preflight requests
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, `{"error": "Method Not Allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		// Decode request body for both SSE and JSON modes
		var reqPayload DiscoveryRequest
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			handlerLogger.Error("Failed to read request body", zap.Error(err))
			http.Error(w, `{"error": "Failed to read request body"}`, http.StatusInternalServerError)
			return
		}
		defer r.Body.Close() // Ensure body is closed

		if err := json.Unmarshal(bodyBytes, &reqPayload); err != nil {
			handlerLogger.Error("Failed to decode discovery POST body", zap.Error(err), zap.ByteString("body", bodyBytes))
			http.Error(w, `{"error": "Invalid request body format"}`, http.StatusBadRequest)
			return
		}

		targetURL := reqPayload.TargetURL
		discoveryHeaders := reqPayload.Headers
		if discoveryHeaders == nil { // Ensure map is not nil
			discoveryHeaders = make(map[string]string)
		}

		if targetURL == "" {
			handlerLogger.Warn("Missing target URL")
			http.Error(w, `{"error": "Target URL is required"}`, http.StatusBadRequest)
			return
		}
		handlerLogger = handlerLogger.With(zap.String("targetURL", targetURL), zap.Bool("sse", isSSE))
		handlerLogger.Info("Handling discovery request")

		if isSSE {
			// --- SSE Mode ---
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK) // Send 200 OK to start the stream

			flusher, ok := w.(http.Flusher)
			if !ok {
				handlerLogger.Error("ResponseWriter does not support flushing (http.Flusher), cannot use SSE")
				// Cannot realistically change response code here, just log
				return
			}
			flusher.Flush() // Ensure headers are sent

			logChan := make(chan DiscoveryLogEntry, 10) // Buffered channel for log entries
			var wg sync.WaitGroup
			var resultsMu sync.Mutex
			results := []*DiscoveryResult{} // Collect results from goroutines

			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second) // Overall timeout for discovery
			defer cancel()

			// Create HTTP client once
			discoveryHTTPClient := &http.Client{
				// Timeout is handled by the context passed to individual discovery funcs
			}

			// Function to add results safely
			addResult := func(res *DiscoveryResult) {
				if res != nil {
					resultsMu.Lock()
					results = append(results, res)
					resultsMu.Unlock()
				}
			}

			wg.Add(3)
			go func() {
				defer wg.Done()
				stepID := shared.RandomID() // Unique ID for this step
				res, _ := tryMCPDiscovery(ctx, stepID, targetURL, discoveryHeaders, logChan, handlerLogger.Named("mcp"))
				addResult(res)
			}()
			go func() {
				defer wg.Done()
				stepID := shared.RandomID() // Unique ID for this step
				res, _ := tryA2ADiscovery(ctx, stepID, targetURL, discoveryHTTPClient, discoveryHeaders, logChan, handlerLogger.Named("a2a"))
				addResult(res)
			}()
			go func() {
				defer wg.Done()
				stepID := shared.RandomID() // Unique ID for this step
				res, _ := tryRESTDiscovery(ctx, stepID, targetURL, discoveryHTTPClient, discoveryHeaders, logChan, handlerLogger.Named("rest"))
				addResult(res)
			}()

			// Goroutine to collect logs and forward to client
			logProcessingDone := make(chan struct{})
			go func() {
				defer close(logProcessingDone)
				for entry := range logChan {
					if err := writeLogEntry(w, r, entry, handlerLogger); err != nil {
						handlerLogger.Warn("Error writing log entry to SSE stream, client might have disconnected", zap.Error(err))
						cancel() // Cancel discovery if client disconnects
						return
					}
				}
				handlerLogger.Debug("Log channel closed")
			}()

			// Wait for all discovery attempts to finish
			wg.Wait()
			close(logChan)         // Close log channel *after* wg.Wait()
			<-logProcessingDone    // Wait for log processor to finish sending remaining logs
			handlerLogger.Debug("All discovery goroutines finished")

			// Determine final result
			finalResponse := prioritizeResults(results)
			if finalResponse == nil {
				finalResponse = &DiscoveryResult{Error: "no compatible protocol found"}
			}

			// Send final result
			if err := writeFinalResult(w, r, *finalResponse, handlerLogger); err != nil {
				handlerLogger.Error("Failed to write final result to SSE stream", zap.Error(err))
			}
			handlerLogger.Info("Discovery stream finished", zap.String("resultProtocol", string(finalResponse.Protocol)), zap.String("error", finalResponse.Error))

		} else {
			// --- Synchronous JSON Mode (Original Behavior - No Streaming Log) ---
			w.Header().Set("Content-Type", "application/json")
			var responseMCP, responseA2A, responseREST *DiscoveryResult
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second) // Shorter timeout for sync
			defer cancel()

			discoveryHTTPClient := &http.Client{}
			logChan := make(chan DiscoveryLogEntry, 1) // Dummy channel, won't be read
			defer close(logChan)
			waitGroup := sync.WaitGroup{}
			waitGroup.Add(3)

			go func() {
				defer waitGroup.Done()
				// Provide dummy StepIDs for sync mode as logs aren't sent
				responseMCP, _ = tryMCPDiscovery(ctx, "sync-mcp", targetURL, discoveryHeaders, logChan, handlerLogger.Named("mcp-sync"))
			}()
			go func() {
				defer waitGroup.Done()
				responseA2A, _ = tryA2ADiscovery(ctx, "sync-a2a", targetURL, discoveryHTTPClient, discoveryHeaders, logChan, handlerLogger.Named("a2a-sync"))
			}()
			go func() {
				defer waitGroup.Done()
				responseREST, _ = tryRESTDiscovery(ctx, "sync-rest", targetURL, discoveryHTTPClient, discoveryHeaders, logChan, handlerLogger.Named("rest-sync"))
			}()

			waitGroup.Wait()

			finalResponse := prioritizeResults([]*DiscoveryResult{responseMCP, responseA2A, responseREST})
			if finalResponse == nil {
				finalResponse = &DiscoveryResult{Error: "no compatible protocol found"}
			}

			w.WriteHeader(http.StatusOK)
			if err := json.NewEncoder(w).Encode(finalResponse); err != nil {
				handlerLogger.Error("Failed to encode final JSON response", zap.Error(err))
			}
			handlerLogger.Info("Discovery sync request finished", zap.String("resultProtocol", string(finalResponse.Protocol)), zap.String("error", finalResponse.Error))
		}
	}
}

// Helper to prioritize results: MCP > A2A > REST
func prioritizeResults(results []*DiscoveryResult) *DiscoveryResult {
	var bestResult *DiscoveryResult
	priority := map[clients.ServerProtocol]int{
		clients.ServerTypeMCP:  3,
		clients.ServerTypeA2A:  2,
		clients.ServerTypeREST: 1,
	}

	currentBestPriority := 0
	for _, res := range results {
		if res != nil && res.Error == "" { // Only consider successful results
			p, ok := priority[res.Protocol]
			if ok && p > currentBestPriority {
				bestResult = res
				currentBestPriority = p
			}
		}
	}
	// If no successful result, return the first non-nil result (which might have an error)
	if bestResult == nil {
		for _, res := range results {
			if res != nil {
				return res
			}
		}
	}
	return bestResult
}