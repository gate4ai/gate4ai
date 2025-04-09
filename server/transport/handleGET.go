package transport

import (
	"net/http"

	"go.uber.org/zap"
)

func (t *Transport) handleGET(w http.ResponseWriter, r *http.Request, logger *zap.Logger) {
	logger.Warn("Method not allowed for SSE streaming at this endpoint", zap.String("method", r.Method))
	http.Error(w, "Method Not Allowed", statusMethodNotAllowed)
	logger.Info("Returned 405 Method Not Allowed", zap.String("path", r.URL.Path))
}
