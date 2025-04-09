package extra

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
)

// StatusResponse represents the response structure for the status endpoint
type StatusResponse struct {
	Config string `json:"config"`
	Portal string `json:"portal,omitempty"`
}

// StatusHandler creates an HTTP handler for checking system status
func StatusHandler(cfg config.IConfig, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handlerLogger := logger.With(zap.String("handler", "StatusHandler"))
		w.Header().Set("Content-Type", "application/json")

		// Always return 200 status code
		w.WriteHeader(http.StatusOK)

		response := StatusResponse{
			Config: "none",
			Portal: "none",
		}

		if err := cfg.Status(r.Context()); err != nil {
			handlerLogger.Error("Failed to get config status", zap.Error(err))
			response.Config = "error"
		} else {
			response.Config = "ok"
		}

		portalURL, err := cfg.FrontendAddressForProxy()
		if err != nil {
			handlerLogger.Error("Failed to get portal URL", zap.Error(err))
			response.Config = "error"
		}

		// Check portal status if portalURL is provided
		if portalURL != "" {
			client := &http.Client{
				Timeout: 5 * time.Second,
			}

			statusURL := portalURL
			if statusURL[len(statusURL)-1:] != "/" {
				statusURL += "/"
			}
			statusURL += "api/status"

			handlerLogger.Debug("Checking portal status", zap.String("url", statusURL))
			resp, err := client.Get(statusURL)
			if err != nil {
				handlerLogger.Error("Failed to connect to portal", zap.Error(err))
				response.Portal = "error"
			} else {
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusOK {
					handlerLogger.Error("Portal returned non-OK status", zap.Int("status", resp.StatusCode))
					response.Portal = "error"
				} else {
					response.Portal = "ok"
				}
			}
		}

		// Send response
		json.NewEncoder(w).Encode(response)
	}
}
