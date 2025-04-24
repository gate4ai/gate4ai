package transport

import (
	"errors"
	"sync"

	"github.com/gate4ai/gate4ai/shared/config"
	"go.uber.org/zap"
)

// AuthenticationManager interface for authenticating users
type AuthenticationManager interface {
	// Authenticate validates an authorization key and returns user ID and session parameters
	// If authKey is empty, it should use remoteAddr for decision making
	// For A2A, auth might come from AgentCard/HTTP Headers instead of keys.
	Authenticate(authKey string, remoteAddr string) (userID string, sessionParams *sync.Map, err error)
}

// DefaultAuthManager implements the AuthenticationManager interface
type DefaultAuthManager struct {
	logger *zap.Logger
	config config.IConfig
}

var _ AuthenticationManager = (*DefaultAuthManager)(nil)

// NewNoAuthorization creates a new NoAuthorization manager with the given config
func NewAuthenticator(cfg config.IConfig, logger *zap.Logger) *DefaultAuthManager {
	return &DefaultAuthManager{
		config: cfg,
		logger: logger,
	}
}

// Authenticate validates the provided key and remote address, returning UID if valid
func (a *DefaultAuthManager) Authenticate(authKey string, remoteAddr string) (userID string, sessionParams *sync.Map, err error) {
	sessionParams = &sync.Map{}
	// Store RemoteAddr FIRST
	if remoteAddr != "" {
		SaveRemoteAddr(sessionParams, remoteAddr)
	}

	authType, err := a.config.AuthorizationType()
	if err != nil {
		return "", nil, err
	}

	// If authentication is not required everywhere
	if authKey != "" {
		// Hash the auth key before looking it up
		keyHash := config.HashAPIKey(authKey)
		userID, err = a.config.GetUserIDByKeyHash(keyHash)
		if err != nil && !errors.Is(err, config.ErrNotFound) {
			// Log unexpected errors, but don't necessarily fail auth yet if anon is allowed
			a.logger.Error("Error checking key hash", zap.String("keyHash", keyHash), zap.Error(err))
		} else if err == nil && userID != "" {
			// Valid key found
			a.logger.Debug("Authenticated via API Key", zap.String("userID", userID))
		} else {
			// Key not found or empty user ID returned
			userID = "" // Ensure userID is empty if key invalid
		}
	}

	// Check if auth is strictly required but we don't have a user ID
	if userID == "" && (authType == config.AuthorizedUsersOnly) {
		a.logger.Warn("Authorization required but no valid key/token found or key invalid", zap.String("authType", authType.String()))
		return "", nil, ErrSessionNotFound
	}
	// For MarkedMethods, validation happens per method call later

	// Store AuthKey and UserID (which might be empty for anonymous)
	SaveAuthKey(sessionParams, authKey)
	SaveUserId(sessionParams, userID)

	// Return successfully (userID might be empty if anonymous access is allowed by authType)
	return userID, sessionParams, nil
}

// --- Session Parameter Helpers ---

// Constants for session parameter keys
const (
	UserIDKey     = "authenticator_user_id"
	AuthKeyKey    = "authenticator_auth_key"
	RemoteAddrKey = "authenticator_remote_addr" // NEW
)

func SaveUserId(sessionParams *sync.Map, userID string) {
	sessionParams.Store(UserIDKey, userID)
}

func GetUserId(sessionParams *sync.Map) string {
	userID, ok := sessionParams.Load(UserIDKey)
	if !ok {
		return ""
	}
	return userID.(string)
}

func SaveAuthKey(sessionParams *sync.Map, authKey string) {
	sessionParams.Store(AuthKeyKey, authKey)
}

// GetAuthKey retrieves the auth key from session parameters
func GetAuthKey(sessionParams *sync.Map) string {
	authKey, ok := sessionParams.Load(AuthKeyKey)
	if !ok {
		return ""
	}
	return authKey.(string)
}

// NEW: SaveRemoteAddr stores the remote address in session parameters
func SaveRemoteAddr(sessionParams *sync.Map, remoteAddr string) {
	sessionParams.Store(RemoteAddrKey, remoteAddr)
}

// NEW: GetRemoteAddr retrieves the remote address from session parameters
func GetRemoteAddr(sessionParams *sync.Map) string {
	remoteAddr, ok := sessionParams.Load(RemoteAddrKey)
	if !ok {
		return ""
	}
	return remoteAddr.(string)
}
