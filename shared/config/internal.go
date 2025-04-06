package config

import (
	"context"
	"errors"
	"sync"
)

var _ IConfig = (*InternalConfig)(nil)
var ErrNotFound = errors.New("not found")

// InternalConfig implements all configuration interfaces with in-memory storage
type InternalConfig struct {
	mu                     sync.RWMutex
	ServerAddress          string
	ServerNameValue        string
	ServerVersionValue     string
	AuthorizationTypeValue AuthorizationType
	LogLevelValue          string
	InfoHandlerValue       string
	FrontendAddressValue   string
	UserKeyHashes          map[string]string            // keyHash -> userID (new, secure)
	userParams             map[string]map[string]string // userID -> paramName -> paramValue
	UserSubscribes         map[string][]string          // userID -> BackendIDs
	Backends               map[string]*Backend          // serverID -> Server
}

// NewInternalConfig creates a new in-memory configuration
func NewInternalConfig() *InternalConfig {
	return &InternalConfig{
		ServerAddress:        ":8080",
		ServerNameValue:      "Unknown",
		ServerVersionValue:   "0.0.0",
		LogLevelValue:        "info",
		FrontendAddressValue: "http://localhost:3000",

		UserKeyHashes:  make(map[string]string),
		userParams:     make(map[string]map[string]string),
		UserSubscribes: make(map[string][]string),
		Backends:       make(map[string]*Backend),
	}
}

// ServerConfig implementation

func (c *InternalConfig) ListenAddr() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ServerAddress, nil
}

func (c *InternalConfig) SetListenAddr(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ServerAddress = addr
}

func (c *InternalConfig) AuthorizationType() (AuthorizationType, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AuthorizationTypeValue, nil
}

func (c *InternalConfig) ServerName() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ServerNameValue, nil
}

func (c *InternalConfig) ServerVersion() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ServerVersionValue, nil
}

// LogLevel returns the configured log level
func (c *InternalConfig) LogLevel() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.LogLevelValue, nil
}

// InfoHandler returns the information handler path
func (c *InternalConfig) InfoHandler() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.InfoHandlerValue, nil
}

// SetInfoHandler sets the info handler path
func (c *InternalConfig) SetInfoHandler(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.InfoHandlerValue = path
}

// FrontendAddressForProxy returns the frontend address for the proxy
func (c *InternalConfig) FrontendAddressForProxy() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.FrontendAddressValue, nil
}

// SetFrontendAddressForProxy sets the frontend address for the proxy
func (c *InternalConfig) SetFrontendAddressForProxy(address string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.FrontendAddressValue = address
}

// UsersConfig implementation

func (c *InternalConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// If empty key hash, return empty user ID
	if keyHash == "" {
		return "", nil
	}

	return c.UserKeyHashes[keyHash], nil
}

func (c *InternalConfig) GetUserParams(userID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	params, exists := c.userParams[userID]
	if !exists {
		return make(map[string]string), nil
	}

	// Return a copy to prevent concurrent modification
	paramsCopy := make(map[string]string, len(params))
	for k, v := range params {
		paramsCopy[k] = v
	}
	return paramsCopy, nil
}

func (c *InternalConfig) SetUserParam(userID, paramName, paramValue string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	params, exists := c.userParams[userID]
	if !exists {
		params = make(map[string]string)
		c.userParams[userID] = params
	}

	params[paramName] = paramValue
}

// UserSubscribesConfig implementation

func (c *InternalConfig) GetUserSubscribes(userID string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	servers, exists := c.UserSubscribes[userID]
	if !exists {
		return []string{}, nil
	}

	serversCopy := make([]string, len(servers))
	copy(serversCopy, servers)
	return serversCopy, nil
}

func (c *InternalConfig) SetUserSubscribes(userID string, servers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.UserSubscribes[userID]; !exists {
		c.UserSubscribes[userID] = make([]string, 0)
	}
	serversCopy := make([]string, len(servers))
	copy(serversCopy, servers)

	c.UserSubscribes[userID] = serversCopy
}

// ServersConfig implementation

func (c *InternalConfig) GetBackend(serverID string) (*Backend, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	server, exists := c.Backends[serverID]
	if !exists {
		return nil, ErrNotFound
	}

	return server, nil
}

func (c *InternalConfig) SetBackend(serverID, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	backend, exists := c.Backends[serverID]
	if !exists {
		c.Backends[serverID] = &Backend{URL: url}
		return
	}
	backend.URL = url
	c.Backends[serverID] = backend
}

func (c *InternalConfig) SetBackendBearer(backendID, bearer string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	server, exists := c.Backends[backendID]
	if !exists {
		c.Backends[backendID] = &Backend{Bearer: bearer}
		return
	}
	server.Bearer = bearer
	c.Backends[backendID] = server
}

func (c *InternalConfig) Close() error {
	return nil
}

func (c *InternalConfig) Status(ctx context.Context) error {
	return nil
}
