package tests

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSettingsEnvAccess tests access control for the Environment Variables tab.
func TestSettingsEnv_RegularUserAccessDenied(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Create regular user
	regularUser, err := createUser(am)
	require.NoError(t, err, "Failed to create regular user")

	// Log in as regular user
	require.NoError(t, loginUser(am, regularUser.Email, regularUser.Password), "Regular user login failed")

	// Navigate to settings
	am.OpenPageWithURL("/settings")

	_, errSnack := am.WaitForLocatorWithDebug(".v-snackbar__content:has-text('Access Denied: Settings page requires Admin or Security role.')", "snackbar_Access_Denied")
	require.NoError(t, errSnack, "Access Denied: Settings page requires Admin or Security role")

	envTabSelector := "[data-testid='settings-tab-environment']" 
	_, err = am.WaitForLocatorWithDebug(envTabSelector, "environment_tab_admin", 5000)
	require.Error(t, err, "Could not find Environment tab for admin")
}


func TestSettingsEnv_AdminUserAccessGranted(t *testing.T) {
	am := NewArtifactManager(t)
	defer am.Close()

	// Get admin user
	adminUser, err := getAdminUser()
	require.NoError(t, err, "Failed to get admin user credentials")

	// Log in as admin
	require.NoError(t, loginUser(am, adminUser.Email, adminUser.Password), "Admin login failed")

	// Navigate to settings
	am.OpenPageWithURL("/settings")
	_, err = am.WaitForLocatorWithDebug("h1:has-text('Settings')", "settings_heading_admin")
	require.NoError(t, err, "Failed to load settings page heading for admin user")

	// Verify "Environment" tab is ENABLED using data-testid
	envTabSelector := "[data-testid='settings-tab-environment']" 
	envTab, err := am.WaitForLocatorWithDebug(envTabSelector, "environment_tab_admin", 5000)
	require.NoError(t, err, "Could not find Environment tab for admin")

	// Click the Environment tab
	envTab.Click()

	// Wait for the content to load (check for the table using data-testid)
	envTableSelector := "[data-testid='env-vars-table']" // Use data-testid
	_, err = am.WaitForLocatorWithDebug(envTableSelector, "environment_table_admin", 15000) // Increased timeout for API call
	require.NoError(t, err, "Environment variables table did not load for admin user")

	// Verify specific known environment variables are present (check keys, not sensitive values)
	knownKeys := []string{
		"NODE_ENV",
		"NUXT_PORT",
		"PORT",
		"GATE4AI_DATABASE_URL",
		"NUXT_JWT_SECRET",
		"NUXT_PUBLIC_API_BASE_URL",
		"URL_HOW_USERS_CONNECT_TO_THE_PORTAL",
	}

	for _, key := range knownKeys {
		// Look for the table c	})ell containing the key name using data-testid
		keyCellSelector := fmt.Sprintf("[data-testid='env-var-key-%s']", key) // Use data-testid

		// Use the keyCell variable in the check
		_, err = am.WaitForLocatorWithDebug(keyCellSelector, fmt.Sprintf("env_var_key_%s", key), 5000)

		envValue, envExists := os.LookupEnv(key)
		if err != nil {
			if envExists {
				t.Logf("Warning: Could not find key '%s' in the settings table using selector '%s', but it exists in test environment (value: %s). Error: %v", key, keyCellSelector, envValue, err)
			} else {
				t.Logf("Warning: Could not find key '%s' in the settings table using selector '%s', and it might not be set in the test environment. Error: %v", key, keyCellSelector, err)
			}
			if key == "NODE_ENV" || key == "GATE4AI_DATABASE_URL" {
				require.NoError(t, err, "Expected environment variable key '%s' was not found in the table", key)
			}
		} else {
			t.Logf("Found environment variable key '%s' in the table.", key)
			// You could optionally use keyCell here for further assertions if needed, e.g., checking adjacent value cell
			// valueCellSelector := fmt.Sprintf("[data-testid='env-var-value-%s']", key)
			// valueCell := envTable.Locator(valueCellSelector)
			// require.NoError(t, valueCell.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible}))
		}
	}

	// Example: Asserting a specific value (use cautiously) using data-testid
	nodeEnvValueSelector := "[data-testid='env-var-value-NODE_ENV']"
	nodeEnvCell, err := am.WaitForLocatorWithDebug(nodeEnvValueSelector, "env_var_value_NODE_ENV")
	require.NoError(t, err, "Failed to find NODE_ENV value cell")
	nodeEnvValue, err := nodeEnvCell.TextContent()
	require.NoError(t, err, "Failed to get NODE_ENV value")
	require.Equal(t, "production", nodeEnvValue, "NODE_ENV should be 'production'")

	am.SaveScreenshot("settings_env_access_granted")
}