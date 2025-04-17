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
	return []string{DBComponentName, MailhogComponentName, PrismaComponentName}, nil
}

// Start connects to the database and updates settings based on dependency info.
func (e *DBSettingsEnv) Start(ctx context.Context, envs *Envs) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		logPrefix := fmt.Sprintf("[%s] ", e.Name())
		defer close(resultChan)
		log.Printf("%sStarting component...", logPrefix)

		// --- Get Dependency Info ---
		log.Printf("%sFetching dependency information...", logPrefix)
		dbURL := envs.GetURL(DBComponentName)
		if dbURL == "" {
			err := fmt.Errorf("%sdatabase URL not available", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sDatabase URL: %s", logPrefix, dbURL)

		smtpDetailsRaw := envs.GetDetails(MailhogComponentName)
		smtpDetails, ok := smtpDetailsRaw.(SmtpServerDetails)
		if !ok {
			err := fmt.Errorf("%smailhog SMTP details not available or wrong type (%T)", logPrefix, smtpDetailsRaw)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sMailhog SMTP Details: %+v", logPrefix, smtpDetails)

		portalInternalURL := envs.GetURL(PortalComponentName)
		if portalInternalURL == "" {
			err := fmt.Errorf("%sportal internal URL not available", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sPortal Internal URL: %s", logPrefix, portalInternalURL)

		gatewayURL := envs.GetURL(GatewayComponentName)
		if gatewayURL == "" {
			err := fmt.Errorf("%sgateway URL not available", logPrefix)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sGateway Public URL: %s", logPrefix, gatewayURL)
		log.Printf("%sDependency information fetched successfully.", logPrefix)

		// --- Connect to Database ---
		log.Printf("%sConnecting to database...", logPrefix)
		db, err := sql.Open("postgres", dbURL)
		if err != nil {
			err = fmt.Errorf("%sfailed to open database connection: %w", logPrefix, err)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		defer db.Close()

		// Verify connection (optional but good practice)
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		if err != nil {
			err = fmt.Errorf("%sfailed to ping database: %w", logPrefix, err)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		log.Printf("%sDatabase connection successful.", logPrefix)

		// --- Update Settings ---
		log.Printf("%sUpdating database settings...", logPrefix)

		settingsToUpdate := []struct {
			key   string
			value interface{}
		}{
			{"email_smtp_server", smtpDetails},
			{"url_how_gateway_proxy_connect_to_the_portal", portalInternalURL},
			{"url_how_users_connect_to_the_portal", gatewayURL},
			{"general_gateway_address", gatewayURL},
			// Disable email sending by default for tests unless explicitly enabled elsewhere
			{"email_do_not_send_email", true},
		}

		// Update gateway_listen_address (extract port from public gateway URL)
		_, portStr, err := net.SplitHostPort(gatewayURL[len("http://"):]) // Remove "http://"
		if err != nil {
			err = fmt.Errorf("%sfailed to parse gateway URL '%s': %w", logPrefix, gatewayURL, err)
			log.Printf("%sERROR: %v", logPrefix, err)
			resultChan <- err
			return
		}
		gatewayListenAddress := fmt.Sprintf(":%s", portStr)
		settingsToUpdate = append(settingsToUpdate, struct {
			key   string
			value interface{}
		}{"gateway_listen_address", gatewayListenAddress})

		for _, setting := range settingsToUpdate {
			log.Printf("%sUpdating setting '%s'...", logPrefix, setting.key)
			if err := updateSettingInDB(ctx, db, setting.key, setting.value); err != nil {
				err = fmt.Errorf("%sfailed to update setting '%s': %w", logPrefix, setting.key, err)
				log.Printf("%sERROR: %v", logPrefix, err)
				resultChan <- err
				return
			}
			log.Printf("%sSetting '%s' updated.", logPrefix, setting.key)
		}

		log.Printf("%sDatabase settings updated successfully.", logPrefix)
		resultChan <- nil // Signal success
	}()

	return resultChan
}

// Helper function to update a setting in the Settings table.
func updateSettingInDB(ctx context.Context, db *sql.DB, key string, value interface{}) error {
	logPrefix := "[db-settings-updater] "
	// Marshal the value to JSON
	var valueJSON []byte
	var err error
	switch v := value.(type) {
	case string:
		// Ensure strings are quoted in JSON
		valueJSON = []byte(fmt.Sprintf("%q", v))
		log.Printf("%sMarshalled string for key '%s': %s", logPrefix, key, string(valueJSON))
	case json.RawMessage:
		valueJSON = v
		log.Printf("%sUsing raw JSON for key '%s': %s", logPrefix, key, string(valueJSON))
	default:
		valueJSON, err = json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal value for key '%s' to JSON: %w", key, err)
		}
		log.Printf("%sMarshalled default type for key '%s': %s", logPrefix, key, string(valueJSON))
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

	log.Printf("%sExecuting UPSERT for key '%s'", logPrefix, key)
	_, err = db.ExecContext(txCtx, query, key, valueJSON)
	if err != nil {
		return fmt.Errorf("failed to upsert setting '%s': %w", key, err)
	}
	log.Printf("%sUPSERT successful for key '%s'", logPrefix, key)
	return nil
}
