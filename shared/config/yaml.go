package config

import (
	"context" // Import errors package
	"fmt"
	"os"
	"strings"
	"sync"

	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Ensure YamlConfig implements IConfig
var _ IConfig = (*YamlConfig)(nil)

// YamlConfig implements all configuration interfaces with YAML file-based storage
type YamlConfig struct {
	mu                          sync.RWMutex
	configPath                  string
	logger                      *zap.Logger
	serverAddress               string
	serverName                  string
	serverVersion               string
	logLevel                    string
	DiscoveringHandlerPathValue string
	frontendAddressValue        string
	authorizationType           AuthorizationType
	userKeyHashes               map[string]string
	userParams                  map[string]map[string]string
	userSubscribes              map[string][]string
	backends                    map[string]*Backend

	// SSL Fields
	sslEnabled      bool
	sslMode         string
	sslCertFile     string
	sslKeyFile      string
	sslAcmeDomains  []string
	sslAcmeEmail    string
	sslAcmeCacheDir string

	// A2A Fields
	a2a *a2aSchema.AgentCard
}

// YAML configuration structure matching the required format
type yamlConfig struct {
	Server struct {
		Address                string               `yaml:"address"`
		Name                   string               `yaml:"name"`
		Version                string               `yaml:"version"`
		LogLevel               string               `yaml:"log_level"`
		DiscoveringHandlerPath string               `yaml:"info_handler"`
		FrontendAddress        string               `yaml:"frontend_address"`
		Authorization          string               `yaml:"authorization"`
		SSL                    yamlSSLConfig        `yaml:"ssl"`
		A2A                    *a2aSchema.AgentCard `yaml:"a2a"`
	} `yaml:"server"`
	Users    map[string]yamlUserConfig    `yaml:"users"`
	Backends map[string]yamlBackendConfig `yaml:"backends"`
}

type yamlUserConfig struct {
	Keys       []string `yaml:"keys"`
	Subscribes []string `yaml:"subscribes"`
}

type yamlBackendConfig struct {
	URL    string `yaml:"url"`
	Bearer string `yaml:"bearer"` // Corrected yaml tag
}

type yamlSSLConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Mode         string   `yaml:"mode"`
	CertFile     string   `yaml:"cert_file"`
	KeyFile      string   `yaml:"key_file"`
	AcmeDomains  []string `yaml:"acme_domains"`
	AcmeEmail    string   `yaml:"acme_email"`
	AcmeCacheDir string   `yaml:"acme_cache_dir"`
}

// NewYamlConfig creates a new YAML-based configuration
func NewYamlConfig(configPath string, logger *zap.Logger) (*YamlConfig, error) {
	return NewYamlConfigWithOptions(configPath, logger)
}

// NewYamlConfigWithOptions creates a new YAML-based configuration with specified options
func NewYamlConfigWithOptions(configPath string, logger *zap.Logger) (*YamlConfig, error) {
	if logger == nil {
		logger, _ = zap.NewProduction()
	}
	config := &YamlConfig{
		configPath:        configPath,
		logger:            logger,
		userKeyHashes:     make(map[string]string),
		userParams:        make(map[string]map[string]string),
		userSubscribes:    make(map[string][]string),
		backends:          make(map[string]*Backend),
		authorizationType: AuthorizedUsersOnly, // Default
		sslMode:           "manual",
		sslAcmeCacheDir:   "./.autocert-cache",
	}
	if err := config.Update(); err != nil {
		return nil, err
	}
	return config, nil
}

// Update reloads configuration from the YAML file
func (c *YamlConfig) Update() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger.Debug("Updating configuration from YAML", zap.String("path", c.configPath))
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	var yamlCfg yamlConfig
	if err := yaml.Unmarshal(data, &yamlCfg); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}

	// Process Server Section
	c.serverAddress = yamlCfg.Server.Address
	c.serverName = yamlCfg.Server.Name
	c.serverVersion = yamlCfg.Server.Version
	c.logLevel = yamlCfg.Server.LogLevel
	c.DiscoveringHandlerPathValue = yamlCfg.Server.DiscoveringHandlerPath
	c.frontendAddressValue = yamlCfg.Server.FrontendAddress
	switch strings.ToLower(yamlCfg.Server.Authorization) {
	case "marked_methods":
		c.authorizationType = NotAuthorizedToMarkedMethods
	case "none":
		c.authorizationType = NotAuthorizedEverywhere
	default:
		c.authorizationType = AuthorizedUsersOnly
	}

	// Process SSL Section
	c.sslEnabled = yamlCfg.Server.SSL.Enabled
	c.sslMode = strings.ToLower(yamlCfg.Server.SSL.Mode)
	if c.sslMode != "acme" {
		c.sslMode = "manual"
	}
	c.sslCertFile = yamlCfg.Server.SSL.CertFile
	c.sslKeyFile = yamlCfg.Server.SSL.KeyFile
	c.sslAcmeDomains = yamlCfg.Server.SSL.AcmeDomains
	c.sslAcmeEmail = yamlCfg.Server.SSL.AcmeEmail
	c.sslAcmeCacheDir = yamlCfg.Server.SSL.AcmeCacheDir
	if c.sslAcmeCacheDir == "" {
		c.sslAcmeCacheDir = "./.autocert-cache"
	}

	// Process A2A section
	c.a2a = yamlCfg.Server.A2A

	// Process Users Section
	newUserKeyHashes := make(map[string]string)
	newUserSubscribes := make(map[string][]string)
	for userID, user := range yamlCfg.Users {
		for _, keyHash := range user.Keys {
			newUserKeyHashes[keyHash] = userID
		}
		if len(user.Subscribes) > 0 {
			ns := make([]string, len(user.Subscribes))
			copy(ns, user.Subscribes)
			newUserSubscribes[userID] = ns
		}
	}
	c.userKeyHashes = newUserKeyHashes
	c.userSubscribes = newUserSubscribes

	// Process Backends Section
	newBackends := make(map[string]*Backend)
	for backendID, backend := range yamlCfg.Backends {
		newBackends[backendID] = &Backend{URL: backend.URL, Bearer: backend.Bearer}
	}
	c.backends = newBackends

	return nil
}

// --- IConfig Implementation ---

func (c *YamlConfig) Close() error { return nil }
func (c *YamlConfig) ListenAddr() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverAddress, nil
}
func (c *YamlConfig) AuthorizationType() (AuthorizationType, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authorizationType, nil
}
func (c *YamlConfig) ServerName() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverName, nil
}
func (c *YamlConfig) ServerVersion() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverVersion, nil
}
func (c *YamlConfig) LogLevel() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.logLevel, nil
}
func (c *YamlConfig) DiscoveringHandlerPath() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.DiscoveringHandlerPathValue, nil
}
func (c *YamlConfig) FrontendAddressForProxy() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.frontendAddressValue, nil
}
func (c *YamlConfig) SSLEnabled() (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslEnabled, nil
}
func (c *YamlConfig) SSLMode() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslMode, nil
}
func (c *YamlConfig) SSLCertFile() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslCertFile, nil
}
func (c *YamlConfig) SSLKeyFile() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslKeyFile, nil
}
func (c *YamlConfig) SSLAcmeDomains() ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	dc := make([]string, len(c.sslAcmeDomains))
	copy(dc, c.sslAcmeDomains)
	return dc, nil
}
func (c *YamlConfig) SSLAcmeEmail() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslAcmeEmail, nil
}
func (c *YamlConfig) SSLAcmeCacheDir() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sslAcmeCacheDir, nil
}
func (c *YamlConfig) Status(ctx context.Context) error {
	if _, err := os.Stat(c.configPath); err != nil {
		return fmt.Errorf("config file error: %w", err)
	}
	return nil
}

func (c *YamlConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if keyHash == "" {
		return "", nil
	}
	userID, exists := c.userKeyHashes[keyHash]
	if !exists {
		return "", ErrNotFound
	}
	return userID, nil
}
func (c *YamlConfig) GetUserParams(userID string) (map[string]string, error) {
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
func (c *YamlConfig) GetUserSubscribes(userID string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	servers, exists := c.userSubscribes[userID]
	if !exists {
		return []string{}, nil
	}
	sc := make([]string, len(servers))
	copy(sc, servers)
	return sc, nil
}
func (c *YamlConfig) GetBackendBySlug(backendID string) (*Backend, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	backend, exists := c.backends[backendID]
	if !exists {
		return nil, ErrNotFound
	}
	bc := *backend
	return &bc, nil
}

// NEW: GetServerHeaders returns empty map for YAML config.
func (c *YamlConfig) GetServerHeaders(serverSlug string) (map[string]string, error) {
	return make(map[string]string), nil
}

// NEW: GetSubscriptionHeaders returns empty map for YAML config.
func (c *YamlConfig) GetSubscriptionHeaders(userID, serverSlug string) (map[string]string, error) {
	return make(map[string]string), nil
}

func (c *YamlConfig) GetA2AAgentCard(agentURL string) (*a2aSchema.AgentCard, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.a2a == nil {
		return nil, fmt.Errorf("A2A configuration not present in YAML")
	}
	// Return a copy, updating the URL
	cardCopy := *c.a2a
	cardCopy.URL = agentURL
	return &cardCopy, nil
}
