package env

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net" // For SplitHostPort
	"time"

	_ "github.com/lib/pq" // Import postgres driver
)

const DBSettingsComponentName = "db-settings"

// DBSettingsEnv updates specific settings in the database after dependencies are configured.
type DBSettingsEnv struct {
	BaseEnv
	// No component-specific state needed after completion.
}

// NewDBSettingsEnv creates a new DB settings component.
func NewDBSettingsEnv() *DBSettingsEnv {
	return &DBSettingsEnv{
		BaseEnv: BaseEnv{name: DBSettingsComponentName},
	}
}

// Configure declares dependencies needed to get URLs/details for updating settings.
// It depends on the database being ready, MailHog having SMTP details,
// and Portal/Gateway having their intended URLs allocated.
func (e *DBSettingsEnv) Configure(envs *Envs) (dependencies []string, err error) {
	// Dependencies needed *before* Start can run:
	// - database: Need DSN to connect.
	// - mailhog: Need SMTP details.

	// - portal: Only URL is needed.
	// - gateway: Only URL is needed.
	return []string{DBComponentName, MailhogComponentName, PrismaComponentName}, nil
}

// Start connects to the database and updates settings based on dependency info.
func (e *DBSettingsEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)
		log.Printf("Starting component: %s", e.Name())

		// --- Get Dependency Info ---
		dbURL := envs.GetURL(DBComponentName)
		if dbURL == "" {
			resultChan <- fmt.Errorf("%s: database URL not available", e.Name())
			return
		}

		smtpDetailsRaw := envs.GetDetails(MailhogComponentName)
		smtpDetails, ok := smtpDetailsRaw.(SmtpServerDetails)
		if !ok {
			resultChan <- fmt.Errorf("%s: mailhog SMTP details not available or wrong type (%T)", e.Name(), smtpDetailsRaw)
			return
		}

		// Get *intended* URLs from portal/gateway (available after their Configure phase)
		portalInternalURL := envs.GetURL(PortalComponentName)
		if portalInternalURL == "" {
			resultChan <- fmt.Errorf("%s: portal internal URL not available", e.Name())
			return
		}

		gatewayURL := envs.GetURL(GatewayComponentName)
		if gatewayURL == "" {
			resultChan <- fmt.Errorf("%s: gateway URL not available", e.Name())
			return
		}

		// --- Connect to Database ---
		db, err := sql.Open("postgres", dbURL)
		if err != nil {
			resultChan <- fmt.Errorf("%s: failed to open database connection: %w", e.Name(), err)
			return
		}
		defer db.Close()

		// Verify connection (optional but good practice)
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		if err != nil {
			resultChan <- fmt.Errorf("%s: failed to ping database: %w", e.Name(), err)
			return
		}

		// --- Update Settings ---
		log.Printf("%s: Updating database settings...", e.Name())

		// Update email SMTP server settings
		if err := updateSettingInDB(ctx, db, "email_smtp_server", smtpDetails); err != nil {
			resultChan <- fmt.Errorf("%s: failed to update email_smtp_server: %w", e.Name(), err)
			return
		}

		// Update gateway_listen_address (extract port from public gateway URL)
		_, portStr, err := net.SplitHostPort(gatewayURL[len("http://"):]) // Remove "http://"
		if err != nil {
			resultChan <- fmt.Errorf("%s: failed to parse gateway URL '%s': %w", e.Name(), gatewayURL, err)
			return
		}
		gatewayListenAddress := fmt.Sprintf(":%s", portStr)
		if err := updateSettingInDB(ctx, db, "gateway_listen_address", gatewayListenAddress); err != nil {
			resultChan <- fmt.Errorf("%s: failed to update gateway_listen_address: %w", e.Name(), err)
			return
		}

		// Update frontend proxy address (use the INTERNAL portal URL)
		if err := updateSettingInDB(ctx, db, "url_how_gateway_proxy_connect_to_the_portal", portalInternalURL); err != nil {
			resultChan <- fmt.Errorf("%s: failed to update url_how_gateway_proxy_connect_to_the_portal: %w", e.Name(), err)
			return
		}

		// Update the base URL users connect to (the PUBLIC gateway URL)
		if err := updateSettingInDB(ctx, db, "url_how_users_connect_to_the_portal", gatewayURL); err != nil {
			resultChan <- fmt.Errorf("%s: failed to update url_how_users_connect_to_the_portal: %w", e.Name(), err)
			return
		}

		// Update general gateway address (the PUBLIC gateway URL)
		if err := updateSettingInDB(ctx, db, "general_gateway_address", gatewayURL); err != nil {
			resultChan <- fmt.Errorf("%s: failed to update general_gateway_address: %w", e.Name(), err)
			return
		}

		log.Printf("%s: Database settings updated successfully.", e.Name())
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Helper function to update a setting in the Settings table.
func updateSettingInDB(ctx context.Context, db *sql.DB, key string, value interface{}) error {
	// Marshal the value to JSON
	var valueJSON []byte
	var err error
	switch v := value.(type) {
	case string:
		// Ensure strings are quoted in JSON
		valueJSON = []byte(fmt.Sprintf("%q", v))
	case json.RawMessage:
		valueJSON = v
	default:
		valueJSON, err = json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value for key '%s' to JSON: %w", key, err)
		}
	}

	// Use context for the transaction/query
	txCtx, cancel := context.WithTimeout(ctx, 10*time.Second) // Timeout for DB operation
	defer cancel()

	// Use UPSERT logic for simplicity (requires PostgreSQL 9.5+)
	// Assumes 'key' is the primary key or has a unique constraint.
	query := `
        INSERT INTO "Settings" (id, key, "group", name, description, value, frontend, "updatedAt", "createdAt")
        VALUES ($1, $1, 'test', $1, $1, $2, false, NOW(), NOW())
        ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
    `
	// Note: Using default values for group, name, description, frontend. Adjust if needed.

	_, err = db.ExecContext(txCtx, query, key, valueJSON)
	if err != nil {
		return fmt.Errorf("failed to upsert setting '%s': %w", key, err)
	}
	// log.Printf("Updated/Inserted setting: %s", key)
	return nil
}

/*

// updateSetting updates a setting in the database
func updateSetting(key string, value interface{}) error {
	// Connect to the database
	db, err := sql.Open("postgres", DATABASE_URL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	// Marshal the value to JSON
	var valueJSON []byte
	switch v := value.(type) {
	case string:
		valueJSON = []byte(fmt.Sprintf("%q", v))
	case json.RawMessage:
		valueJSON = v
	default:
		valueJSON, err = json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value to JSON: %w", err)
		}
	}

	// Check if the setting exists
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM "Settings" WHERE key = $1`, key).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check if setting exists: %w", err)
	}

	if count > 0 {
		// Update existing setting
		_, err = db.Exec(`UPDATE "Settings" SET value = $1 WHERE key = $2`, valueJSON, key)
		if err != nil {
			return fmt.Errorf("failed to update setting: %w", err)
		}
	} else {
		// Insert new setting with minimal required fields
		_, err = db.Exec(`INSERT INTO "Settings" (key, "group", name, description, value, frontend) VALUES ($1, 'test', $1, $1, $2, false)`,
			key, valueJSON)
		if err != nil {
			return fmt.Errorf("failed to insert setting: %w", err)
		}
	}

	return nil
}

*/
