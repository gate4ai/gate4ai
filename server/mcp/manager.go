package mcp

import (
	"errors"
	"sync"
	"time"

	"github.com/gate4ai/mcp/shared"
	"github.com/gate4ai/mcp/shared/config"

	// Use V2025 schema for manager's state
	"github.com/gate4ai/mcp/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

type ISessionManager interface {
	CreateSession(userID string, params *sync.Map) shared.ISession
	GetSession(id string) (shared.ISession, error)
	CloseSession(id string)
	CloseAllSessions()
	GetLogger() *zap.Logger

	NotifyEligibleSessions(method string, params map[string]any)
	CleanupIdleSessions(timeout time.Duration)
	GetServerInfo() *schema.Implementation
}

var _ ISessionManager = (*Manager)(nil)

// Manager handles all active sessions
type Manager struct {
	sessions       map[string]*Session
	mu             sync.RWMutex
	logger         *zap.Logger
	ServerInfo     schema.Implementation
	inputProcessor *shared.Input
}

func (m *Manager) GetLogger() *zap.Logger {
	return m.logger
}

func (m *Manager) CleanupIdleSessions(timeout time.Duration) {
	for _, session := range m.sessions {
		if session.GetLastActivity().Add(timeout).Before(time.Now()) {
			session.Close()
		}
	}
}

func (m *Manager) GetServerInfo() *schema.Implementation {
	return &m.ServerInfo
}

// NewManager creates a new session manager
func NewManager(logger *zap.Logger, cfg config.IConfig, capabilities ...[]shared.ICapability) (*Manager, error) {
	serverName, err := cfg.ServerName()
	if err != nil {
		return nil, err
	}
	serverVersion, err := cfg.ServerVersion()
	if err != nil {
		return nil, err
	}

	m := &Manager{
		sessions:       make(map[string]*Session),
		logger:         logger,
		inputProcessor: shared.NewInput(logger),
		ServerInfo: schema.Implementation{
			Name:    serverName,
			Version: serverVersion,
		},
	}
	go m.inputProcessor.Process()
	return m, nil
}

func (m *Manager) AddCapability(capabilities ...shared.IServerCapability) {
	m.inputProcessor.AddServerCapability(capabilities...)
}

// CreateSession creates a new session with a unique ID
func (m *Manager) CreateSession(userID string, params *sync.Map) shared.ISession {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := NewSession(m, userID, m.inputProcessor, params)
	m.sessions[session.ID] = session

	m.logger.Debug("Created new session",
		zap.String("sessionID", session.ID),
		zap.String("userID", userID),
	)
	return session
}

// GetSession retrieves a session by its ID
func (m *Manager) GetSession(id string) (shared.ISession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[id]
	if !exists {
		return nil, errors.New("session not found")
	}

	return session, nil
}

// CloseSession removes a session and cleans up resources
func (m *Manager) CloseSession(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[id]
	if exists {
		// Close the session resources
		err := session.Close() // Call the Close method on the session itself
		if err != nil {
			m.logger.Error("Error closing session resources", zap.String("sessionID", id), zap.Error(err))
		}
		delete(m.sessions, id)
		m.logger.Info("Closed session", zap.String("sessionID", id))
	} else {
		m.logger.Warn("Attempted to close non-existent session", zap.String("sessionID", id))
	}
}

func (m *Manager) CloseAllSessions() {
	m.mu.Lock()
	// Create a slice of IDs to close to avoid holding the lock during CloseSession calls
	idsToClose := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		idsToClose = append(idsToClose, id)
	}
	m.mu.Unlock() // Release lock before iterating and closing

	var wg sync.WaitGroup
	for _, id := range idsToClose {
		wg.Add(1)
		go func(sessionID string) {
			defer wg.Done()
			m.CloseSession(sessionID) // CloseSession handles locking internally now
		}(id)
	}
	wg.Wait() // Wait for all sessions to be closed
	m.logger.Info("Closed all sessions")
}

func (m *Manager) NotifyEligibleSessions(method string, params map[string]any) {
	m.mu.RLock()
	// Create a slice of sessions to notify to avoid holding the lock during SendNotification
	sessionsToNotify := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		// Check eligibility (e.g., is connected, supports the feature via capabilities)
		if session.GetStatus() == shared.StatusConnected {
			// Add more checks based on 'method' and session capabilities if needed
			sessionsToNotify = append(sessionsToNotify, session)
		}
	}
	m.mu.RUnlock() // Release lock before sending notifications

	count := len(sessionsToNotify)
	if count > 0 {
		m.logger.Debug("Sending notification to eligible sessions",
			zap.String("method", method),
			zap.Int("count", count),
		)
		for _, session := range sessionsToNotify {
			session.SendNotification(method, params)
		}
	} else {
		//m.logger.Debug("No eligible sessions found for notification", zap.String("method", method))
	}
}

func (m *Manager) AddValidator(validators ...shared.MessageValidator) {
	m.inputProcessor.AddValidator(validators...)
}
