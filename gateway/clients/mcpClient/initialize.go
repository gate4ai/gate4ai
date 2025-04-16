package mcpClient

import (
	"encoding/json"
	"fmt"

	"github.com/gate4ai/mcp/shared"
	schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

// List of protocol versions this client supports when connecting to backends
var clientSupportedVersions = map[string]bool{
	schema.PROTOCOL_VERSION:     true,
	schema2024.PROTOCOL_VERSION: true,
}

// The latest version this client prefers and advertises
const clientLatestVersion = schema.PROTOCOL_VERSION

// sendInitialize initiates the MCP handshake with the backend.
func (s *Session) sendInitialize() {
	logger := s.BaseSession.Logger
	logger.Debug("Sending initialize request to backend")

	// Prepare V2025 InitializeRequestParams
	params := &schema.InitializeRequestParams{
		ProtocolVersion: clientLatestVersion, // Send the latest version this client supports
		ClientInfo: schema.Implementation{
			Name:    "gate4ai-gateway-client", // Identify as gateway's client part
			Version: "0.1.0",                  // TODO: Use actual gateway version from build info
		},
		Capabilities: schema.ClientCapabilities{},
	}

	logger.Debug("Initialize params being sent to backend", zap.Any("params", params))
	msg := <-s.SendRequestSync("initialize", params)
	if msg.Error != nil {
		logger.Error("Failed to initialize backend", zap.Error(msg.Error))
		s.writeInitializationErrorAndClose(msg.Error)
		return
	}
	if msg.Result == nil {
		err := fmt.Errorf("backend returned nil result")
		logger.Error(err.Error())
		s.writeInitializationErrorAndClose(err)
		return
	}
	var result schema.InitializeResult
	if err := json.Unmarshal(*msg.Result, &result); err != nil {
		logger.Error("Failed to unmarshal backend initialize result",
			zap.Error(err),
			zap.ByteString("result", *msg.Result),
		)
		err = fmt.Errorf("failed to parse backend initialize response: %w", err)
		s.writeInitializationErrorAndClose(err)
	}
	msg.Processed = true

	backendNegotiatedVersion := result.ProtocolVersion
	logger.Debug("Received initialize response from backend", zap.String("backendNegotiatedVersion", backendNegotiatedVersion))

	if _, supported := clientSupportedVersions[backendNegotiatedVersion]; !supported {
		err := fmt.Errorf("backend '%s' negotiated unsupported protocol version '%s'", s.Backend.Slug, backendNegotiatedVersion)
		logger.Error(err.Error())
		s.writeInitializationErrorAndClose(err)
	}

	// Store negotiated version and server info for this backend connection
	s.SetNegotiatedVersion(backendNegotiatedVersion)
	s.serverInfo = &result.ServerInfo

	logger.Info("Backend initialize successful",
		zap.String("negotiatedVersion", backendNegotiatedVersion),
		zap.Any("serverInfo", result.ServerInfo),
	)

	s.SetStatus(shared.StatusConnected)
	s.writeInitializationErrorAndClose(nil)

	s.SendRequestSync("notifications/initialized", map[string]interface{}{})
}
