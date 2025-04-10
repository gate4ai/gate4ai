package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

var _ IConfig = (*DatabaseConfig)(nil)

// DatabaseConfig implements all configuration interfaces with PostgreSQL database-based storage
type DatabaseConfig struct {
	logger             *zap.Logger
	dbConnectionString string
}

// DatabaseConfigOptions contains options for configuring the DatabaseConfig
type DatabaseConfigOptions struct {
	// Optional parameters for database configuration
}

// DefaultDatabaseConfigOptions returns the default options for DatabaseConfig
func DefaultDatabaseConfigOptions() DatabaseConfigOptions {
	return DatabaseConfigOptions{}
}

// NewDatabaseConfig creates a new DatabaseConfig instance with default options
func NewDatabaseConfig(dbConnectionString string, logger *zap.Logger) (*DatabaseConfig, error) {
	return NewDatabaseConfigWithOptions(dbConnectionString, logger, DefaultDatabaseConfigOptions())
}

// NewDatabaseConfigWithOptions creates a new DatabaseConfig instance with the specified options
func NewDatabaseConfigWithOptions(dbConnectionString string, logger *zap.Logger, options DatabaseConfigOptions) (*DatabaseConfig, error) {
	// Create a new database config
	config := &DatabaseConfig{
		dbConnectionString: dbConnectionString,
		logger:             logger,
	}

	return config, nil
}

// Close closes any resources held by the config
func (c *DatabaseConfig) Close() error {
	return nil
}

// ServerConfig interface implementation

// ListenAddr returns the configured server listen address
func (c *DatabaseConfig) ListenAddr() (string, error) {
	return c.getSettingString("gateway_listen_address")
}

// Authorization returns the authorization type based on configuration
func (c *DatabaseConfig) AuthorizationType() (AuthorizationType, error) {
	authTypeStr, err := c.getSettingString("gateway_authorization_type")
	if err != nil {
		return AuthorizedUsersOnly, err
	}

	// Convert string to int
	var authTypeInt int
	if _, err := fmt.Sscanf(authTypeStr, "%d", &authTypeInt); err != nil {
		return AuthorizedUsersOnly, fmt.Errorf("failed to parse authorization type: %w", err)
	}

	return AuthorizationType(authTypeInt), nil
}

// UsersConfig interface implementation

// GetUserIDByKeyHash returns the user ID for the given key hash by querying the database
func (c *DatabaseConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	if keyHash == "" {
		return "", fmt.Errorf("token hash not found")
	}

	// Open a connection to the database
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return "", fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Query to get user ID from the API key hash
	query := `SELECT "userId" FROM "ApiKey" WHERE "keyHash" = $1 LIMIT 1`
	var userID string
	err = db.QueryRow(query, keyHash).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("token not found")
		}
		return "", fmt.Errorf("failed to get user ID: %w", err)
	}

	return userID, nil
}

// GetUserParams returns the parameters for the given user ID
func (c *DatabaseConfig) GetUserParams(userID string) (map[string]string, error) {
	// Open a connection to the database
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Query to get user parameters from the database
	query := `SELECT name, status, role, company FROM "User" WHERE id = $1 LIMIT 1`
	var name sql.NullString
	var status string
	var role string
	var company sql.NullString

	err = db.QueryRow(query, userID).Scan(&name, &status, &role, &company)
	if err != nil {
		if err == sql.ErrNoRows {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("failed to get user parameters: %w", err)
	}

	// Create the parameters map
	params := make(map[string]string)

	if name.Valid {
		params["name"] = name.String
	}

	params["status"] = status
	params["role"] = role

	if company.Valid {
		params["company"] = company.String
	}

	return params, nil
}

// UserSubscribesConfig interface implementation

// GetUserSubscribes returns the server IDs that the user is subscribed to
func (c *DatabaseConfig) GetUserSubscribes(userID string) ([]string, error) {
	// Open a connection to the database
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Get user role first
	userParams, err := c.GetUserParams(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user parameters: %w", err)
	}

	role, exists := userParams["role"]
	if !exists {
		return nil, fmt.Errorf("user role not found")
	}

	var query string
	var rows *sql.Rows

	// Handle different user roles
	switch role {
	case "ADMIN", "SECURITY":
		// Admins and security users get all servers
		query = `SELECT id FROM "Server"`
		rows, err = db.Query(query)
	default:
		// Owners get servers they own + active subscriptions
		query = `
			SELECT "serverId" FROM "ServerOwner"
			WHERE "userId" = $1
			UNION
			SELECT "serverId" FROM "Subscription" 
			JOIN "Server" ON "Subscription"."serverId" = "Server"."id"
			WHERE "userId" = $1
			AND "Subscription"."status" = 'ACTIVE'
			AND "Server"."status" = 'ACTIVE'
		`
		rows, err = db.Query(query, userID)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get user subscriptions: %w", err)
	}
	defer rows.Close()

	var serverIDs []string
	for rows.Next() {
		var serverID string
		if err := rows.Scan(&serverID); err != nil {
			return nil, fmt.Errorf("failed to scan subscription row: %w", err)
		}
		serverIDs = append(serverIDs, serverID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating through subscription rows: %w", err)
	}

	return serverIDs, nil
}

// ServersConfig interface implementation

// GetServer returns the URL for the given server ID
func (c *DatabaseConfig) GetBackend(backendID string) (*Backend, error) {
	// Open a connection to the database
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Query to get server details from the database
	query := `SELECT "serverUrl" FROM "Server" WHERE id = $1 LIMIT 1`
	var serverURL string
	err = db.QueryRow(query, backendID).Scan(&serverURL)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get server details: %w", err)
	}

	// For now, we're not setting the Bearer token - this would require additional logic
	// to determine the appropriate token for the given server
	return &Backend{
		URL:    serverURL,
		Bearer: "", // This may need to be filled in from a different source
	}, nil
}

func (c *DatabaseConfig) ServerName() (string, error) {
	return c.getSettingString("gateway_server_name")
}

func (c *DatabaseConfig) ServerVersion() (string, error) {
	return c.getSettingString("gateway_server_version")
}

// LogLevel returns the configured log level for Zap
func (c *DatabaseConfig) LogLevel() (string, error) {
	return c.getSettingString("gateway_log_level")
}

// InfoHandler returns the information handler path from settings
func (c *DatabaseConfig) InfoHandler() (string, error) {
	// Open a connection to the database
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return "", fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Query to get info handler setting
	query := `SELECT value FROM "Settings" WHERE key = 'general_gateway_info_handler'`
	var valueStr string
	err = db.QueryRow(query).Scan(&valueStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil // Return empty string if setting doesn't exist
		}
		return "", fmt.Errorf("failed to get info handler setting: %w", err)
	}

	// Parse value JSON
	var value interface{}
	if err := json.Unmarshal([]byte(valueStr), &value); err != nil {
		return "", fmt.Errorf("failed to unmarshal info handler value: %w", err)
	}

	// Convert to string
	infoHandler, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("info handler value is not a string")
	}

	return infoHandler, nil
}

// FrontendAddressForProxy returns the address for the frontend proxy from settings
func (c *DatabaseConfig) FrontendAddressForProxy() (string, error) {
	return c.getSettingString("url_how_gateway_proxy_connect_to_the_portal")
}

func (c *DatabaseConfig) Status(ctx context.Context) error {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		c.logger.Error("Failed to connect to database", zap.Error(err))
		return err
	} else {
		err = db.PingContext(ctx)
		if err != nil {
			c.logger.Error("Failed to ping database", zap.Error(err))
			return err
		}
		db.Close()
	}
	return nil
}

// getSettingString retrieves a string value from the Settings table
func (c *DatabaseConfig) getSettingString(key string) (string, error) {
	// Open a connection to the database
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return "", fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Query to get the setting
	query := `SELECT value FROM "Settings" WHERE key = $1 LIMIT 1`
	var valueStr string
	err = db.QueryRow(query, key).Scan(&valueStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("setting not found: %s", key)
		}
		return "", fmt.Errorf("failed to get setting: %w", err)
	}

	// Parse value JSON
	var value interface{}
	if err := json.Unmarshal([]byte(valueStr), &value); err != nil {
		return "", fmt.Errorf("failed to unmarshal setting value: %w", err)
	}

	// Convert to string
	strValue, ok := value.(string)
	if !ok {
		floatValue, isFloat := value.(float64)
		if isFloat {
			// Convert numeric value to string (for items like gateway_authorization_type)
			return fmt.Sprintf("%v", int(floatValue)), nil
		}
		return "", fmt.Errorf("setting value is not a string or number: %s", key)
	}

	return strValue, nil
}

// --- Implement New SSL Methods ---

func (c *DatabaseConfig) SSLEnabled() (bool, error) {
	// Default to false if setting not found or error occurs
	val, err := c.getSettingBool("gateway_ssl_enabled")
	if err != nil && !errors.Is(err, ErrNotFound) {
		c.logger.Error("Error reading gateway_ssl_enabled", zap.Error(err))
	}
	return val, nil // Return default (false) on error or not found
}

func (c *DatabaseConfig) SSLMode() (string, error) {
	// Default to "manual" if not found
	val, err := c.getSettingString("gateway_ssl_mode")
	if err != nil && !errors.Is(err, ErrNotFound) {
		c.logger.Error("Error reading gateway_ssl_mode", zap.Error(err))
	}
	if errors.Is(err, ErrNotFound) || val == "" {
		return "manual", nil
	}
	return val, nil
}

func (c *DatabaseConfig) SSLCertFile() (string, error) {
	val, err := c.getSettingString("gateway_ssl_cert_file")
	if err != nil && !errors.Is(err, ErrNotFound) {
		c.logger.Error("Error reading gateway_ssl_cert_file", zap.Error(err))
	}
	return val, nil // Return "" on error or not found
}

func (c *DatabaseConfig) SSLKeyFile() (string, error) {
	val, err := c.getSettingString("gateway_ssl_key_file")
	if err != nil && !errors.Is(err, ErrNotFound) {
		c.logger.Error("Error reading gateway_ssl_key_file", zap.Error(err))
	}
	return val, nil // Return "" on error or not found
}

func (c *DatabaseConfig) SSLAcmeDomains() ([]string, error) {
	value, err := c.getSettingJSON("gateway_ssl_acme_domains")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return []string{}, nil // Return empty slice if not found
		}
		c.logger.Error("Error reading gateway_ssl_acme_domains", zap.Error(err))
		return []string{}, err
	}

	// Type assert to []interface{} first, then convert to []string
	if domainsInterface, ok := value.([]interface{}); ok {
		domains := make([]string, 0, len(domainsInterface))
		for _, item := range domainsInterface {
			if domainStr, ok := item.(string); ok {
				domains = append(domains, domainStr)
			} else {
				err := fmt.Errorf("non-string value found in ACME domains list for key 'gateway_ssl_acme_domains'")
				c.logger.Error(err.Error())
				return []string{}, err
			}
		}
		return domains, nil
	}
	// Handle case where it's already []string (less likely with json.Unmarshal)
	if domainsStr, ok := value.([]string); ok {
		return domainsStr, nil
	}

	err = fmt.Errorf("setting 'gateway_ssl_acme_domains' has invalid format, expected JSON array of strings")
	c.logger.Error(err.Error())
	return []string{}, err
}

func (c *DatabaseConfig) SSLAcmeEmail() (string, error) {
	val, err := c.getSettingString("gateway_ssl_acme_email")
	if err != nil && !errors.Is(err, ErrNotFound) {
		c.logger.Error("Error reading gateway_ssl_acme_email", zap.Error(err))
	}
	return val, nil // Return "" on error or not found
}

func (c *DatabaseConfig) SSLAcmeCacheDir() (string, error) {
	val, err := c.getSettingString("gateway_ssl_acme_cache_dir")
	if err != nil && !errors.Is(err, ErrNotFound) {
		c.logger.Error("Error reading gateway_ssl_acme_cache_dir", zap.Error(err))
	}
	if errors.Is(err, ErrNotFound) || val == "" {
		return "./.autocert-cache", nil // Default cache dir
	}
	return val, nil
}

// --- Helper functions to get typed settings ---

// getSettingJSON retrieves a raw JSON value from the Settings table
func (c *DatabaseConfig) getSettingJSON(key string) (interface{}, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	query := `SELECT value FROM "Settings" WHERE key = $1 LIMIT 1`
	var valueStr string

	ctx := context.Background() // Replace with c.ctx if watcher is active
	err = db.QueryRowContext(ctx, query, key).Scan(&valueStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound // Use specific error for not found
		}
		return nil, fmt.Errorf("failed to get setting '%s': %w", key, err)
	}

	var value interface{}
	if err := json.Unmarshal([]byte(valueStr), &value); err != nil {
		return nil, fmt.Errorf("failed to unmarshal setting value for '%s': %w", key, err)
	}
	return value, nil
}

// getSettingBool retrieves a boolean setting
func (c *DatabaseConfig) getSettingBool(key string) (bool, error) {
	value, err := c.getSettingJSON(key)
	if err != nil {
		return false, err // Propagate ErrNotFound or other errors
	}
	boolValue, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("setting '%s' value is not a boolean", key)
	}
	return boolValue, nil
}
