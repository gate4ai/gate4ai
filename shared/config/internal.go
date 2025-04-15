package config

import (
	"context"
	"errors"
	"fmt"
	"sync"

	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema"
)

var _ IConfig = (*InternalConfig)(nil)
var ErrNotFound = errors.New("not found")

// InternalConfig implements all configuration interfaces with in-memory storage
type InternalConfig struct {
	mu                          sync.RWMutex
	ServerAddress               string
	ServerNameValue             string
	ServerVersionValue          string
	AuthorizationTypeValue      AuthorizationType
	LogLevelValue               string
	DiscoveringHandlerPathValue string
	FrontendAddressValue        string
	UserKeyHashes               map[string]string            // keyHash -> userID (new, secure)
	userParams                  map[string]map[string]string // userID -> paramName -> paramValue
	UserSubscribes              map[string][]string          // userID -> BackendIDs
	Backends                    map[string]*Backend          // serverSlug -> Server

	// SSL Fields
	SSLEnabledValue      bool
	SSLModeValue         string
	SSLCertFileValue     string
	SSLKeyFileValue      string
	SSLAcmeDomainsValue  []string
	SSLAcmeEmailValue    string
	SSLAcmeCacheDirValue string

	// A2A Fields
	A2AAgentNameValue          string
	A2AAgentDescriptionValue   *string
	A2AProviderOrgValue        *string
	A2AProviderURLValue        *string
	A2AAgentVersionValue       string
	A2ADocumentationURLValue   *string
	A2ADefaultInputModesValue  []string
	A2ADefaultOutputModesValue []string
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

		// Default SSL settings
		SSLEnabledValue:      false,
		SSLModeValue:         "manual",
		SSLCertFileValue:     "",
		SSLKeyFileValue:      "",
		SSLAcmeDomainsValue:  []string{},
		SSLAcmeEmailValue:    "",
		SSLAcmeCacheDirValue: "./.autocert-cache", // Default cache dir

		// Default A2A Settings (can be overridden)
		A2AAgentNameValue:          "Unnamed A2A Agent",
		A2AAgentVersionValue:       "0.1.0",
		A2ADefaultInputModesValue:  []string{"text"},
		A2ADefaultOutputModesValue: []string{"text"},
	}
}

// --- IConfig Implementation (Existing methods omitted for brevity, assume they remain) ---

func (c *InternalConfig) ListenAddr() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ServerAddress, nil
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

func (c *InternalConfig) LogLevel() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.LogLevelValue, nil
}

func (c *InternalConfig) DiscoveringHandlerPath() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.DiscoveringHandlerPathValue, nil
}

func (c *InternalConfig) SetDiscoveringHandlerPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.DiscoveringHandlerPathValue = path
}

func (c *InternalConfig) FrontendAddressForProxy() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.FrontendAddressValue, nil
}

func (c *InternalConfig) SetFrontendAddressForProxy(address string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.FrontendAddressValue = address
}

func (c *InternalConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if keyHash == "" {
		return "", nil
	} // Allow anonymous if key is empty and auth allows
	userID, exists := c.UserKeyHashes[keyHash]
	if !exists {
		return "", fmt.Errorf("key hash not found")
	} // Specific error for not found
	return userID, nil
}

func (c *InternalConfig) GetUserParams(userID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	params, exists := c.userParams[userID]
	if !exists {
		return make(map[string]string), nil
	}
	paramsCopy := make(map[string]string, len(params))
	copyMap(params, paramsCopy)
	return paramsCopy, nil
}

func (c *InternalConfig) SetUserParam(userID, paramName, paramValue string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.userParams[userID]; !exists {
		c.userParams[userID] = make(map[string]string)
	}
	c.userParams[userID][paramName] = paramValue
}

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
	serversCopy := make([]string, len(servers))
	copy(serversCopy, servers)
	c.UserSubscribes[userID] = serversCopy
}

func (c *InternalConfig) GetBackendBySlug(serverSlug string) (*Backend, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	backend, exists := c.Backends[serverSlug]
	if !exists {
		return nil, ErrNotFound
	}
	// Return a copy to prevent modification
	backendCopy := *backend
	return &backendCopy, nil
}

func (c *InternalConfig) SetBackend(serverSlug string, url string, bearer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Backends[serverSlug] = &Backend{URL: url, Bearer: bearer}
}

func (c *InternalConfig) Close() error                     { return nil }
func (c *InternalConfig) Status(ctx context.Context) error { return nil }

// --- SSL Methods (unchanged, just copied) ---
func (c *InternalConfig) SSLEnabled() (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SSLEnabledValue, nil
}
func (c *InternalConfig) SSLMode() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SSLModeValue, nil
}
func (c *InternalConfig) SSLCertFile() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SSLCertFileValue, nil
}
func (c *InternalConfig) SSLKeyFile() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SSLKeyFileValue, nil
}
func (c *InternalConfig) SSLAcmeDomains() ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	domainsCopy := make([]string, len(c.SSLAcmeDomainsValue))
	copy(domainsCopy, c.SSLAcmeDomainsValue)
	return domainsCopy, nil
}
func (c *InternalConfig) SSLAcmeEmail() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SSLAcmeEmailValue, nil
}
func (c *InternalConfig) SSLAcmeCacheDir() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SSLAcmeCacheDirValue, nil
}

// --- A2A Method ---
func (c *InternalConfig) GetA2ACardBaseInfo(agentURL string) (A2ACardBaseInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	info := A2ACardBaseInfo{
		Name:               c.A2AAgentNameValue,
		Description:        c.A2AAgentDescriptionValue,
		AgentURL:           agentURL, // Use the dynamically provided agent URL
		Version:            c.A2AAgentVersionValue,
		DocumentationURL:   c.A2ADocumentationURLValue,
		DefaultInputModes:  make([]string, len(c.A2ADefaultInputModesValue)),
		DefaultOutputModes: make([]string, len(c.A2ADefaultOutputModesValue)),
	}
	copy(info.DefaultInputModes, c.A2ADefaultInputModesValue)
	copy(info.DefaultOutputModes, c.A2ADefaultOutputModesValue)

	if c.A2AProviderOrgValue != nil || c.A2AProviderURLValue != nil {
		info.Provider = &a2aSchema.AgentProvider{
			Organization: derefString(c.A2AProviderOrgValue), // Handle nil safely
			URL:          c.A2AProviderURLValue,
		}
	}

	return info, nil
}

// Helper to dereference string pointer safely
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Helper to copy string maps
func copyMap(src, dst map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}
