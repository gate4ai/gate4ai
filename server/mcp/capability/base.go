package capability

import (
	"encoding/json"
	"fmt"

	"github.com/gate4ai/mcp/server/mcp"
	"github.com/gate4ai/mcp/shared"

	// Import V2024 schema with an alias for checking supported versions
	schemaV2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"
	// Import V2025 schema as the default 'schema' for parsing, state, and response structure
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"

	"go.uber.org/zap"
)

// Store supported protocol versions by this server implementation
var supportedVersions = map[string]bool{
	schema.PROTOCOL_VERSION:      true, // 2025-03-26 (latest preferred)
	schemaV2024.PROTOCOL_VERSION: true, // 2024-11-05 (supported for backward compatibility)
}

// Define the latest version the server prefers/defaults to
const latestSupportedVersion = schema.PROTOCOL_VERSION // 2025-03-26

var _ shared.IServerCapability = (*BaseCapability)(nil)

// BaseCapability provides handlers for fundamental MCP methods like initialize and ping.
type BaseCapability struct {
	logger   *zap.Logger
	manager  mcp.ISessionManager
	handlers map[string]func(*shared.Message) (interface{}, error) // Map method -> handler function
}

// NewBase creates a new BaseCapability.
func NewBase(logger *zap.Logger, manager mcp.ISessionManager) *BaseCapability {
	bc := &BaseCapability{
		logger:  logger,
		manager: manager,
	}
	bc.handlers = map[string]func(*shared.Message) (interface{}, error){
		"ping":                      bc.handlePing,
		"initialize":                bc.handleInitialize,
		"notifications/ping":        bc.handleNotificationPing,
		"notifications/initialized": bc.handleNotificationInitialized,
	}

	return bc
}

// GetHandlers returns a map of method names to handler functions
// This satisfies the shared.ICapability interface
func (bc *BaseCapability) GetHandlers() map[string]func(*shared.Message) (interface{}, error) {
	return bc.handlers
}

// SetCapabilities sets the server capabilities for this capability
// This satisfies the shared.IServerCapability interface
func (bc *BaseCapability) SetCapabilities(s *schema.ServerCapabilities) {
	// The base capability doesn't have specific capability options
	bc.logger.Debug("SetCapabilities called on BaseCapability")
}

// GetCapabilityOptions returns the capability options for this capability
func (bc *BaseCapability) GetCapabilityOptions() map[string]interface{} {
	// Base capability doesn't register specific capability options in the schema
	return map[string]interface{}{}
}

func (bc *BaseCapability) handleNotificationPing(msg *shared.Message) (interface{}, error) {
	return nil, nil
}

// handleInitialize handles the 'initialize' request from the client.
func (bc *BaseCapability) handleInitialize(msg *shared.Message) (interface{}, error) {
	sessionID := msg.Session.GetID()
	logger := bc.logger.With(zap.String("sessionID", sessionID), zap.String("method", "initialize"))
	logger.Debug("Handling initialize request")

	// --- Parse Request (Using V2025 structure) ---
	var params schema.InitializeRequestParams // Use V2025 type for parsing client request
	if msg.Params == nil {
		logger.Warn("Received initialize request with missing params")
		return nil, fmt.Errorf("invalid initialize request: missing params")
	}
	err := json.Unmarshal(*msg.Params, &params)
	if err != nil {
		logger.Error("Failed to unmarshal initialize params", zap.Error(err), zap.ByteString("params", *msg.Params))
		// Return a specific JSON-RPC error if possible, TBD based on error handling strategy
		return nil, fmt.Errorf("invalid initialize parameters: %w", err)
	}

	requestedVersion := params.ProtocolVersion
	clientCaps := params.Capabilities // Parsed as V2025 ClientCapabilities
	clientInfo := params.ClientInfo   // Parsed as V2025 Implementation

	logger.Info("Received initialize request", // Log key info
		zap.String("requestedVersion", requestedVersion),
		zap.String("clientName", clientInfo.Name),
		zap.String("clientVersion", clientInfo.Version),
	)
	logger.Debug("Client reported capabilities", zap.Any("clientCaps", clientCaps)) // Debug log for verbose caps

	// --- Version Negotiation Logic ---
	negotiatedVersion := ""
	clientRequestedSupportedVersion := false

	if requestedVersion == "" {
		// Client didn't specify a version (older client or error?) - default to latest
		negotiatedVersion = latestSupportedVersion
		logger.Warn("Client did not specify protocol version, defaulting to server's latest", zap.String("negotiatedVersion", negotiatedVersion))
	} else if _, supported := supportedVersions[requestedVersion]; supported {
		// Client requested a version we explicitly support, use it.
		negotiatedVersion = requestedVersion
		clientRequestedSupportedVersion = true
		logger.Info("Negotiated protocol version (client requested supported)", zap.String("version", negotiatedVersion))
	} else {
		// Client requested an unsupported/unknown version.
		// Respond with the latest version *this server* supports.
		negotiatedVersion = latestSupportedVersion
		logger.Warn("Client requested unsupported version, responding with server's latest",
			zap.String("requestedVersion", requestedVersion),
			zap.String("negotiatedVersion", negotiatedVersion))
		// The client *should* disconnect if it doesn't support 'negotiatedVersion'.
	}

	// --- Store negotiated info in session ---
	// Type assertion to access specific Session methods
	session, ok := msg.Session.(mcp.IDownstreamSession)
	if !ok {
		logger.Error("Session type assertion failed in handleInitialize")
		// This indicates an internal server issue
		return nil, fmt.Errorf("internal server error: invalid session type")
	}
	session.SetNegotiatedVersion(negotiatedVersion)
	// Store client info using V2025 types (already parsed as such)
	session.SetClientInfo(clientInfo, clientCaps)

	logger.Debug("Stored negotiated version and client info in session")

	capabilities := schema.ServerCapabilities{}
	msg.Session.Input().SetCapabilities(&capabilities)

	response := schema.InitializeResult{
		ProtocolVersion: negotiatedVersion,
		Capabilities:    capabilities,
		ServerInfo:      *bc.manager.GetServerInfo(),
	}

	// Log detailed information about the response
	jsonResponse, err := json.Marshal(response)
	if err != nil {
		logger.Error("Failed to marshal initialize response for logging", zap.Error(err))
	} else {
		logger.Debug("Initialize response contents",
			zap.String("json", string(jsonResponse)),
			zap.String("negotiatedVersion", negotiatedVersion),
			zap.Any("serverInfo", response.ServerInfo))
	}

	// Log if we are responding with a version the client might not support
	if !clientRequestedSupportedVersion && requestedVersion != "" {
		logger.Info("Responding with a version potentially unsupported by the client", zap.String("negotiatedVersion", negotiatedVersion))
	}

	logger.Debug("Sending initialize response", zap.String("negotiatedVersion", negotiatedVersion))
	// Set session status to Connecting *after* successfully preparing response
	session.SetStatus(shared.StatusConnecting)
	return response, nil
}

// handleNotificationInitialized handles the 'notifications/initialized' notification from the client.
func (bc *BaseCapability) handleNotificationInitialized(msg *shared.Message) (interface{}, error) {
	session := msg.Session
	logger := bc.logger.With(zap.String("sessionID", session.GetID()), zap.String("method", "notifications/initialized"))
	logger.Debug("Handling initialized notification")

	// --- Validate State ---
	currentStatus := session.GetStatus()
	if currentStatus == shared.StatusConnected {
		logger.Debug("Received initialized notification for already connected session. Ignoring.")
		return nil, nil // Acknowledge but do nothing further
	}
	if currentStatus != shared.StatusConnecting {
		logger.Warn("Received initialized notification for session not in connecting state",
			zap.Int("status", int(currentStatus)))
		// Allow transition to connected anyway, might recover from race condition.
		// Alternative: return error: fmt.Errorf("invalid session state (%d) for initialized notification", currentStatus)
	}

	// Ensure negotiated version was set - indicates initialize handshake occurred.
	mcpSession, ok := session.(*mcp.Session)
	if !ok {
		logger.Error("Session type assertion failed in handleNotificationInitialized")
		return nil, fmt.Errorf("internal server error: invalid session type")
	}
	negotiatedVersion := mcpSession.GetNegotiatedVersion()
	if negotiatedVersion == "" {
		logger.Error("Received initialized notification before successful initialize handshake (no negotiated version)")
		// Optionally close the session here as it's a protocol violation
		// session.Close()
		return nil, fmt.Errorf("protocol error: received initialized notification before successful initialize")
	}

	// --- Update Status and Log ---
	session.SetStatus(shared.StatusConnected)

	// Log detailed client info now that connection is confirmed
	clientInfo := mcpSession.GetClientInfo()
	logger.Info("Session initialized and connected",
		zap.String("negotiatedVersion", negotiatedVersion),
		zap.String("clientName", clientInfo.Name),
		zap.String("clientVersion", clientInfo.Version),
	)
	// Log capabilities if needed (can be verbose)
	// logger.Debug("Client capabilities", zap.Any("clientCaps", mcpSession.GetClientCapabilities()))

	return nil, nil // Notifications expect no response content (nil result, nil error)
}

// handlePing handles the 'ping' request from the client.
func (bc *BaseCapability) handlePing(msg *shared.Message) (interface{}, error) {
	logger := bc.logger.With(zap.String("sessionID", msg.Session.GetID()), zap.String("method", "ping"))
	logger.Debug("Received ping request, sending pong")
	// Respond with an empty object as per JSON-RPC and MCP specs
	return map[string]interface{}{}, nil
}
