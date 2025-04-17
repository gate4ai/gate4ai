package a2aClient

import (
	"net/http"

	"go.uber.org/zap"
)

// ClientOption defines options for configuring the A2A client.
type ClientOption func(*Client)

// WithLogger sets a custom logger for the client.
// If not provided, a no-op logger will be used.
func WithLogger(logger *zap.Logger) ClientOption {
	return func(c *Client) {
		if logger != nil {
			c.logger = logger.Named("a2aClient").With(zap.String("baseURL", c.baseURL))
		}
	}
}

// WithHTTPClient sets a custom HTTP client for the client.
// If not provided, http.DefaultClient will be used.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// DoNotTrustAgentInfoURL is an option to prevent the client from updating its
// baseURL with the URL from the fetched AgentInfo.
func DoNotTrustAgentInfoURL() ClientOption {
	return func(c *Client) {
		c.trustAgentInfoURL = false
	}
}
