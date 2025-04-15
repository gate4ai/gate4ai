package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema" // Import A2A schema
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

// --- IConfig Implementation (Existing methods mostly unchanged, adding A2A) ---

func (c *DatabaseConfig) ListenAddr() (string, error) {
	return c.getSettingString("gateway_listen_address", ":8080") // Provide default
}

func (c *DatabaseConfig) AuthorizationType() (AuthorizationType, error) {
	// Get raw value first
	rawValue, err := c.getSettingJSON("gateway_authorization_type")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return AuthorizedUsersOnly, nil // Default if not found
		}
		return AuthorizedUsersOnly, err
	}

	// Handle both number and string representations from DB
	switch v := rawValue.(type) {
	case float64: // JSON numbers are float64
		return AuthorizationType(int(v)), nil
	case string: // Handle potential string values if they exist
		switch strings.ToLower(v) {
		case "authorizedusersonly", "users_only":
			return AuthorizedUsersOnly, nil
		case "notauthorizedtomarkedmethods", "marked_methods":
			return NotAuthorizedToMarkedMethods, nil
		case "notauthorizedeverywhere", "none":
			return NotAuthorizedEverywhere, nil
		default:
			// Attempt numeric conversion from string
			var authTypeInt int
			if _, scanErr := fmt.Sscanf(v, "%d", &authTypeInt); scanErr == nil {
				if authTypeInt >= int(AuthorizedUsersOnly) && authTypeInt <= int(NotAuthorizedEverywhere) {
					return AuthorizationType(authTypeInt), nil
				}
			}
			return AuthorizedUsersOnly, fmt.Errorf("invalid authorization type string value: %s", v)
		}
	default:
		return AuthorizedUsersOnly, fmt.Errorf("invalid authorization type format in database: %T", rawValue)
	}
}

func (c *DatabaseConfig) GetUserIDByKeyHash(keyHash string) (string, error) {
	if keyHash == "" {
		return "", nil
	} // Allow anonymous if key is empty and auth type allows

	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return "", fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()

	query := `SELECT "userId" FROM "ApiKey" WHERE "keyHash" = $1 LIMIT 1`
	var userID string
	err = db.QueryRow(query, keyHash).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		} // Use ErrNotFound
		return "", fmt.Errorf("query user by key hash: %w", err)
	}
	return userID, nil
}

func (c *DatabaseConfig) GetUserParams(userID string) (map[string]string, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()

	query := `SELECT name, status, role, company FROM "User" WHERE id = $1 LIMIT 1`
	var name, status, role, company sql.NullString
	err = db.QueryRow(query, userID).Scan(&name, &status, &role, &company)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("query user params: %w", err)
	}
	params := make(map[string]string)
	if name.Valid {
		params["name"] = name.String
	}
	if status.Valid {
		params["status"] = status.String
	}
	if role.Valid {
		params["role"] = role.String
	}
	if company.Valid {
		params["company"] = company.String
	}
	return params, nil
}

func (c *DatabaseConfig) GetUserSubscribes(userID string) ([]string, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()

	userParams, err := c.GetUserParams(userID) // Reuse existing method
	if err != nil {
		return nil, fmt.Errorf("get user params: %w", err)
	}
	role := userParams["role"] // Ignore existence check, default is handled below

	var query string
	var rows *sql.Rows
	if role == "ADMIN" || role == "SECURITY" {
		query = `SELECT slug FROM "Server" WHERE status = 'ACTIVE'` // Admins see active servers
		rows, err = db.Query(query)
	} else {
		// Non-admins see owned + active subscribed & active servers
		query = `
			SELECT slug from "Server"
            join "ServerOwner" ON "Server".id = "ServerOwner"."serverId" 
			WHERE "ServerOwner"."userId" = $1
			UNION
			SELECT slug FROM "Subscription" 
			JOIN "Server" ON "Subscription"."serverId" = "Server"."id"
			WHERE "userId" = $1
			AND "Subscription"."status" = 'ACTIVE'
			AND "Server"."status" = 'ACTIVE'
		`
		rows, err = db.Query(query, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	var serverSlugs []string
	for rows.Next() {
		var serverSlug string
		if scanErr := rows.Scan(&serverSlug); scanErr != nil {
			return nil, fmt.Errorf("scan subscription: %w", scanErr)
		}
		serverSlugs = append(serverSlugs, serverSlug)
	}
	return serverSlugs, rows.Err()
}

func (c *DatabaseConfig) GetBackendBySlug(backendSlug string) (*Backend, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()

	query := `SELECT "serverUrl" FROM "Server" WHERE slug = $1 LIMIT 1`
	var serverURL sql.NullString
	err = db.QueryRow(query, backendSlug).Scan(&serverURL)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query backend: %w", err)
	}
	if !serverURL.Valid {
		return nil, fmt.Errorf("backend URL is NULL for ID %s", backendSlug)
	}

	return &Backend{URL: serverURL.String}, nil
}

func (c *DatabaseConfig) ServerName() (string, error) {
	return c.getSettingString("gateway_server_name", "Gate4AI Gateway") // Default
}

func (c *DatabaseConfig) ServerVersion() (string, error) {
	return c.getSettingString("gateway_server_version", "1.0.0") // Default
}

func (c *DatabaseConfig) LogLevel() (string, error) {
	return c.getSettingString("gateway_log_level", "info") // Default
}

func (c *DatabaseConfig) DiscoveringHandlerPath() (string, error) {
	return c.getSettingString("path_for_discovering_handler", "") // Default empty
}

func (c *DatabaseConfig) FrontendAddressForProxy() (string, error) {
	return c.getSettingString("url_how_gateway_proxy_connect_to_the_portal", "http://portal:3000") // Default
}

func (c *DatabaseConfig) Status(ctx context.Context) error {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		c.logger.Error("DB connect failed", zap.Error(err))
		return err
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		c.logger.Error("DB ping failed", zap.Error(err))
		return err
	}
	return nil
}

// --- SSL Methods ---
func (c *DatabaseConfig) SSLEnabled() (bool, error) {
	return c.getSettingBool("gateway_ssl_enabled", false)
}
func (c *DatabaseConfig) SSLMode() (string, error) {
	return c.getSettingString("gateway_ssl_mode", "manual")
}
func (c *DatabaseConfig) SSLCertFile() (string, error) {
	return c.getSettingString("gateway_ssl_cert_file", "")
}
func (c *DatabaseConfig) SSLKeyFile() (string, error) {
	return c.getSettingString("gateway_ssl_key_file", "")
}
func (c *DatabaseConfig) SSLAcmeEmail() (string, error) {
	return c.getSettingString("gateway_ssl_acme_email", "")
}
func (c *DatabaseConfig) SSLAcmeCacheDir() (string, error) {
	return c.getSettingString("gateway_ssl_acme_cache_dir", "./.autocert-cache")
}
func (c *DatabaseConfig) SSLAcmeDomains() ([]string, error) {
	return c.getSettingStringSlice("gateway_ssl_acme_domains", []string{})
}

// --- A2A Method ---
func (c *DatabaseConfig) GetA2ACardBaseInfo(agentURL string) (A2ACardBaseInfo, error) {
	info := A2ACardBaseInfo{AgentURL: agentURL}
	var err error

	info.Name, err = c.getSettingString("a2a_agent_name", "Gate4AI A2A Agent")
	if err != nil {
		return info, fmt.Errorf("a2a_agent_name: %w", err)
	}

	desc, err := c.getSettingString("a2a_agent_description", "")
	if err != nil {
		return info, fmt.Errorf("a2a_agent_description: %w", err)
	}
	if desc != "" {
		info.Description = &desc
	}

	info.Version, err = c.getSettingString("a2a_agent_version", "1.0.0")
	if err != nil {
		return info, fmt.Errorf("a2a_agent_version: %w", err)
	}

	docURL, err := c.getSettingString("a2a_agent_documentation_url", "")
	if err != nil {
		return info, fmt.Errorf("a2a_agent_documentation_url: %w", err)
	}
	if docURL != "" {
		info.DocumentationURL = &docURL
	}

	info.DefaultInputModes, err = c.getSettingStringSlice("a2a_default_input_modes", []string{"text"})
	if err != nil {
		return info, fmt.Errorf("a2a_default_input_modes: %w", err)
	}

	info.DefaultOutputModes, err = c.getSettingStringSlice("a2a_default_output_modes", []string{"text"})
	if err != nil {
		return info, fmt.Errorf("a2a_default_output_modes: %w", err)
	}

	// Provider Info
	provOrg, errOrg := c.getSettingString("a2a_agent_provider_organization", "")
	provURL, errURL := c.getSettingString("a2a_agent_provider_url", "")
	// Only create provider if at least organization is set
	if errOrg == nil && provOrg != "" {
		provider := a2aSchema.AgentProvider{Organization: provOrg}
		if errURL == nil && provURL != "" {
			provider.URL = &provURL
		}
		info.Provider = &provider
	} else if errOrg != nil && !errors.Is(errOrg, ErrNotFound) {
		return info, fmt.Errorf("a2a_agent_provider_organization: %w", errOrg)
	} else if errURL != nil && !errors.Is(errURL, ErrNotFound) {
		return info, fmt.Errorf("a2a_agent_provider_url: %w", errURL)
	}

	// Authentication - Assuming simple structure for now, adjust if complex
	authJSON, err := c.getSettingString("a2a_agent_authentication", "")
	if err == nil && authJSON != "" && authJSON != "{}" && authJSON != "null" {
		var auth a2aSchema.AgentAuthentication
		if jsonErr := json.Unmarshal([]byte(authJSON), &auth); jsonErr != nil {
			c.logger.Error("Failed to unmarshal a2a_agent_authentication", zap.Error(jsonErr), zap.String("value", authJSON))
			return info, fmt.Errorf("invalid a2a_agent_authentication format: %w", jsonErr)
		}
		info.Authentication = &auth
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return info, fmt.Errorf("a2a_agent_authentication: %w", err)
	}

	return info, nil
}

// --- Database Helper Functions ---

// getSetting retrieves a setting value as JSON bytes
func (c *DatabaseConfig) getSettingRaw(key string) ([]byte, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()

	query := `SELECT value FROM "Settings" WHERE key = $1 LIMIT 1`
	var valueStr string
	err = db.QueryRowContext(context.Background(), query, key).Scan(&valueStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query setting '%s': %w", key, err)
	}
	return []byte(valueStr), nil
}

// getSettingJSON unmarshals a setting into an interface{}
func (c *DatabaseConfig) getSettingJSON(key string) (interface{}, error) {
	raw, err := c.getSettingRaw(key)
	if err != nil {
		return nil, err
	} // Propagate ErrNotFound

	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("unmarshal setting '%s': %w", key, err)
	}
	return value, nil
}

// getSettingString retrieves a string setting, handling potential number conversion
func (c *DatabaseConfig) getSettingString(key string, defaultValue string) (string, error) {
	value, err := c.getSettingJSON(key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return defaultValue, nil
		} // Use default if not found
		return defaultValue, err // Return default on other errors
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case float64: // Handle numeric values stored as JSON numbers
		return fmt.Sprintf("%v", int(v)), nil // Convert int part
	default:
		return defaultValue, fmt.Errorf("setting '%s' has unexpected type %T", key, value)
	}
}

// getSettingBool retrieves a boolean setting
func (c *DatabaseConfig) getSettingBool(key string, defaultValue bool) (bool, error) {
	value, err := c.getSettingJSON(key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return defaultValue, nil
		}
		return defaultValue, err
	}
	boolValue, ok := value.(bool)
	if !ok {
		return defaultValue, fmt.Errorf("setting '%s' is not a boolean (type: %T)", key, value)
	}
	return boolValue, nil
}

// getSettingStringSlice retrieves a setting expected to be a JSON array of strings
func (c *DatabaseConfig) getSettingStringSlice(key string, defaultValue []string) ([]string, error) {
	value, err := c.getSettingJSON(key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return defaultValue, nil
		}
		return defaultValue, err
	}
	if sliceInterface, ok := value.([]interface{}); ok {
		strSlice := make([]string, 0, len(sliceInterface))
		for i, item := range sliceInterface {
			if strVal, ok := item.(string); ok {
				strSlice = append(strSlice, strVal)
			} else {
				return defaultValue, fmt.Errorf("non-string value at index %d in setting '%s'", i, key)
			}
		}
		return strSlice, nil
	}
	// Handle case where it might already be []string (less common from DB json)
	if strSlice, ok := value.([]string); ok {
		return strSlice, nil
	}
	return defaultValue, fmt.Errorf("setting '%s' is not a JSON array of strings (type: %T)", key, value)
}
