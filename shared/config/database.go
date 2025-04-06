package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

var _ IConfig = (*DatabaseConfig)(nil)

// DatabaseConfig implements all configuration interfaces with PostgreSQL database-based storage
type DatabaseConfig struct {
	logger             *zap.Logger
	dbConnectionString string

	// For watcher
	ctx        context.Context
	cancelFunc context.CancelFunc
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

// StartWatcher starts a goroutine that periodically updates the configuration from the database
func (c *DatabaseConfig) StartWatcher() error {
	// Create context for watcher
	c.ctx, c.cancelFunc = context.WithCancel(context.Background())

	// Open a connection to the database to get the reload interval
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Get reload interval setting
	var reloadIntervalSecs int64 = 60 // Default to 60 seconds
	row := db.QueryRow(`SELECT value FROM "Settings" WHERE key = 'gateway_reload_every_seconds'`)
	var valueStr string
	if err := row.Scan(&valueStr); err == nil {
		var value interface{}
		if err := json.Unmarshal([]byte(valueStr), &value); err == nil {
			if floatValue, ok := value.(float64); ok {
				reloadIntervalSecs = int64(floatValue)
			}
		}
	}

	c.logger.Info("Config watcher started", zap.Int64("intervalSeconds", reloadIntervalSecs))
	return nil
}

// Stop the configuration watcher
func (c *DatabaseConfig) StopWatcher() {
	if c.cancelFunc != nil {
		c.cancelFunc()
		c.cancelFunc = nil
	}
}

// Close closes any resources held by the config
func (c *DatabaseConfig) Close() error {
	c.StopWatcher()
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
	return c.getSettingString("gateway_frontend_address_for_proxy")
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
