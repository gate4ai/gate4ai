package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type Backend struct {
	URL    string
	Bearer string
}

type IConfig interface {
	ListenAddr() (string, error)
	ServerName() (string, error)
	ServerVersion() (string, error)
	AuthorizationType() (AuthorizationType, error)
	LogLevel() (string, error)
	InfoHandler() (string, error)
	FrontendAddressForProxy() (string, error)

	GetUserIDByKeyHash(keyHash string) (userID string, err error)

	GetUserParams(userID string) (params map[string]string, err error)

	GetUserSubscribes(userID string) (backends []string, err error)

	GetBackend(backendID string) (backendCfg *Backend, err error)

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
