package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/server/mcp"
	"github.com/gate4ai/gate4ai/shared"
	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"github.com/gate4ai/gate4ai/shared/config"
	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

const (
	SESSION_ID_KEY2024 = "session_id"     // Query parameter for session ID (for V2024 compatibility)
	MCP2024_AUTH_KEY   = "key"            // Query parameter for authentication key (for V2024 compatibility)
	A2A_PATH           = "/a2a"           // Dedicated path for A2A protocol
	MCP2024_PATH       = "/sse"           // Unified endpoint path for V2024 (for V2024 compatibility)
	MCP2025_PATH       = "/mcp"           // Unified endpoint path
	MCP_SESSION_HEADER = "Mcp-Session-Id" // Header for session ID
	//TODO: A2A - move to hendler files ?
	//TODO: A2A - add well-known agent.json ?

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

var responseTimeout = 30 * time.Second // Default timeout for waiting on responses

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

// WithCleanupInterval sets the interval for checking idle sessions
func WithCleanupInterval(interval time.Duration) TransportOption {
	return func(t *Transport) error {
		if interval <= 0 {
			return errors.New("cleanup interval must be positive")
		}
		t.cleanupInterval = interval
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
		logger:         logger.Named("transport"),
		authManager:    NewAuthenticator(cfg, logger), // Default authenticator
		config:         cfg,
		//TODO: A2A - need only for mcp
		serverInfo: schema.Implementation{
			Name:    serverName,
			Version: serverVersion,
		},
		cleanupInterval: 5 * time.Minute,  // Default cleanup interval
		sessionTimeout:  30 * time.Minute, // Default session timeout
	}

	// Apply configuration options
	for _, option := range options {
		if err := option(transport); err != nil {
			return nil, fmt.Errorf("failed to apply transport option: %w", err)
		}
	}

	// Start background cleanup routine if timeout is set
	if transport.sessionTimeout > 0 { // TODO: Move cleanup to session manager?
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

// RegisterMCPHandlers registers only the MCP protocol handlers.
func (t *Transport) RegisterMCPHandlers(mux *http.ServeMux) {
	mux.HandleFunc(MCP2024_PATH, t.Handle2024MCP())
	mux.HandleFunc(MCP2025_PATH, t.HandleMCP())
	t.logger.Info("Registered MCP protocol handlers", zap.String("path_v2025", MCP2025_PATH), zap.String("path_v2024", MCP2024_PATH))
}

// RegisterA2AHandlers registers only the A2A protocol handlers.
func (t *Transport) RegisterA2AHandlers(mux *http.ServeMux, agentCard a2aSchema.AgentCard) {
	mux.HandleFunc(A2A_PATH, t.HandleA2A())
	// Register /.well-known only if A2A path is different
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(agentCard)
	}
	mux.HandleFunc("/.well-known/agent.json", handler)

	t.logger.Info("Registered A2A protocol handlers", zap.String("path", A2A_PATH), zap.String("wellKnownPath", "/.well-known/agent.json"))
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

// HandleA2A handles requests on the dedicated /a2a path.
func (t *Transport) HandleA2A() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Add CORS headers for all A2A responses, including errors and OPTIONS
		w.Header().Set("Access-Control-Allow-Origin", "*")                   // Adjust as needed
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET") // Allow GET for .well-known even if routed here initially
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept")

		logger := t.logger.With(zap.String("protocol", "A2A"))
		logger.Debug("Received A2A request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.String("remoteAddr", r.RemoteAddr),
		)

		switch r.Method {
		case http.MethodPost:
			t.handleA2APOST(w, r, logger)
		case http.MethodGet:
			http.NotFound(w, r)
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		default:
			logger.Warn("Method not allowed for A2A", zap.String("method", r.Method))
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

func (t *Transport) getSession(w http.ResponseWriter, r *http.Request, sessionID string, logger *zap.Logger, allowCreate bool) (shared.ISession, error) {
	if sessionID != "" {
		session, err := t.sessionManager.GetSession(sessionID)
		if err == nil {
			logger.Debug("Retrieved existing session", zap.String("sessionId", sessionID))
			return session, nil
		}
		// Log specific error if session was looked up but not found
		logger.Warn("Session lookup failed", zap.String("lookupSessionId", sessionID), zap.Error(err))
		http.Error(w, "Not Found: Session expired or invalid", statusNotFound)
		return nil, fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	// No session ID found in request
	if !allowCreate {
		logger.Warn("Session ID missing and creation not allowed for this request type", zap.String("path", r.URL.Path), zap.String("method", r.Method))
		http.Error(w, "Bad Request: Session ID required", statusBadRequest) // Or 404? Let's use 400 as it's a missing identifier.
		return nil, errors.New("session id required but not found")
	}

	// Allow creation - Authenticate and create new session
	authKey := t.extractAuthKey(r)
	userID, sessionParams, err := t.authManager.Authenticate(authKey, r.RemoteAddr)
	if err != nil {
		logger.Warn("Authentication failed", zap.String("remoteAddr", r.RemoteAddr), zap.Error(err))
		http.Error(w, "Unauthorized: "+err.Error(), statusUnauthorized)
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	// Add User-Agent and potentially other headers to session params
	if sessionParams != nil {
		userAgent := r.Header.Get("User-Agent")
		if userAgent != "" {
			sessionParams.Store("UserAgent", userAgent)
		}
		// Example: Add X-Forwarded-For if behind a proxy
		forwardedFor := r.Header.Get("X-Forwarded-For")
		if forwardedFor != "" {
			sessionParams.Store("X-Forwarded-For", forwardedFor)
		}
	} else {
		logger.Warn("SessionParams map was nil after authentication")
		sessionParams = &sync.Map{} // Initialize if nil to avoid panics
	}

	newSession := t.sessionManager.CreateSession(userID, sessionParams)
	logger.Info("Created new session", zap.String("newSessionId", newSession.GetID()), zap.String("userId", userID))
	return newSession, nil
}
