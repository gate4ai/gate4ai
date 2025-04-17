package mcp

import (
	"sync"

	"github.com/gate4ai/gate4ai/shared"
	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

type IDownstreamSession interface {
	shared.ISession
	SetClientInfo(info schema.Implementation, caps schema.ClientCapabilities)
}

var _ IDownstreamSession = (*Session)(nil)

// Session represents a client connection session
type Session struct {
	*shared.BaseSession
	manager ISessionManager
	UserID  string // User identifier for the session

	// Fields added for multi-version support
	NegotiatedVersion  string                     `json:"-"` // The protocol version agreed upon for this session
	ClientCapabilities *schema.ClientCapabilities `json:"-"` // Capabilities reported by the client
	ClientInfo         schema.Implementation      `json:"-"` // Info about the client implementation
}

// NewSession creates a new session with the given parameters
func NewSession(manager ISessionManager, userID string, inputProcessor *shared.Input, params *sync.Map) *Session {
	// Note: ClientCapabilities and ClientInfo will be set during initialization
	return &Session{
		BaseSession: shared.NewBaseSession(manager.GetLogger(), inputProcessor, params),
		manager:     manager,
		UserID:      userID,
	}
}

func (s *Session) Close() error {
	//TODO add close all backend server connections
	logger := s.BaseSession.Logger
	logger.Debug("Closing server session")
	err := s.BaseSession.Close()
	if err != nil {
		logger.Error("Error while closing base session", zap.Error(err))
	}
	return err
}

// SetClientInfo stores the client's capabilities and implementation info.
// Uses V2025 types for storage.
func (s *Session) SetClientInfo(info schema.Implementation, caps schema.ClientCapabilities) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.ClientInfo = info
	s.ClientCapabilities = &caps // Store pointer to capabilities
}

// GetClientInfo retrieves the client's implementation info.
func (s *Session) GetClientInfo() schema.Implementation {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.ClientInfo
}

// GetClientCapabilities retrieves the client's reported capabilities.
func (s *Session) GetClientCapabilities() *schema.ClientCapabilities {
	s.Mu.RLock()
	defer s.Mu.RUnlock()
	return s.ClientCapabilities // Return pointer
}
