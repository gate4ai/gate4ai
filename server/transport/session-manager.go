package transport

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gate4ai/gate4ai/shared"
	"github.com/gate4ai/gate4ai/shared/config"

	"github.com/gate4ai/gate4ai/shared/mcp/2025/schema"
	"go.uber.org/zap"
)

type ISessionManager interface {
	CreateSession(userID string, id string, params *sync.Map) shared.ISession
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

// Input returns the manager's input processor.
func (m *Manager) Input() *shared.Input {
	return m.inputProcessor
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

// AddCapability registers one or more capabilities with the input processor.
// Changed to accept generic ICapability. The input processor will route correctly.
func (m *Manager) AddCapability(capabilities ...shared.ICapability) {
	// The type check logic is now inside Input.AddServer/ClientCapability methods
	for _, cap := range capabilities {
		if serverCap, ok := cap.(shared.IServerCapability); ok {
			m.inputProcessor.AddServerCapability(serverCap)
		} else if clientCap, ok := cap.(shared.IClientCapability); ok {
			m.inputProcessor.AddClientCapability(clientCap)
		} else {
			m.logger.Warn("Unknown capability type, cannot add", zap.String("type", fmt.Sprintf("%T", cap)))
		}
	}
}

// CreateSession creates a new session with a unique ID
func (m *Manager) CreateSession(userID string, id string, params *sync.Map) shared.ISession {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := NewSession(m, userID, id, m.inputProcessor, params)
	m.sessions[session.ID] = session

	m.logger.Debug("Created new session",
		zap.String("sessionID", session.ID),
		zap.String("userID", userID),
	)
	return session
}

var ErrSessionNotFound = errors.New("session not found")

// GetSession retrieves a session by its ID
func (m *Manager) GetSession(id string) (shared.ISession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[id]
	if !exists {
		return nil, ErrSessionNotFound
	}

	return session, nil
}

// RemoveSession removes a session reference without calling Close.
// Used by transport on disconnect detection.
func (m *Manager) RemoveSession(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, exists := m.sessions[id]
	if exists {
		delete(m.sessions, id)
		m.logger.Debug("Removed session reference", zap.String("sessionID", id))
	}
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
