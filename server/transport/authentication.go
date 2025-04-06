package transport

import (
	"errors"
	"sync"

	"github.com/gate4ai/mcp/shared/config"
	"go.uber.org/zap"
)

// AuthenticationManager is responsible for validating authorization keys and returning user information
type AuthenticationManager interface {
	// Authenticate validates an authorization key and returns user ID and session parameters
	// If authKey is empty, it should use remoteAddr for decision making
	Authenticate(authKey string, remoteAddr string) (userID string, sessionParams *sync.Map, err error)
}

// Authenticator is an implementation of AuthManager that authorizes requests based on config settings
type Authenticator struct {
	logger *zap.Logger
	config config.IConfig
}

var _ AuthenticationManager = (*Authenticator)(nil)

// NewNoAuthorization creates a new NoAuthorization manager with the given config
func NewAuthenticator(cfg config.IConfig, logger *zap.Logger) *Authenticator {
	return &Authenticator{
		config: cfg,
		logger: logger,
	}
}

// Authenticate handles authorization based on the configuration settings
func (a *Authenticator) Authenticate(authKey string, remoteAddr string) (string, *sync.Map, error) {
	sessionParams := &sync.Map{}
	sessionParams.Store("RemoteAddr", remoteAddr)

	authType, err := a.config.AuthorizationType()
	if err != nil {
		return "", nil, err
	}

	var userID string
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
		a.logger.Info("AuthKey is not set, NotAuthorized")
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

func GetAuthKey(sessionParams *sync.Map) string {
	authKey, ok := sessionParams.Load(AuthKeyKey)
	if !ok {
		return ""
	}
	return authKey.(string)
}
