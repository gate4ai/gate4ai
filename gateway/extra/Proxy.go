package extra

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"go.uber.org/zap"
)

// Custom error message for proxy failures
const proxyErrorMessage = "Internal Server Error\n" +
	"It appears that the portal address specified for proxying UI requests is incorrect.\n" +
	"If you are using the portal and gateway together, with settings being passed through the database, " +
	"then specify the correct address in the portal settings\n" +
	"[Open Portal as Admin] -> Settings -> Gateway -> [Frontend Address for Proxy] -> Reboot gateway"

func ProxyHandler(frontUrl string, logger *zap.Logger) http.HandlerFunc {
	targetURL, err := url.Parse(frontUrl)
	if err != nil {
		logger.Error("Creating proxy handler failed: Invalid frontUrl format", zap.String("frontUrl", frontUrl), zap.Error(err))
		return nil
	}

	return func(w http.ResponseWriter, r *http.Request) {
		proxy := httputil.NewSingleHostReverseProxy(targetURL)

		proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
			// Log the actual underlying error for debugging purposes
			logger.Error("Reverse proxy error connecting to frontend",
				zap.String("targetUrl", targetURL.String()), // Log the target
				zap.String("method", req.Method),
				zap.String("url", req.URL.String()),
				zap.Error(err), // The specific error (e.g., connection refused, timeout)
			)

			// Set the status code to Internal Server Error (500)
			rw.WriteHeader(http.StatusInternalServerError)

			// Write the custom error message to the response body
			_, writeErr := rw.Write([]byte(proxyErrorMessage))
			if writeErr != nil {
				// Log an error if we fail to write the custom message back to the client
				logger.Error("Failed to write custom proxy error message to response", zap.Error(writeErr))
			}
		}

		proxy.ServeHTTP(w, r)
	}
}
