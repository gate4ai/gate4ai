package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/shared"
	"github.com/gate4ai/mcp/shared/config"
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

const (
	SESSION_ID_KEY2024 = "session_id"     // Query parameter for session ID (for V2024 compatibility)
	AUTH_KEY2024       = "key"            // Query parameter for authentication key (for V2024 compatibility)
	PATH2024           = "/sse"           // Unified endpoint path for V2024 (for V2024 compatibility)
	PATH               = "/mcp"           // Unified endpoint path
	MCP_SESSION_HEADER = "Mcp-Session-Id" // Header for session ID

	// Content Types
	contentTypeJSON = "application/json"

	// HTTP Statuses
	statusAccepted            = http.StatusAccepted            // 202
	statusNotFound            = http.StatusNotFound            // 404
	statusBadRequest          = http.StatusBadRequest          // 400
	statusMethodNotAllowed    = http.StatusMethodNotAllowed    // 405
	statusUnauthorized        = http.StatusUnauthorized        // 401
	statusInternalServerError = http.StatusInternalServerError // 500
)

// Transport manages MCP HTTP connections supporting multiple protocol versions.
type Transport struct {
	sessionManager  mcp.ISessionManager
	logger          *zap.Logger
	authManager     AuthenticationManager
	config          config.IConfig
	serverInfo      schema.Implementation
	NoStream2025    bool          // Whether server supports streaming responses in V2
	sessionTimeout  time.Duration // Idle timeout for sessions
	cleanupInterval time.Duration // How often to check for idle sessions
}

// TransportOption defines a function type for configuring the Transport.
type TransportOption func(*Transport) error

// WithStreamingSupport enables or disables streaming responses for V2025.
func WithStreamingSupport(enabled bool) TransportOption {
	return func(t *Transport) error {
		t.NoStream2025 = enabled
		return nil
	}
}

// WithSessionTimeout sets the idle timeout for sessions.
func WithSessionTimeout(timeout time.Duration) TransportOption {
	return func(t *Transport) error {
		if timeout <= 0 {
			return errors.New("session timeout must be positive")
		}
		t.sessionTimeout = timeout
		return nil
	}
}

// New creates a new MCP HTTP transport handler.
func New(mcpManager mcp.ISessionManager, logger *zap.Logger, cfg config.IConfig, options ...TransportOption) (*Transport, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if mcpManager == nil {
		return nil, errors.New("session manager cannot be nil")
	}
	if cfg == nil {
		return nil, errors.New("config cannot be nil")
	}

	serverName, err := cfg.ServerName()
	if err != nil {
		return nil, fmt.Errorf("failed to get server name from config: %w", err)
	}
	serverVersion, err := cfg.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("failed to get server version from config: %w", err)
	}

	transport := &Transport{
		sessionManager: mcpManager,
		logger:         logger.Named("mcp-transport"),
		authManager:    NewAuthenticator(cfg, logger), // Default authenticator
		config:         cfg,
		serverInfo: schema.Implementation{
			Name:    serverName,
			Version: serverVersion,
		},
		cleanupInterval: 1 * time.Minute, // Default cleanup interval
		sessionTimeout:  5 * time.Minute, // Default session timeout
	}

	// Apply configuration options
	for _, option := range options {
		if err := option(transport); err != nil {
			return nil, fmt.Errorf("failed to apply transport option: %w", err)
		}
	}

	// Start background cleanup routine if timeout is set
	if transport.sessionTimeout > 0 { // TODO Session manager function?
		go transport.startSessionCleanup()
	}

	logger.Info("MCP HTTP Transport created",
		zap.Bool("streamingSupport2025", transport.NoStream2025),
		zap.Duration("sessionTimeout", transport.sessionTimeout),
	)

	return transport, nil
}

// SetAuthManager allows changing the authentication manager.
func (t *Transport) SetAuthManager(authManager AuthenticationManager) {
	t.authManager = authManager
}

// RegisterHandlers registers the unified MCP handler with the HTTP mux.
func (t *Transport) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc(PATH2024, t.Handle2024MCP())
	mux.HandleFunc(PATH, t.HandleMCP())
	t.logger.Info("Registered MCP handler", zap.String("path", PATH), zap.String("path2024", PATH2024))
}

func (t *Transport) Handle2024MCP() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := t.logger

		logger.Debug("Received request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remoteAddr", r.RemoteAddr),
			zap.String("query", r.URL.RawQuery),
		)

		// Handle based on HTTP method
		switch r.Method {
		case http.MethodGet:
			t.handle2024GET(w, r, logger)
		case http.MethodPost:
			t.handle2024POST(w, r, logger)
		case http.MethodOptions:
			w.Header().Set("Allow", "GET, POST, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
		default:
			logger.Warn("Method not allowed", zap.String("method", r.Method))
			http.Error(w, "Method Not Allowed", statusMethodNotAllowed)
		}
	}
}

func (t *Transport) HandleMCP() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger := t.logger

		logger.Debug("Received request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remoteAddr", r.RemoteAddr),
			zap.String("query", r.URL.RawQuery),
		)

		switch r.Method {
		case http.MethodGet:
			t.handleGET(w, r, logger)
		case http.MethodPost:
			t.handlePOST(w, r, logger)
		case http.MethodDelete:
			t.handleDELETE(w, r, logger)
		case http.MethodOptions:
			w.Header().Set("Allow", "GET, POST, DELETE, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
		default:
			logger.Warn("Method not allowed", zap.String("method", r.Method))
			http.Error(w, "Method Not Allowed", statusMethodNotAllowed)
		}
	}
}

// startSessionCleanup periodically checks for idle sessions and closes them.
func (t *Transport) startSessionCleanup() {
	ticker := time.NewTicker(t.cleanupInterval)
	defer ticker.Stop()
	t.logger.Info("Starting session cleanup routine",
		zap.Duration("interval", t.cleanupInterval),
		zap.Duration("timeout", t.sessionTimeout),
	)
	for range ticker.C {
		t.sessionManager.CleanupIdleSessions(t.sessionTimeout)
	}
	t.logger.Info("Session cleanup routine stopped")
}

// --- Helper to send JSON responses ---
func sendJSONResponse(w http.ResponseWriter, statusCode int, data interface{}, logger *zap.Logger) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(statusCode)
	if data != nil {
		if err := json.NewEncoder(w).Encode(data); err != nil {
			logger.Error("Failed to encode JSON response", zap.Error(err))
			// Attempt to send a plain text error if JSON encoding fails
			http.Error(w, `{"jsonrpc":"2.0", "error":{"code":-32603, "message":"Internal server error writing response"}}`, http.StatusInternalServerError)
		}
	}
}

// --- Helper to send JSON-RPC errors ---
func sendJSONRPCErrorResponse(w http.ResponseWriter, id *schema.RequestID, code int, message string, data interface{}, logger *zap.Logger) {
	errResp := shared.JSONRPCErrorResponse{
		JSONRPC: shared.JSONRPCVersion,
		ID:      id, // Can be nil for some errors (like parse error)
		Error: &shared.JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	logger.Warn("Sending JSON-RPC Error",
		zap.Int("code", code),
		zap.String("message", message),
		zap.Any("data", data),
		zap.Any("reqID", id),
	)
	// According to JSON-RPC spec, errors should still return 200 OK at HTTP level
	sendJSONResponse(w, http.StatusOK, errResp, logger)
}

func (t *Transport) getSession(w http.ResponseWriter, r *http.Request, logger *zap.Logger, allowCreate bool) (shared.ISession, error) {
	sessionID := r.Header.Get(MCP_SESSION_HEADER)
	if sessionID == "" {
		sessionID = r.URL.Query().Get(SESSION_ID_KEY2024)
	}

	if sessionID != "" {
		session, err := t.sessionManager.GetSession(sessionID)
		if err != nil {
			logger.Warn("Session not found for V2 GET stream request", zap.String("sessionId", sessionID), zap.Error(err))
			http.Error(w, "Not Found: Session expired or invalid", statusNotFound)
			return nil, err
		}
		return session, nil
	} else {
		if !allowCreate {
			logger.Warn("Session not found for V2 GET stream request", zap.String("sessionId", sessionID))
			http.Error(w, "Not Found: Session expired or invalid", statusNotFound)
			return nil, errors.New("session not found")
		}

		authKey := t.extractAuthKey(r)
		userID, sessionParams, err := t.authManager.Authenticate(authKey, r.RemoteAddr)
		if err != nil {
			logger.Warn("Authentication failed for V2024 SSE connection", zap.String("remoteAddr", r.RemoteAddr), zap.Error(err))
			http.Error(w, "Authentication failed: "+err.Error(), statusUnauthorized)
			return nil, err
		}

		return t.sessionManager.CreateSession(userID, sessionParams), nil
	}
}
