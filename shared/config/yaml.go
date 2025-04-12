package config

import (
	"context"
	"fmt"
	"os"
	"sync"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

var _ IConfig = (*YamlConfig)(nil)

// YamlConfig implements all configuration interfaces with YAML file-based storage
type YamlConfig struct {
	mu         sync.RWMutex
	configPath string
	logger     *zap.Logger

	// Parsed configuration
	serverAddress               string
	serverName                  string
	serverVersion               string
	logLevel                    string
	DiscoveringHandlerPathValue string
	frontendAddressValue        string
	authorizationType           AuthorizationType
	userAuthKeys                map[string]string            // authKey -> userID
	userParams                  map[string]map[string]string // userID -> paramName -> paramValue
	userSubscribes              map[string][]string          // userID -> serverIDs
	backends                    map[string]*Backend          // serverID -> Server

	// SSL Fields
	sslEnabled      bool
	sslMode         string
	sslCertFile     string
	sslKeyFile      string
	sslAcmeDomains  []string
	sslAcmeEmail    string
	sslAcmeCacheDir string
}

// YAML configuration structure matching the required format
type yamlConfig struct {
	Server struct {
		Address                string   `yaml:"address"`
		Name                   string   `yaml:"name"`
		Version                string   `yaml:"version"`
		LogLevel               string   `yaml:"log_level"`
		DiscoveringHandlerPath string   `yaml:"info_handler"`
		FrontendAddress        string   `yaml:"frontend_address"`
		Authorization          string   `yaml:"authorization"` // Can be "users_only", "marked_methods", or "none"
		SSL                    struct { // New SSL section
			Enabled      bool     `yaml:"enabled"`
			Mode         string   `yaml:"mode"`           // "manual" or "acme"
			CertFile     string   `yaml:"cert_file"`      // Path for manual mode
			KeyFile      string   `yaml:"key_file"`       // Path for manual mode
			AcmeDomains  []string `yaml:"acme_domains"`   // Domains for ACME
			AcmeEmail    string   `yaml:"acme_email"`     // Contact email for ACME
			AcmeCacheDir string   `yaml:"acme_cache_dir"` // Cache directory for ACME
		} `yaml:"ssl"`
	} `yaml:"server"`

	Users map[string]struct {
		Keys       []string `yaml:"keys"`
		Subscribes []string `yaml:"subscribes"`
	} `yaml:"users"`

	Backends map[string]struct {
		URL    string `yaml:"url"`
		Bearer string `yaml:"bearer"`
	} `yaml:"backends"`
}

// NewYamlConfig creates a new YAML-based configuration
func NewYamlConfig(configPath string, logger *zap.Logger) (*YamlConfig, error) {
	return NewYamlConfigWithOptions(configPath, logger)
}

// NewYamlConfigWithOptions creates a new YAML-based configuration with specified options
func NewYamlConfigWithOptions(configPath string, logger *zap.Logger) (*YamlConfig, error) {
	if logger == nil {
		// Create a default logger if none provided
		var err error
		logger, err = zap.NewProduction()
		if err != nil {
			return nil, err
		}
	}

	config := &YamlConfig{
		configPath:        configPath,
		logger:            logger,
		userAuthKeys:      make(map[string]string),
		userParams:        make(map[string]map[string]string),
		userSubscribes:    make(map[string][]string),
		backends:          make(map[string]*Backend),
		authorizationType: AuthorizedUsersOnly, // Default to requiring authorization
		// Default SSL settings
		sslMode:         "manual",
		sslAcmeCacheDir: "./.autocert-cache", // Default cache dir
	}

	// Load initial configuration
	if err := config.Update(); err != nil {
		return nil, err
	}

	return config, nil
}

// Update reloads configuration from the YAML file
func (c *YamlConfig) Update() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.logger.Info("Updating configuration from YAML file", zap.String("path", c.configPath))

	// Read and parse the configuration file
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		c.logger.Error("Failed to read configuration file", zap.String("path", c.configPath), zap.Error(err))
		return err
	}

	var yamlCfg yamlConfig
	if err := yaml.Unmarshal(data, &yamlCfg); err != nil {
		c.logger.Error("Failed to parse YAML configuration", zap.Error(err))
		return err
	}

	// Process server configuration
	c.serverAddress = yamlCfg.Server.Address
	c.serverName = yamlCfg.Server.Name
	c.serverVersion = yamlCfg.Server.Version
	c.logLevel = yamlCfg.Server.LogLevel
	c.DiscoveringHandlerPathValue = yamlCfg.Server.DiscoveringHandlerPath
	c.frontendAddressValue = yamlCfg.Server.FrontendAddress

	// Process SSL settings
	c.sslEnabled = yamlCfg.Server.SSL.Enabled
	c.sslMode = yamlCfg.Server.SSL.Mode
	c.sslCertFile = yamlCfg.Server.SSL.CertFile
	c.sslKeyFile = yamlCfg.Server.SSL.KeyFile
	c.sslAcmeDomains = yamlCfg.Server.SSL.AcmeDomains
	c.sslAcmeEmail = yamlCfg.Server.SSL.AcmeEmail
	c.sslAcmeCacheDir = yamlCfg.Server.SSL.AcmeCacheDir
	// Provide defaults if values are missing
	if c.sslMode == "" {
		c.sslMode = "manual"
	}
	if c.sslAcmeCacheDir == "" {
		c.sslAcmeCacheDir = "./.autocert-cache"
	}

	// Process authorization type
	switch yamlCfg.Server.Authorization {
	case "users_only":
		c.authorizationType = AuthorizedUsersOnly
	case "marked_methods":
		c.authorizationType = NotAuthorizedToMarkedMethods
	case "none":
		c.authorizationType = NotAuthorizedEverywhere
	default:
		// Default to requiring authorization for all users if not specified
		c.authorizationType = AuthorizedUsersOnly
	}

	// Process users and their auth keys
	oldUserAuthKeys := c.userAuthKeys
	c.userAuthKeys = make(map[string]string)
	c.userSubscribes = make(map[string][]string)

	// Collect all users for which we need to call the callbacks
	affectedUsers := make(map[string]bool)

	for userID, user := range yamlCfg.Users {
		// Process auth keys
		for _, authKey := range user.Keys {
			c.userAuthKeys[authKey] = userID
			if oldUserID, exists := oldUserAuthKeys[authKey]; !exists || oldUserID != userID {
				affectedUsers[userID] = true
			}
		}

		// Process subscribes
		if len(user.Subscribes) > 0 {
			c.userSubscribes[userID] = make([]string, len(user.Subscribes))
			copy(c.userSubscribes[userID], user.Subscribes)
		}
	}

	// Check for removed auth keys
	for authKey, userID := range oldUserAuthKeys {
		if _, exists := c.userAuthKeys[authKey]; !exists {
			affectedUsers[userID] = true
		}
	}

	// Process servers
	c.backends = make(map[string]*Backend)
	for backendID, backend := range yamlCfg.Backends {
		c.backends[backendID] = &Backend{URL: backend.URL, Bearer: backend.Bearer}
	}

	return nil
}

// Close stops the file watcher and cleans up resources
func (c *YamlConfig) Close() error {
	return nil
}

// ListenAddr returns the server address
func (c *YamlConfig) ListenAddr() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverAddress, nil
}

func (c *YamlConfig) SetListenAddr(add string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.serverAddress = add
}

// GetUserIDByKeyHash returns the user ID associated with the given key hash
func (c *YamlConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Load the file again to ensure we have the latest data
	var config yamlConfig
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Iterate through users to find the matching key hash
	for userID, user := range config.Users {
		for _, hash := range user.Keys {
			if hash == keyHash {
				return userID, nil
			}
		}
	}

	return "", nil // Return empty string if hash not found
}

// GetUserParams returns the parameters for the given user ID
func (c *YamlConfig) GetUserParams(userID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	params, exists := c.userParams[userID]
	if !exists {
		return make(map[string]string), nil
	}

	// Return a copy to prevent concurrent map access
	result := make(map[string]string, len(params))
	for k, v := range params {
		result[k] = v
	}

	return result, nil
}

// GetUserSubscribes returns the server IDs that the user is subscribed to
func (c *YamlConfig) GetUserSubscribes(userID string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	servers, exists := c.userSubscribes[userID]
	if !exists {
		return []string{}, nil
	}

	serversCopy := make([]string, len(servers))
	copy(serversCopy, servers)
	return serversCopy, nil
}

// GetServer returns the URL for the given server ID
func (c *YamlConfig) GetBackend(backendID string) (*Backend, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	backend, exists := c.backends[backendID]
	if !exists {
		return nil, ErrNotFound
	}

	return backend, nil
}

// Authorization returns the configured authorization type
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

// LogLevel returns the configured log level for Zap
func (c *YamlConfig) LogLevel() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.logLevel, nil
}

// DiscoveringHandlerPath returns the configured info handler path
func (c *YamlConfig) DiscoveringHandlerPath() (string, error) {
	// For YAML config, we don't have this setting
	// Return a default value or empty string
	return c.DiscoveringHandlerPathValue, nil
}

// FrontendAddressForProxy returns the frontend address for proxy
func (c *YamlConfig) FrontendAddressForProxy() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.frontendAddressValue, nil
}

func (c *YamlConfig) Status(ctx context.Context) error {
	return nil
}

// --- Implement SSL Methods ---

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
	// Return a copy to prevent modification of the internal slice
	domainsCopy := make([]string, len(c.sslAcmeDomains))
	copy(domainsCopy, c.sslAcmeDomains)
	return domainsCopy, nil
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
