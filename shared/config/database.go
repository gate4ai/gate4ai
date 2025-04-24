package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
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

// --- IConfig Implementation ---

func (c *DatabaseConfig) ListenAddr() (string, error) {
	return c.getSettingString("gateway_listen_address", ":8080")
}

func (c *DatabaseConfig) AuthorizationType() (AuthorizationType, error) {
	rawValue, err := c.getSettingJSON("gateway_authorization_type")
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return AuthorizedUsersOnly, nil
		}
		return AuthorizedUsersOnly, err
	}
	switch v := rawValue.(type) {
	case float64:
		return AuthorizationType(int(v)), nil
	case string:
		switch strings.ToLower(v) {
		case "authorizedusersonly", "users_only":
			return AuthorizedUsersOnly, nil
		case "notauthorizedtomarkedmethods", "marked_methods":
			return NotAuthorizedToMarkedMethods, nil
		case "notauthorizedeverywhere", "none":
			return NotAuthorizedEverywhere, nil
		default:
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
	}

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
		}
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

	userParams, err := c.GetUserParams(userID)
	if err != nil {
		return nil, fmt.Errorf("get user params: %w", err)
	}
	role := userParams["role"]

	var query string
	var rows *sql.Rows
	if role == "ADMIN" || role == "SECURITY" {
		query = `SELECT slug FROM "Server" WHERE status = 'ACTIVE'`
		rows, err = db.Query(query)
	} else {
		query = `
			SELECT slug from "Server"
            JOIN "ServerOwner" ON "Server".id = "ServerOwner"."serverId"
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

// NEW: GetServerHeaders retrieves the server-specific headers.
func (c *DatabaseConfig) GetServerHeaders(serverSlug string) (map[string]string, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()

	query := `SELECT headers FROM "Server" WHERE slug = $1 LIMIT 1`
	var headersJSON sql.NullString
	err = db.QueryRow(query, serverSlug).Scan(&headersJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query server headers for slug '%s': %w", serverSlug, err)
	}

	if !headersJSON.Valid || headersJSON.String == "" {
		return make(map[string]string), nil // Return empty map if NULL or empty
	}

	var headers map[string]string
	if err := json.Unmarshal([]byte(headersJSON.String), &headers); err != nil {
		return nil, fmt.Errorf("unmarshal server headers for slug '%s': %w", serverSlug, err)
	}

	return headers, nil
}

// NEW: GetSubscriptionHeaders retrieves the subscription-specific headers for a user and server.
func (c *DatabaseConfig) GetSubscriptionHeaders(userID, serverSlug string) (map[string]string, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()

	// Find server ID first
	serverIDQuery := `SELECT id FROM "Server" WHERE slug = $1 LIMIT 1`
	var serverID string
	err = db.QueryRow(serverIDQuery, serverSlug).Scan(&serverID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("server not found for slug '%s'", serverSlug)
		}
		return nil, fmt.Errorf("query server ID for slug '%s': %w", serverSlug, err)
	}

	// Now find the subscription and its headers
	subscriptionQuery := `SELECT "headerValues" FROM "Subscription" WHERE "userId" = $1 AND "serverId" = $2 LIMIT 1`
	var headersJSON sql.NullString
	err = db.QueryRow(subscriptionQuery, userID, serverID).Scan(&headersJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No active subscription found, return empty map
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("query subscription headers for user '%s', server '%s': %w", userID, serverSlug, err)
	}

	if !headersJSON.Valid || headersJSON.String == "" {
		return make(map[string]string), nil // Return empty map if NULL or empty
	}

	var headers map[string]string
	if err := json.Unmarshal([]byte(headersJSON.String), &headers); err != nil {
		return nil, fmt.Errorf("unmarshal subscription headers for user '%s', server '%s': %w", userID, serverSlug, err)
	}

	return headers, nil
}

func (c *DatabaseConfig) ServerName() (string, error) {
	return c.getSettingString("gateway_server_name", "Gate4AI Gateway")
}
func (c *DatabaseConfig) ServerVersion() (string, error) {
	return c.getSettingString("gateway_server_version", "1.0.0")
}
func (c *DatabaseConfig) LogLevel() (string, error) {
	return c.getSettingString("gateway_log_level", "info")
}
func (c *DatabaseConfig) DiscoveringHandlerPath() (string, error) {
	return c.getSettingString("path_for_discovering_handler", "")
}
func (c *DatabaseConfig) FrontendAddressForProxy() (string, error) {
	return c.getSettingString("url_how_gateway_proxy_connect_to_the_portal", "http://portal:3000")
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

func (c *DatabaseConfig) GetA2AAgentCard(agentURL string) (*a2aSchema.AgentCard, error) {
	info := &a2aSchema.AgentCard{URL: agentURL}
	var err error
	info.Name, err = c.getSettingString("a2a_agent_name", "Gate4AI A2A Agent")
	if err != nil {
		return info, err
	}
	desc, err := c.getSettingString("a2a_agent_description", "")
	if err != nil {
		return info, err
	}
	if desc != "" {
		info.Description = &desc
	}
	info.Version, err = c.getSettingString("a2a_agent_version", "1.0.0")
	if err != nil {
		return info, err
	}
	docURL, err := c.getSettingString("a2a_agent_documentation_url", "")
	if err != nil {
		return info, err
	}
	if docURL != "" {
		info.DocumentationURL = &docURL
	}
	info.DefaultInputModes, err = c.getSettingStringSlice("a2a_default_input_modes", []string{"text"})
	if err != nil {
		return info, err
	}
	info.DefaultOutputModes, err = c.getSettingStringSlice("a2a_default_output_modes", []string{"text"})
	if err != nil {
		return info, err
	}
	provOrg, errOrg := c.getSettingString("a2a_agent_provider_organization", "")
	provURL, errURL := c.getSettingString("a2a_agent_provider_url", "")
	if errOrg == nil && provOrg != "" {
		provider := a2aSchema.AgentProvider{Organization: provOrg}
		if errURL == nil && provURL != "" {
			provider.URL = &provURL
		}
		info.Provider = &provider
	} else if errOrg != nil && !errors.Is(errOrg, ErrNotFound) {
		return info, errOrg
	} else if errURL != nil && !errors.Is(errURL, ErrNotFound) {
		return info, errURL
	}
	authJSON, err := c.getSettingString("a2a_agent_authentication", "")
	if err == nil && authJSON != "" && authJSON != "{}" && authJSON != "null" {
		var auth a2aSchema.AgentAuthentication
		if jsonErr := json.Unmarshal([]byte(authJSON), &auth); jsonErr != nil {
			return info, fmt.Errorf("invalid a2a_agent_authentication: %w", jsonErr)
		}
		info.Authentication = &auth
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return info, err
	}
	return info, nil
}

// --- Database Helper Functions (Unchanged) ---
func (c *DatabaseConfig) getSettingRaw(key string) ([]byte, error) {
	db, err := sql.Open("postgres", c.dbConnectionString)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	defer db.Close()
	var valueStr sql.NullString // Use NullString to handle potential NULL
	err = db.QueryRowContext(context.Background(), `SELECT value FROM "Settings" WHERE key = $1 LIMIT 1`, key).Scan(&valueStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query setting '%s': %w", key, err)
	}
	if !valueStr.Valid {
		return nil, ErrNotFound
	} // Treat NULL as not found for consistency? Or return []byte("null")? Let's use ErrNotFound.
	return []byte(valueStr.String), nil
}
func (c *DatabaseConfig) getSettingJSON(key string) (interface{}, error) {
	raw, err := c.getSettingRaw(key)
	if err != nil {
		return nil, err
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("unmarshal setting '%s': %w", key, err)
	}
	return value, nil
}
func (c *DatabaseConfig) getSettingString(key string, defaultValue string) (string, error) {
	value, err := c.getSettingJSON(key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return defaultValue, nil
		}
		return defaultValue, err
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case float64:
		return fmt.Sprintf("%v", int(v)), nil
	default:
		return defaultValue, fmt.Errorf("setting '%s' has unexpected type %T", key, value)
	}
}
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
	if strSlice, ok := value.([]string); ok {
		return strSlice, nil
	}
	return defaultValue, fmt.Errorf("setting '%s' is not a JSON array of strings (type: %T)", key, value)
}
