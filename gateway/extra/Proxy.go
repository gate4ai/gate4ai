package extra

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"go.uber.org/zap"
)

func ProxyHandler(frontUrl string, logger *zap.Logger) http.HandlerFunc {
	targetURL, err := url.Parse(frontUrl)
	if err != nil {
		logger.Error("Creating proxy handler failed", zap.Error(err))
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) {
		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
			logger.Error("Reverse proxy error", zap.Error(err))
			http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
		}
		proxy.ServeHTTP(w, r)
	}
}
