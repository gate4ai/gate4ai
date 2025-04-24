package config

import (
	"context"
	"errors"
	"fmt"
	"sync"

	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
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
	UserKeyHashes               map[string]string            // keyHash -> userID
	userParams                  map[string]map[string]string // userID -> paramName -> paramValue
	UserSubscribes              map[string][]string          // userID -> serverSlugs
	Backends                    map[string]*Backend          // serverSlug -> Server
	serverHeaders               map[string]map[string]string // NEW: serverSlug -> {headerKey: headerValue}
	subscriptionHeaders         map[string]map[string]string // NEW: subscriptionKey (userID:serverSlug) -> {headerKey: headerValue}

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
	A2ASkills                  []a2aSchema.AgentSkill
}

// NewInternalConfig creates a new in-memory configuration
func NewInternalConfig() *InternalConfig {
	return &InternalConfig{
		ServerAddress:        ":8080",
		ServerNameValue:      "Unknown",
		ServerVersionValue:   "0.0.0",
		LogLevelValue:        "info",
		FrontendAddressValue: "http://localhost:3000",

		UserKeyHashes:       make(map[string]string),
		userParams:          make(map[string]map[string]string),
		UserSubscribes:      make(map[string][]string),
		Backends:            make(map[string]*Backend),
		serverHeaders:       make(map[string]map[string]string), // NEW
		subscriptionHeaders: make(map[string]map[string]string), // NEW

		// Default SSL settings
		SSLEnabledValue:      false,
		SSLModeValue:         "manual",
		SSLCertFileValue:     "",
		SSLKeyFileValue:      "",
		SSLAcmeDomainsValue:  []string{},
		SSLAcmeEmailValue:    "",
		SSLAcmeCacheDirValue: "./.autocert-cache",
	}
}

// --- IConfig Implementation ---

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
	dc := make([]string, len(c.SSLAcmeDomainsValue))
	copy(dc, c.SSLAcmeDomainsValue)
	return dc, nil
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
func (c *InternalConfig) Status(ctx context.Context) error { return nil }
func (c *InternalConfig) Close() error                     { return nil }

func (c *InternalConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if keyHash == "" {
		return "", nil
	}
	userID, exists := c.UserKeyHashes[keyHash]
	if !exists {
		return "", ErrNotFound
	}
	return userID, nil
}
func (c *InternalConfig) GetUserParams(userID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	params, exists := c.userParams[userID]
	if !exists {
		return make(map[string]string), nil
	}
	pc := make(map[string]string, len(params))
	copyMap(params, pc)
	return pc, nil
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
	sc := make([]string, len(servers))
	copy(sc, servers)
	return sc, nil
}
func (c *InternalConfig) SetUserSubscribes(userID string, servers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sc := make([]string, len(servers))
	copy(sc, servers)
	c.UserSubscribes[userID] = sc
}
func (c *InternalConfig) GetBackendBySlug(serverSlug string) (*Backend, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	backend, exists := c.Backends[serverSlug]
	if !exists {
		return nil, ErrNotFound
	}
	bc := *backend
	return &bc, nil
}
func (c *InternalConfig) SetBackend(serverSlug string, url string, bearer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Backends[serverSlug] = &Backend{URL: url, Bearer: bearer}
}

// NEW: GetServerHeaders retrieves the server-specific headers.
func (c *InternalConfig) GetServerHeaders(serverSlug string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	headers, exists := c.serverHeaders[serverSlug]
	if !exists {
		return make(map[string]string), nil // Return empty map if not found
	}
	headersCopy := make(map[string]string, len(headers))
	copyMap(headers, headersCopy)
	return headersCopy, nil
}

// NEW: SetServerHeaders sets server-specific headers (for testing/setup).
func (c *InternalConfig) SetServerHeaders(serverSlug string, headers map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	headersCopy := make(map[string]string, len(headers))
	copyMap(headers, headersCopy)
	c.serverHeaders[serverSlug] = headersCopy
}

// NEW: GetSubscriptionHeaders retrieves the subscription-specific headers.
func (c *InternalConfig) GetSubscriptionHeaders(userID, serverSlug string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	subscriptionKey := fmt.Sprintf("%s:%s", userID, serverSlug)
	headers, exists := c.subscriptionHeaders[subscriptionKey]
	if !exists {
		return make(map[string]string), nil // Return empty map if not found
	}
	headersCopy := make(map[string]string, len(headers))
	copyMap(headers, headersCopy)
	return headersCopy, nil
}

// NEW: SetSubscriptionHeaders sets subscription-specific headers (for testing/setup).
func (c *InternalConfig) SetSubscriptionHeaders(userID, serverSlug string, headers map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	subscriptionKey := fmt.Sprintf("%s:%s", userID, serverSlug)
	headersCopy := make(map[string]string, len(headers))
	copyMap(headers, headersCopy)
	c.subscriptionHeaders[subscriptionKey] = headersCopy
}

func (c *InternalConfig) GetA2AAgentCard(agentURL string) (*a2aSchema.AgentCard, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.A2AAgentNameValue == "" { // If A2A config isn't set, return nil/error
		return nil, fmt.Errorf("A2A configuration not set in InternalConfig")
	}
	info := &a2aSchema.AgentCard{
		Name: c.A2AAgentNameValue, Description: c.A2AAgentDescriptionValue, URL: agentURL,
		Version: c.A2AAgentVersionValue, DocumentationURL: c.A2ADocumentationURLValue,
		DefaultInputModes:  make([]string, len(c.A2ADefaultInputModesValue)),
		DefaultOutputModes: make([]string, len(c.A2ADefaultOutputModesValue)),
		Skills:             c.A2ASkills,
		// Assuming Capabilities and Authentication are not set in InternalConfig directly
	}
	copy(info.DefaultInputModes, c.A2ADefaultInputModesValue)
	copy(info.DefaultOutputModes, c.A2ADefaultOutputModesValue)
	if c.A2AProviderOrgValue != nil || c.A2AProviderURLValue != nil {
		info.Provider = &a2aSchema.AgentProvider{Organization: derefString(c.A2AProviderOrgValue), URL: c.A2AProviderURLValue}
	}
	return info, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func copyMap(src, dst map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}
