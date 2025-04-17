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
	sessionParams.Store("RemoteAddr", remoteAddr)

	authType, err := a.config.AuthorizationType()
	if err != nil {
		return "", nil, err
	}

	// If authentication is not required everywhere
	if authKey != "" {
		// Hash the auth key before looking it up
		keyHash := config.HashAPIKey(authKey)
		userID, err = a.config.GetUserIDByKeyHash(keyHash)
		if err != nil {
			return "", nil, err
		}
	}

	if userID == "" && (authType != config.NotAuthorizedEverywhere && authType != config.NotAuthorizedToMarkedMethods) {
		a.logger.Warn("Authorization required but no valid key/token found", zap.String("authType", authType.String()))
		return "", nil, errors.New("authorization required")
	}

	SaveAuthKey(sessionParams, authKey)
	SaveUserId(sessionParams, userID)

	// If we got a valid user ID, return success
	return userID, sessionParams, nil
}

// Constants for session parameter keys
const (
	UserIDKey  = "authenticator_user_id"
	AuthKeyKey = "authenticator_auth_key"
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
