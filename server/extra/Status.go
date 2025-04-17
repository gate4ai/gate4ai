package extra

import (
	"encoding/json"
	"net/http"

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
		}

		if err := cfg.Status(r.Context()); err != nil {
			handlerLogger.Error("Failed to get config status", zap.Error(err))
			response.Config = "error"
		} else {
			response.Config = "ok"
		}

		// Send response
		json.NewEncoder(w).Encode(response)
	}
}
