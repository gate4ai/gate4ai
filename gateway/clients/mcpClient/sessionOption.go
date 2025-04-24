package mcpClient

import (
	"fmt"
	"net/http"
	"strings"
)

// SessionOption defines a function type for configuring an MCP Session.
type SessionOption func(*Session) error // Return error for potential validation

// WithHTTPClient sets a custom HTTP client for the session.
// If not provided, http.DefaultClient will be used.
func WithHTTPClient(client *http.Client) SessionOption {
	return func(s *Session) error {
		if client != nil {
			s.httpClient = client
		} else {
			s.httpClient = http.DefaultClient // Ensure default if nil is passed
		}
		return nil
	}
}

// WithHeaders adds or overrides headers for the session.
// Headers are merged if this option is used multiple times.
// Keys are case-insensitive during merge but preserved in final map.
func WithHeaders(headers map[string]string) SessionOption {
	return func(s *Session) error {
		if s.currentHeaders == nil {
			s.currentHeaders = make(map[string]string)
		}
		// Merge headers, respecting case of the *last* key added for a given lower-case key
		existingLowerKeys := make(map[string]string)
		for k := range s.currentHeaders {
			existingLowerKeys[strings.ToLower(k)] = k
		}

		for key, value := range headers {
			lowerKey := strings.ToLower(key)
			if existingKey, ok := existingLowerKeys[lowerKey]; ok && existingKey != key {
				// Remove old casing if new casing is different
				delete(s.currentHeaders, existingKey)
			}
			s.currentHeaders[key] = value     // Add/overwrite with new key casing
			existingLowerKeys[lowerKey] = key // Update the known casing
		}
		return nil
	}
}

// WithAuthenticationBearer adds an Authorization Bearer token header.
// This will override any existing Authorization header set by other options.
func WithAuthenticationBearer(token string) SessionOption {
	return func(s *Session) error {
		if s.currentHeaders == nil {
			s.currentHeaders = make(map[string]string)
		}
		if token == "" {
			// Remove Authorization header if token is empty
			delete(s.currentHeaders, "Authorization")
			// Also remove lower-case version just in case
			delete(s.currentHeaders, "authorization")
		} else {
			// Ensure canonical 'Authorization' key overrides others
			delete(s.currentHeaders, "authorization") // Delete potential lower-case version
			s.currentHeaders["Authorization"] = fmt.Sprintf("Bearer %s", token)
		}
		return nil
	}
}

// applySessionOptions processes the functional options and applies them.
// This helper function can be called within NewSession.
func applySessionOptions(s *Session, options []SessionOption) error {
	for _, option := range options {
		if err := option(s); err != nil {
			return err // Return the first error encountered
		}
	}
	return nil
}
