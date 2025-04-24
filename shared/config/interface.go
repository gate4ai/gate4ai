package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
)

// AuthorizationType represents different authorization strategies
type AuthorizationType int

const (
	// AuthorizedUsersOnly requires authentication for all requests
	AuthorizedUsersOnly AuthorizationType = iota
	// NotAuthorizedToMarkedMethods requires authentication for specific methods
	NotAuthorizedToMarkedMethods
	// NotAuthorizedEverywhere allows all requests without authentication
	NotAuthorizedEverywhere
)

// Helper method for AuthorizationType string representation
func (at AuthorizationType) String() string {
	names := [...]string{"AuthorizedUsersOnly", "NotAuthorizedToMarkedMethods", "NotAuthorizedEverywhere"}
	if at < 0 || int(at) >= len(names) {
		return "Unknown"
	}
	return names[at]
}

type Backend struct {
	URL    string
	Bearer string
}

type IConfig interface {
	// Core Server Settings
	ListenAddr() (string, error)
	ServerName() (string, error)
	ServerVersion() (string, error)
	AuthorizationType() (AuthorizationType, error)
	LogLevel() (string, error)
	DiscoveringHandlerPath() (string, error)
	FrontendAddressForProxy() (string, error)

	// User & Auth Settings
	GetUserIDByKeyHash(keyHash string) (userID string, err error)
	GetUserParams(userID string) (params map[string]string, err error)

	// Backend & Subscription Settings
	GetUserSubscribes(userID string) (backends []string, err error)
	GetBackendBySlug(slug string) (backendCfg *Backend, err error)
	GetServerHeaders(serverSlug string) (headers map[string]string, err error)
	GetSubscriptionHeaders(userID, serverSlug string) (headers map[string]string, err error)

	// SSL Settings
	SSLEnabled() (bool, error)
	SSLMode() (string, error)
	SSLCertFile() (string, error)
	SSLKeyFile() (string, error)
	SSLAcmeDomains() ([]string, error)
	SSLAcmeEmail() (string, error)
	SSLAcmeCacheDir() (string, error)

	// A2A Settings
	GetA2AAgentCard(agentURL string) (*a2aSchema.AgentCard, error)

	// Lifecycle & Status
	Status(ctx context.Context) error
	Close() error
}

// HashAPIKey converts a plaintext API key to its SHA-256 hash representation
func HashAPIKey(key string) string {
	if key == "" {
		return ""
	}
	hasher := sha256.New()
	hasher.Write([]byte(key))
	return hex.EncodeToString(hasher.Sum(nil))
}
