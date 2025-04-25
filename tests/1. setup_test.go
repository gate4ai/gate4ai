package tests

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	// Import the new env package
	"github.com/gate4ai/gate4ai/tests/env"
	// Import types needed for details retrieval
	"github.com/playwright-community/playwright-go"
	// Import details types defined in components
	// "github.com/gate4ai/tests/env" // Already imported
)

const TEST_CONFIG_WORKSPACE_FOLDER = ".."

// Global variables for backward compatibility with existing tests.
// They will be populated after the environment setup is complete.
var (
	DATABASE_URL               string
	PORTAL_URL                 string                // Public URL (usually Gateway)
	PORTAL_INTERNAL_URL        string                // URL portal server listens on directly
	GATEWAY_URL                string                // Public URL
	EXAMPLE_MCP2024_SERVER_URL string                // Specific example endpoint URL
	EXAMPLE_MCP2025_SERVER_URL string                // Specific example endpoint URL
	MAILHOG_API_URL            string                // Mailhog Web UI URL
	EMAIL_SMTP_SERVER          env.SmtpServerDetails // Use the type defined in mailhog.go
	pw                         *playwright.Playwright
)

func TestMain(m *testing.M) {
	exitCode := 1 // Default to failure
	defer func() {
		log.Println("Exiting TestMain...")
		os.Exit(exitCode)
	}()

	// Register all environment components using the global registry
	// Use the constants defined within each component package where available.
	env.Register(
		env.NewDBEnv(),
		env.NewMailHogEnv(),
		//env.NewA2AServerEnv(),
		env.NewPlaywrightEnv(),
		env.NewPrismaEnv(),
		env.NewDBSettingsEnv(),
		env.NewPortalServerEnv(),
		env.NewGatewayServerEnv(),
		env.NewExampleServerEnv(),
	)

	// Use a context with timeout for the entire setup
	setupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // 5 min timeout for setup
	defer cancel()

	// Execute the environment setup
	err := env.Execute(setupCtx)

	// Defer cleanup regardless of setup success or failure
	defer func() {
		log.Println("Running deferred cleanup via env.StopAll()...")
		env.StopAll()
		log.Println("Deferred cleanup finished.")
	}()

	if err != nil {
		log.Printf("FATAL: Test environment setup failed: %v", err)
		// env.Execute already attempted cleanup for started components on error.
		return // Exit code remains 1
	}

	// --- Setup successful ---
	log.Println("Test environment setup successful.")

	// Populate global variables using the env package's global getters
	log.Println("Populating global variables...")
	DATABASE_URL = env.GetURL(env.DBComponentName)
	GATEWAY_URL = env.GetURL(env.GatewayComponentName)
	PORTAL_INTERNAL_URL = env.GetURL(env.PortalComponentName)
	PORTAL_URL = GATEWAY_URL                               // Tests access portal via gateway
	MAILHOG_API_URL = env.GetURL(env.MailhogComponentName) // MailhogEnv.URL() returns API URL

	// Populate details using GetDetails and type assertions
	details := env.GetDetails(env.PlaywrightComponentName)
	if p, ok := details.(*playwright.Playwright); ok {
		pw = p
		log.Printf("Playwright client retrieved (%T).", pw)
	} else {
		log.Printf("Warning: Could not retrieve Playwright client or details have wrong type (%T).", details)
	}

	details = env.GetDetails(env.MailhogComponentName) // MailhogEnv.GetDetails() returns SmtpServerDetails
	if smtpDetails, ok := details.(env.SmtpServerDetails); ok {
		EMAIL_SMTP_SERVER = smtpDetails
		log.Printf("MailHog SMTP details retrieved (%+v).", EMAIL_SMTP_SERVER)
	} else {
		log.Printf("Warning: Could not retrieve MailHog SMTP details or details have wrong type (%T).", details)
	}

	details = env.GetDetails(env.ExampleServerComponentName) // ExampleServerEnv.GetDetails() returns ExampleServerDetails
	if exDetails, ok := details.(env.ExampleServerDetails); ok {
		EXAMPLE_MCP2024_SERVER_URL = exDetails.MCP2024URL
		EXAMPLE_MCP2025_SERVER_URL = exDetails.MCP2025URL
		log.Printf("Example Server details retrieved (MCP2024: %s, MCP2025: %s).", EXAMPLE_MCP2024_SERVER_URL, EXAMPLE_MCP2025_SERVER_URL)
	} else {
		log.Printf("Warning: Could not retrieve Example Server details or details have wrong type (%T).", details)
	}

	// Log final startup times
	log.Println("--- Component Startup Times ---")
	// Get registered component names directly from the manager if possible, otherwise list known names
	// We need a way to get names, for now list them based on registration:
	componentNames := []string{
		env.DBComponentName,
		env.MailhogComponentName,
		//		env.A2AServerComponentName,
		env.PlaywrightComponentName,
		env.PrismaComponentName,
		env.DBSettingsComponentName,
		env.PortalComponentName,
		env.GatewayComponentName,
		env.ExampleServerComponentName,
	}
	for _, name := range componentNames {
		// Check if component exists and started (duration > 0 implies successful start)
		duration := env.GetStartDuration(name)
		if _, exists := env.GetComponent(name); exists {
			if duration > 0 {
				log.Printf("- %s: %.3fs", name, duration.Seconds())
			} else {
				// This case might occur if GetStartDuration is called before start finishes,
				// or if the component failed very early, or doesn't embed BaseEnv correctly.
				log.Printf("- %s: (started, but duration not recorded or zero)", name)
			}
		} else {
			// This shouldn't happen if the list matches registered components
			log.Printf("- %s: (component not found)", name)
		}
	}
	log.Println("-----------------------------")

	// Run the tests
	log.Println("Running tests...")
	exitCode = m.Run()
	log.Printf("Test run finished with exit code: %d", exitCode)

	// Deferred cleanup (env.StopAll()) will run after this point
}
